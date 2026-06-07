// Package connections owns the social graph: invites, connection requests,
// accept/deny, disconnect, and the IsConnected privacy check that gates every
// cross-user content endpoint (CLAUDE.md §1).
package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/shared/db"
)

const (
	InviteLifetime = 72 * time.Hour
	InviteWindow   = 7 * 24 * time.Hour
	InviteLimit    = 20

	ConnectWindow = 7 * 24 * time.Hour
	ConnectLimit  = 10
)

var (
	ErrInviteInvalid    = errors.New("connections: invite not found, expired, or already used")
	ErrSelfConnect      = errors.New("connections: cannot connect to yourself")
	ErrAlreadyConnected = errors.New("connections: already connected")
	ErrRequestNotFound  = errors.New("connections: request not found")
	ErrRequestResolved  = errors.New("connections: request already resolved")
	ErrNotRecipient     = errors.New("connections: not the request recipient")
)

// RateLimitError reports that a per-window limit was hit and when it lifts.
type RateLimitError struct {
	Kind   string // "invite" or "connection"
	LiftAt time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("connections: %s rate limit reached until %s", e.Kind, e.LiftAt.Format(time.RFC3339))
}

type Service struct {
	DB      *sql.DB
	Notifs  *notifications.Service
	BaseURL string
	Now     func() time.Time
}

func New(d *sql.DB, n *notifications.Service, baseURL string) *Service {
	return &Service{DB: d, Notifs: n, BaseURL: strings.TrimRight(baseURL, "/"), Now: time.Now}
}

// inviteURL builds the shareable invite link for a token.
func (s *Service) inviteURL(token string) string {
	return s.BaseURL + "/i/" + token
}

// ---- privacy / graph reads ----

