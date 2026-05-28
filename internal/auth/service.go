// Package auth owns user signup, sessions, login/logout, and password reset.
// All database access is funnelled through Service so handlers stay thin.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dblanc/hearth/internal/shared/email"
)

const (
	SessionLifetime            = 30 * 24 * time.Hour
	VerificationTokenLifetime  = 24 * time.Hour
	PasswordResetTokenLifetime = 1 * time.Hour
	FailedLoginWindow          = 15 * time.Minute
	FailedLoginLimit           = 10
	ResendVerificationLimit    = 3
	ResendVerificationWindow   = time.Hour
)

type Service struct {
	DB      *sql.DB
	Email   email.Sender
	BaseURL string
	Now     func() time.Time // overridable for tests
}

func New(db *sql.DB, e email.Sender, baseURL string) *Service {
	return &Service{DB: db, Email: e, BaseURL: strings.TrimRight(baseURL, "/"), Now: time.Now}
}

// ---- signup ----

var (
	ErrUsernameTaken    = errors.New("auth: username taken")
	ErrEmailTaken       = errors.New("auth: email taken")
	ErrInvalidLogin     = errors.New("auth: invalid email or password")
	ErrTooMany          = errors.New("auth: too many attempts")
	ErrTokenInvalid     = errors.New("auth: token not found or already used")
	ErrTokenExpired     = errors.New("auth: token expired")
	ErrSendVerification = errors.New("auth: send verification email failed")
)

type SignupInput struct {
	Username  string
	Email     string
	Password  string
	FirstName string
	LastName  string
}

func (s *Service) Signup(ctx context.Context, in SignupInput) (int64, error) {
	in.Username = NormaliseUsername(in.Username)
	in.Email = NormaliseEmail(in.Email)
	in.FirstName = strings.TrimSpace(in.FirstName)
	in.LastName = strings.TrimSpace(in.LastName)

	hash, err := HashPassword(in.Password)
	if err != nil {
		return 0, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, first_name, last_name) VALUES (?, ?, ?, ?, ?)`,
		in.Username, in.Email, hash, in.FirstName, in.LastName)
	if err != nil {
		if isUniqueViolation(err, "username") {
			return 0, ErrUsernameTaken
		}
		if isUniqueViolation(err, "email") {
			return 0, ErrEmailTaken
		}
		return 0, err
	}
	userID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	token, err := s.issueVerificationToken(ctx, tx, userID)
	if err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	if err := s.sendVerificationEmail(ctx, in.Email, in.FirstName, token); err != nil {
		// Roll back by deleting the just-created user so the email address is
		// free to try again. Best-effort — ignore delete error.
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
		return 0, fmt.Errorf("%w: %w", ErrSendVerification, err)
	}
	return userID, nil
}

func (s *Service) issueVerificationToken(ctx context.Context, tx *sql.Tx, userID int64) (string, error) {
	token, err := RandomToken(32)
	if err != nil {
		return "", err
	}
	expires := s.Now().Add(VerificationTokenLifetime)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO email_verifications (user_id, token_hash, expires_at) VALUES (?, ?, ?)`,
		userID, HashToken(token), expires)
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) sendVerificationEmail(ctx context.Context, to, displayName, token string) error {
	link := fmt.Sprintf("%s/verify?token=%s", s.BaseURL, token)
	body := fmt.Sprintf(`Hi %s,

Welcome to Hearth. Confirm your email address to activate your account:

%s

This link expires in 24 hours. If you didn't sign up, you can ignore this email.
`, displayName, link)
	return s.Email.Send(ctx, email.Message{
		To: to, Subject: "Confirm your Hearth email", TextBody: body,
	})
}

// ---- verification ----

// VerifyEmail consumes a verification token and marks the user verified.
// Returns the verified user ID on success.
func (s *Service) VerifyEmail(ctx context.Context, token string) (int64, error) {
	hash := HashToken(token)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	var (
		id, userID int64
		expiresAt  time.Time
		consumed   sql.NullTime
	)
	err = tx.QueryRowContext(ctx,
		`SELECT id, user_id, expires_at, consumed_at FROM email_verifications WHERE token_hash = ?`, hash,
	).Scan(&id, &userID, &expiresAt, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrTokenInvalid
	}
	if err != nil {
		return 0, err
	}
	if consumed.Valid {
		return 0, ErrTokenInvalid
	}
	if s.Now().After(expiresAt) {
		return 0, ErrTokenExpired
	}
	now := s.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE email_verifications SET consumed_at = ? WHERE id = ?`, now, id); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET email_verified_at = ? WHERE id = ? AND email_verified_at IS NULL`, now, userID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

// ResendVerification issues a fresh token (rate-limited per user).
func (s *Service) ResendVerification(ctx context.Context, userID int64) error {
	windowStart := s.Now().Add(-ResendVerificationWindow)
	var recent int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM email_verifications WHERE user_id = ? AND created_at > ?`,
		userID, windowStart).Scan(&recent); err != nil {
		return err
	}
	if recent >= ResendVerificationLimit {
		return ErrTooMany
	}
	var (
		emailAddr, firstName string
		verifiedAt           sql.NullTime
	)
	if err := s.DB.QueryRowContext(ctx,
		`SELECT email, first_name, email_verified_at FROM users WHERE id = ? AND deleted_at IS NULL`,
		userID).Scan(&emailAddr, &firstName, &verifiedAt); err != nil {
		return err
	}
	if verifiedAt.Valid {
		return nil // already verified — no-op
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	token, err := s.issueVerificationToken(ctx, tx, userID)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.sendVerificationEmail(ctx, emailAddr, firstName, token)
}

// ---- login / logout / sessions ----

type Session struct {
	ID        int64
	UserID    int64
	Token     string // plaintext, only populated when freshly created
	ExpiresAt time.Time
}

// Authenticate returns the user ID and a new session token on success. It is
// deliberately written so wrong-email and wrong-password take the same code
// path and return the same error.
func (s *Service) Authenticate(ctx context.Context, emailIn, password, ip string) (*Session, error) {
	if blocked, err := s.ipBlocked(ctx, ip); err != nil {
		return nil, err
	} else if blocked {
		return nil, ErrTooMany
	}

	emailIn = NormaliseEmail(emailIn)
	var (
		userID int64
		hash   string
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, password_hash FROM users WHERE email = ? AND deleted_at IS NULL`, emailIn,
	).Scan(&userID, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		// Still hash to avoid timing oracle on email existence.
		_, _ = HashPassword(password)
		s.recordFailedLogin(ctx, ip)
		return nil, ErrInvalidLogin
	}
	if err != nil {
		return nil, err
	}
	ok, err := VerifyPassword(password, hash)
	if err != nil || !ok {
		s.recordFailedLogin(ctx, ip)
		return nil, ErrInvalidLogin
	}
	return s.CreateSession(ctx, userID)
}

// CheckPassword verifies a user's password without issuing a session. Used by
// flows that need re-authentication (e.g. account deletion).
func (s *Service) CheckPassword(ctx context.Context, userID int64, password string) (bool, error) {
	var hash string
	err := s.DB.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ? AND deleted_at IS NULL`, userID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return VerifyPassword(password, hash)
}

func (s *Service) ipBlocked(ctx context.Context, ip string) (bool, error) {
	if ip == "" {
		return false, nil
	}
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM failed_logins WHERE ip = ? AND attempted_at > ?`,
		ip, s.Now().Add(-FailedLoginWindow),
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n >= FailedLoginLimit, nil
}

func (s *Service) recordFailedLogin(ctx context.Context, ip string) {
	if ip == "" {
		return
	}
	_, _ = s.DB.ExecContext(ctx, `INSERT INTO failed_logins (ip) VALUES (?)`, ip)
}