// IsConnected reports whether the two users share an active connection. This is
// the single chokepoint every cross-user content endpoint must call
// (CLAUDE.md §1). A user is never "connected" to themselves.
func (s *Service) IsConnected(ctx context.Context, viewerID, authorID int64) (bool, error) {
	if viewerID == 0 || authorID == 0 || viewerID == authorID {
		return false, nil
	}
	a, b := order(viewerID, authorID)
	var one int
	err := s.DB.QueryRowContext(ctx,
		`SELECT 1 FROM connections WHERE user_a_id = ? AND user_b_id = ?`, a, b,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ConnectionIDs returns the user IDs the given user is connected to. Used by
// the feed query.
func (s *Service) ConnectionIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT user_a_id, user_b_id FROM connections WHERE user_a_id = ? OR user_b_id = ?`,
		userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		if a == userID {
			ids = append(ids, b)
		} else {
			ids = append(ids, a)
		}
	}
	return ids, rows.Err()
}

// Person is a connection rendered for a list. Names only — never a count
// (CLAUDE.md §2).
type Person struct {
	ID       int64
	Username string
	Name     string
	PhotoKey string
}

// ListConnections returns the user's connections by display name. Soft-deleted
// users are excluded.
func (s *Service) ListConnections(ctx context.Context, userID int64) ([]Person, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT u.id, u.username, u.first_name, u.last_name, COALESCE(u.photo_key, '')
		  FROM connections c
		  JOIN users u ON u.id = CASE WHEN c.user_a_id = ? THEN c.user_b_id ELSE c.user_a_id END
		 WHERE (c.user_a_id = ? OR c.user_b_id = ?) AND u.deleted_at IS NULL
		 ORDER BY u.first_name, u.last_name`, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var people []Person
	for rows.Next() {
		var (
			p           Person
			first, last string
		)
		if err := rows.Scan(&p.ID, &p.Username, &first, &last, &p.PhotoKey); err != nil {
			return nil, err
		}
		p.Name = fullName(first, last)
		people = append(people, p)
	}
	return people, rows.Err()
}

// ---- invites ----

type Invite struct {
	Token     string
	CreatedAt time.Time
	ExpiresAt time.Time
	Consumed  bool
	Expired   bool
}

// CreateInvite issues a new invite token for the sender, enforcing the
// 20-per-trailing-7-days limit inside an immediate transaction so the count is
// race-free at the boundary (CLAUDE.md §7).
func (s *Service) CreateInvite(ctx context.Context, senderID int64) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	now := s.Now()
	windowStart := now.Add(-InviteWindow)

	err = db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM invites WHERE sender_id = ? AND created_at > ?`,
			senderID, windowStart).Scan(&count); err != nil {
			return err
		}
		if count >= InviteLimit {
			// Select the column directly (not MIN) so SQLite keeps the TIMESTAMP
			// type affinity; aggregates come back as untyped strings.
			var oldest time.Time
			if err := conn.QueryRowContext(ctx,
				`SELECT created_at FROM invites WHERE sender_id = ? AND created_at > ? ORDER BY created_at ASC LIMIT 1`,
				senderID, windowStart).Scan(&oldest); err != nil {
				return err
			}
			return &RateLimitError{Kind: "invite", LiftAt: oldest.Add(InviteWindow)}
		}
		_, err := conn.ExecContext(ctx,
			`INSERT INTO invites (sender_id, token, created_at, expires_at) VALUES (?, ?, ?, ?)`,
			senderID, token, now, now.Add(InviteLifetime))
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// ListInvites returns the user's invites, newest first, flagged as
// consumed/expired for display.
func (s *Service) ListInvites(ctx context.Context, senderID int64) ([]Invite, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT token, created_at, expires_at, consumed_at
		   FROM invites WHERE sender_id = ? ORDER BY created_at DESC`, senderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	var invites []Invite
	for rows.Next() {
		var (
			inv      Invite
			consumed sql.NullTime
		)
		if err := rows.Scan(&inv.Token, &inv.CreatedAt, &inv.ExpiresAt, &consumed); err != nil {
			return nil, err
		}
		inv.Consumed = consumed.Valid
		inv.Expired = !inv.Consumed && now.After(inv.ExpiresAt)
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// ---- invite resolution (the /i/{token} state machine) ----

type InviteState int

const (
	// StateInvalid means not found, expired, consumed, or the sender is gone.
	StateInvalid InviteState = iota
	StateSelf
	StateAlreadyConnected
	StateValid
)

type Resolution struct {
	State          InviteState
	InviteID       int64
	SenderID       int64
	SenderUsername string
	SenderName     string
	SenderPhotoKey string
}

// Resolve evaluates an invite token against the viewer's identity. viewerID is
// 0 for anonymous visitors. The result maps directly onto the §4.2 state table.
func (s *Service) Resolve(ctx context.Context, token string, viewerID int64) (Resolution, error) {
	var (
		res         Resolution
		expiresAt   time.Time
		consumed    sql.NullTime
		first, last string
		photo       sql.NullString
		deletedAt   sql.NullTime
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT i.id, i.sender_id, i.expires_at, i.consumed_at,
		       u.username, u.first_name, u.last_name, u.photo_key, u.deleted_at
		  FROM invites i
		  JOIN users u ON u.id = i.sender_id
		 WHERE i.token = ?`, token,
	).Scan(&res.InviteID, &res.SenderID, &expiresAt, &consumed,
		&res.SenderUsername, &first, &last, &photo, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Resolution{State: StateInvalid}, nil
	}
	if err != nil {
		return Resolution{}, err
	}
	if consumed.Valid || deletedAt.Valid || s.Now().After(expiresAt) {
		return Resolution{State: StateInvalid}, nil
	}
	res.SenderName = fullName(first, last)
	res.SenderPhotoKey = photo.String

	if viewerID == 0 {
		res.State = StateValid // anonymous: handler sets cookie + redirects
		return res, nil
	}
	if viewerID == res.SenderID {
		res.State = StateSelf
		return res, nil
	}
	connected, err := s.IsConnected(ctx, viewerID, res.SenderID)
	if err != nil {
		return Resolution{}, err
	}
	if connected {
		res.State = StateAlreadyConnected
		return res, nil
	}
	res.State = StateValid
	return res, nil
}

// ---- connection requests ----

// CreateRequest consumes the invite and records a pending connection request
// from requesterID to the invite's sender, notifying the sender. The invite is
// consumed here (not at link-open time, CLAUDE.md §5). Returns ErrInviteInvalid
// if the invite was already used or is otherwise unusable, and ErrSelfConnect /
// ErrAlreadyConnected for those states.
func (s *Service) CreateRequest(ctx context.Context, token string, requesterID int64) error {
	res, err := s.Resolve(ctx, token, requesterID)
	if err != nil {
		return err
	}
	switch res.State {
	case StateInvalid:
		return ErrInviteInvalid
	case StateSelf:
		return ErrSelfConnect
	case StateAlreadyConnected:
		return ErrAlreadyConnected
	}

	return db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		// Consume the invite atomically; 0 rows means someone beat us to it.
		r, err := conn.ExecContext(ctx,
			`UPDATE invites SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`,
			s.Now(), res.InviteID)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrInviteInvalid
		}
		ins, err := conn.ExecContext(ctx, `
			INSERT INTO connection_requests (invite_id, requester_id, recipient_id, status, created_at)
			VALUES (?, ?, ?, 'pending', ?)`,
			res.InviteID, requesterID, res.SenderID, s.Now())
		if err != nil {
			return err
		}
		_ = ins
		return s.Notifs.Create(ctx, conn, notifications.Params{
			UserID:  res.SenderID,
			Type:    notifications.TypeConnectionRequest,
			ActorID: requesterID,
		})
	})
}

// Request is a pending incoming request shown to the recipient.
type Request struct {
	ID                int64
	RequesterUsername string
	RequesterName     string
	RequesterPhotoKey string
	CreatedAt         time.Time
}

// ListPendingRequests returns the recipient's pending incoming requests.
func (s *Service) ListPendingRequests(ctx context.Context, recipientID int64) ([]Request, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT cr.id, u.username, u.first_name, u.last_name, COALESCE(u.photo_key, ''), cr.created_at
		  FROM connection_requests cr
		  JOIN users u ON u.id = cr.requester_id AND u.deleted_at IS NULL
		 WHERE cr.recipient_id = ? AND cr.status = 'pending'
		 ORDER BY cr.created_at DESC`, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reqs []Request
	for rows.Next() {
		var (
			req         Request
			first, last string
		)
		if err := rows.Scan(&req.ID, &req.RequesterUsername, &first, &last, &req.RequesterPhotoKey, &req.CreatedAt); err != nil {
			return nil, err
		}
		req.RequesterName = fullName(first, last)
		reqs = append(reqs, req)
	}
	return reqs, rows.Err()
}

// PendingRequestFrom looks up the id of a pending incoming connection request
// where requesterID asked to connect with recipientID. It returns ok=false when
// no such pending request exists.
//
// This powers the profile preview (issue #3): a viewer may peek at a profile
// only when that profile's owner has an outstanding request to them, and the
// preview needs the request id to wire its Accept/Decline forms to the existing
// /requests/{id}/accept and /requests/{id}/deny endpoints. The lookup is
// deliberately narrow — only status='pending', and only when the requester is
// not soft-deleted — so it can never widen into a general profile-view bypass
// (CLAUDE.md §1). It mirrors the filters used by ListPendingRequests.
func (s *Service) PendingRequestFrom(ctx context.Context, recipientID, requesterID int64) (requestID int64, ok bool, err error) {
	err = s.DB.QueryRowContext(ctx, `
		SELECT cr.id
		  FROM connection_requests cr
		  JOIN users u ON u.id = cr.requester_id AND u.deleted_at IS NULL
		 WHERE cr.recipient_id = ? AND cr.requester_id = ? AND cr.status = 'pending'
		 LIMIT 1`, recipientID, requesterID,
	).Scan(&requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return requestID, true, nil
}

// AcceptRequest accepts a pending request, creating the canonical connection
// row and notifying the original requester. Enforces the 10-accepts-per-7-days
// limit inside the immediate transaction.
func (s *Service) AcceptRequest(ctx context.Context, requestID, accepterID int64) error {
	windowStart := s.Now().Add(-ConnectWindow)
	return db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		var (
			requesterID, recipientID int64
			status                   string
		)
		err := conn.QueryRowContext(ctx,
			`SELECT requester_id, recipient_id, status FROM connection_requests WHERE id = ?`, requestID,
		).Scan(&requesterID, &recipientID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRequestNotFound
		}
		if err != nil {
			return err
		}
		if recipientID != accepterID {
			return ErrNotRecipient
		}
		if status != "pending" {
			return ErrRequestResolved
		}

		var count int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM connections WHERE (user_a_id = ? OR user_b_id = ?) AND created_at > ?`,
			accepterID, accepterID, windowStart).Scan(&count); err != nil {
			return err
		}
		if count >= ConnectLimit {
			var oldest time.Time
			if err := conn.QueryRowContext(ctx,
				`SELECT created_at FROM connections WHERE (user_a_id = ? OR user_b_id = ?) AND created_at > ? ORDER BY created_at ASC LIMIT 1`,
				accepterID, accepterID, windowStart).Scan(&oldest); err != nil {
				return err
			}
			return &RateLimitError{Kind: "connection", LiftAt: oldest.Add(ConnectWindow)}
		}

		a, b := order(requesterID, recipientID)
		if _, err := conn.ExecContext(ctx,
			`INSERT OR IGNORE INTO connections (user_a_id, user_b_id, created_at) VALUES (?, ?, ?)`,
			a, b, s.Now()); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx,
			`UPDATE connection_requests SET status = 'accepted', resolved_at = ? WHERE id = ?`,
			s.Now(), requestID); err != nil {
			return err
		}
		return s.Notifs.Create(ctx, conn, notifications.Params{
			UserID:  requesterID,
			Type:    notifications.TypeConnectionAccepted,
			ActorID: accepterID,
		})
	})
}

// DenyRequest marks a pending request denied. The original requester is not
// notified (CLAUDE.md / §1.3: deny is silent).
func (s *Service) DenyRequest(ctx context.Context, requestID, denierID int64) error {
	var (
		recipientID int64
		status      string
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT recipient_id, status FROM connection_requests WHERE id = ?`, requestID,
	).Scan(&recipientID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRequestNotFound
	}
	if err != nil {
		return err
	}
	if recipientID != denierID {
		return ErrNotRecipient
	}
	if status != "pending" {
		return ErrRequestResolved
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE connection_requests SET status = 'denied', resolved_at = ? WHERE id = ? AND status = 'pending'`,
		s.Now(), requestID)
	return err
}

// Disconnect removes the connection between two users. No notification is sent
// (§1.4). Deletes defensively in both orderings.
func (s *Service) Disconnect(ctx context.Context, userID, otherID int64) error {
	a, b := order(userID, otherID)
	_, err := s.DB.ExecContext(ctx,
		`DELETE FROM connections WHERE user_a_id = ? AND user_b_id = ?`, a, b)
	return err
}

// order returns the two IDs as (lower, higher) for canonical storage.
func order(x, y int64) (int64, int64) {
	if x < y {
		return x, y
	}
	return y, x
}

func fullName(first, last string) string {
	if last == "" {
		return first
	}
	if first == "" {
		return last
	}
	return first + " " + last
}