func (s *Service) CreateSession(ctx context.Context, userID int64) (*Session, error) {
	token, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	expires := s.Now().Add(SessionLifetime)
	res, err := s.DB.ExecContext(ctx,
		`INSERT INTO sessions (user_id, token_hash, expires_at) VALUES (?, ?, ?)`,
		userID, HashToken(token), expires,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Session{ID: id, UserID: userID, Token: token, ExpiresAt: expires}, nil
}

// LoadSession resolves a session cookie value to the owning user. It also
// refreshes last_seen_at as a sliding-expiry signal. Returns nil with no error
// when the cookie is missing/invalid/expired.
func (s *Service) LoadSession(ctx context.Context, token string) (*SessionUser, error) {
	if token == "" {
		return nil, nil
	}
	hash := HashToken(token)
	var (
		sid           int64
		userID        int64
		expiresAt     time.Time
		username      string
		firstName     string
		lastName      string
		emailVerified sql.NullTime
		deletedAt     sql.NullTime
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT s.id, s.user_id, s.expires_at, u.username, u.first_name, u.last_name, u.email_verified_at, u.deleted_at
		  FROM sessions s
		  JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ?`, hash,
	).Scan(&sid, &userID, &expiresAt, &username, &firstName, &lastName, &emailVerified, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if deletedAt.Valid || s.Now().After(expiresAt) {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sid)
		return nil, nil
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, s.Now(), sid)
	return &SessionUser{
		ID: userID, Username: username, FirstName: firstName, LastName: lastName, Verified: emailVerified.Valid,
	}, nil
}

// SessionUser is the subset of user fields needed for request-context purposes.
type SessionUser struct {
	ID        int64
	Username  string
	FirstName string
	LastName  string
	Verified  bool
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	return err
}

func (s *Service) DeleteAllSessionsFor(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// ---- password reset ----

// RequestPasswordReset creates a reset token and emails the link IFF the email
// exists. Always returns nil so callers don't leak existence; rate-limiting
// is applied per email and per IP.
func (s *Service) RequestPasswordReset(ctx context.Context, emailIn, ip string) error {
	emailIn = NormaliseEmail(emailIn)
	var (
		userID    int64
		firstName string
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, first_name FROM users WHERE email = ? AND deleted_at IS NULL`, emailIn,
	).Scan(&userID, &firstName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	windowStart := s.Now().Add(-time.Hour)
	var emailCount, ipCount int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM password_resets WHERE user_id = ? AND created_at > ?`,
		userID, windowStart).Scan(&emailCount); err != nil {
		return err
	}
	if ip != "" {
		if err := s.DB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM password_resets WHERE requester_ip = ? AND created_at > ?`,
			ip, windowStart).Scan(&ipCount); err != nil {
			return err
		}
	}
	if emailCount >= 3 || ipCount >= 3 {
		return nil // silently drop — same response either way
	}

	token, err := RandomToken(32)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO password_resets (user_id, token_hash, expires_at, requester_ip) VALUES (?, ?, ?, ?)`,
		userID, HashToken(token), s.Now().Add(PasswordResetTokenLifetime), ip,
	)
	if err != nil {
		return err
	}
	link := fmt.Sprintf("%s/password/reset?token=%s", s.BaseURL, token)
	body := fmt.Sprintf(`Hi %s,

Use the link below to reset your Hearth password. It expires in 1 hour.

%s

If you didn't request this, you can safely ignore the email.
`, firstName, link)
	return s.Email.Send(ctx, email.Message{To: emailIn, Subject: "Reset your Hearth password", TextBody: body})
}

// ConsumePasswordReset validates a reset token and updates the password.
// Terminates all sessions for that user on success.
func (s *Service) ConsumePasswordReset(ctx context.Context, token, newPassword string) error {
	hash := HashToken(token)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	var (
		id        int64
		userID    int64
		expiresAt time.Time
		consumed  sql.NullTime
	)
	err = tx.QueryRowContext(ctx,
		`SELECT id, user_id, expires_at, consumed_at FROM password_resets WHERE token_hash = ?`, hash,
	).Scan(&id, &userID, &expiresAt, &consumed)
	if errors.Is(err, sql.ErrNoRows) || consumed.Valid {
		return ErrTokenInvalid
	}
	if err != nil {
		return err
	}
	if s.Now().After(expiresAt) {
		return ErrTokenExpired
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, newHash, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE password_resets SET consumed_at = ? WHERE id = ?`, s.Now(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// isUniqueViolation reports whether the given error is a SQLite UNIQUE
// constraint failure on a column whose name contains the given substring.
func isUniqueViolation(err error, columnSubstr string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") && strings.Contains(msg, columnSubstr)
}
