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

	// InviteMaxUses is the real, enforced number of times a single invite link
	// can be accepted before it is exhausted (issue #28). Expiry (InviteLifetime)
	// is independent and unchanged.
	//
	// DELIBERATE DISCREPANCY: the product UI tells users a link is good for 5
	// people (see InviteShownUses and the invite templates), but the backend
	// allows 10. This 5-shown / 10-enforced gap is intentional per issue #28 —
	// do not "fix" it to make the two numbers match.
	InviteMaxUses = 10
	// InviteShownUses is the capacity figure shown to users in the UI. It is
	// deliberately lower than InviteMaxUses (see above). It is static copy, not a
	// live used/remaining counter — the product shows no counts (CLAUDE.md §2).
	InviteShownUses = 5
)

var (
	ErrInviteInvalid    = errors.New("connections: invite not found, expired, or exhausted")
	ErrSelfConnect      = errors.New("connections: cannot connect to yourself")
	ErrAlreadyConnected = errors.New("connections: already connected")
)

// RateLimitError reports that a per-window limit was hit and when it lifts.
// Only invite creation is rate-limited (20 / 7d); accepting an invite is not.
type RateLimitError struct {
	Kind   string // "invite"
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
	// Exhausted reports that the link has been accepted its full max_uses times
	// and can no longer be used. (Formerly "consumed" under the single-use model;
	// now an invite supports up to InviteMaxUses accepts — issue #28.)
	Exhausted bool
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
			`INSERT INTO invites (sender_id, token, created_at, expires_at, max_uses) VALUES (?, ?, ?, ?, ?)`,
			senderID, token, now, now.Add(InviteLifetime), InviteMaxUses)
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
		`SELECT token, created_at, expires_at, accept_count, max_uses
		   FROM invites WHERE sender_id = ? ORDER BY created_at DESC`, senderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	var invites []Invite
	for rows.Next() {
		var (
			inv                  Invite
			acceptCount, maxUses int
		)
		if err := rows.Scan(&inv.Token, &inv.CreatedAt, &inv.ExpiresAt, &acceptCount, &maxUses); err != nil {
			return nil, err
		}
		inv.Exhausted = acceptCount >= maxUses
		inv.Expired = !inv.Exhausted && now.After(inv.ExpiresAt)
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// ---- invite resolution (the /i/{token} state machine) ----

type InviteState int

const (
	// StateInvalid means not found, expired, exhausted (all uses claimed), or the
	// sender is gone.
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
		res                  Resolution
		expiresAt            time.Time
		acceptCount, maxUses int
		first, last          string
		photo                sql.NullString
		deletedAt            sql.NullTime
	)
	err := s.DB.QueryRowContext(ctx, `
		SELECT i.id, i.sender_id, i.expires_at, i.accept_count, i.max_uses,
		       u.username, u.first_name, u.last_name, u.photo_key, u.deleted_at
		  FROM invites i
		  JOIN users u ON u.id = i.sender_id
		 WHERE i.token = ?`, token,
	).Scan(&res.InviteID, &res.SenderID, &expiresAt, &acceptCount, &maxUses,
		&res.SenderUsername, &first, &last, &photo, &deletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Resolution{State: StateInvalid}, nil
	}
	if err != nil {
		return Resolution{}, err
	}
	// Invalid if the sender is gone, the link has expired, or it has been
	// accepted its full max_uses times (exhausted). Multi-use per issue #28.
	if deletedAt.Valid || s.Now().After(expiresAt) || acceptCount >= maxUses {
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

// ---- accepting an invite ----

// AcceptInvite connects the accepter to the invite's sender immediately and
// notifies the sender that their invitation was accepted. This single action is
// the whole handshake — there is no separate confirm step. If the sender didn't
// mean to connect, the notification links to the new connection's profile, where
// they can disconnect.
//
// One of the invite link's uses is claimed here, not at link-open time
// (CLAUDE.md §5); a link can be accepted up to InviteMaxUses times before it is
// exhausted (issue #28). Accepting an invite is not rate-limited — only invite
// creation is (CLAUDE.md §7).
//
// It returns the resolved invite (sender identity) so the caller can render a
// "you're now connected with X" confirmation, even on the ErrAlreadyConnected /
// ErrSelfConnect paths. ErrInviteInvalid, ErrSelfConnect, and ErrAlreadyConnected
// map to the corresponding invite states.
func (s *Service) AcceptInvite(ctx context.Context, token string, accepterID int64) (Resolution, error) {
	res, err := s.Resolve(ctx, token, accepterID)
	if err != nil {
		return Resolution{}, err
	}
	switch res.State {
	case StateInvalid:
		return res, ErrInviteInvalid
	case StateSelf:
		return res, ErrSelfConnect
	case StateAlreadyConnected:
		return res, ErrAlreadyConnected
	}

	err = db.WithImmediate(ctx, s.DB, func(conn *sql.Conn) error {
		a, b := order(accepterID, res.SenderID)

		// Re-check inside the immediate transaction so two concurrent accepts of
		// the same link can't both create the connection and both burn a slot.
		// If the pair is already connected this accept is an idempotent no-op:
		// no extra slot claimed, no duplicate notification.
		var one int
		err := conn.QueryRowContext(ctx,
			`SELECT 1 FROM connections WHERE user_a_id = ? AND user_b_id = ?`, a, b).Scan(&one)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		// Claim one of the link's uses atomically. accept_count < max_uses gates
		// the multi-use cap inside BEGIN IMMEDIATE so concurrent accepts can never
		// push the link past its real cap of InviteMaxUses (CLAUDE.md §7). 0 rows
		// affected means the link is exhausted (or someone just took the last
		// slot).
		r, err := conn.ExecContext(ctx,
			`UPDATE invites SET accept_count = accept_count + 1 WHERE id = ? AND accept_count < max_uses`,
			res.InviteID)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return ErrInviteInvalid
		}

		now := s.Now()
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO connections (user_a_id, user_b_id, created_at) VALUES (?, ?, ?)`,
			a, b, now); err != nil {
			return err
		}
		// Audit row recording which invite produced this connection, born
		// already 'accepted' (the flow has no pending state). It is kept for
		// abuse review; no part of the app reads it back.
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO connection_requests (invite_id, requester_id, recipient_id, status, created_at, resolved_at)
			VALUES (?, ?, ?, 'accepted', ?, ?)`,
			res.InviteID, accepterID, res.SenderID, now, now); err != nil {
			return err
		}
		// Notify the sender that their invitation was accepted (CLAUDE.md §8).
		// The actor link in the notification is how the sender reaches the new
		// connection's profile to disconnect if they didn't mean to connect.
		return s.Notifs.Create(ctx, conn, notifications.Params{
			UserID:  res.SenderID,
			Type:    notifications.TypeConnectionAccepted,
			ActorID: accepterID,
		})
	})
	if err != nil {
		return res, err
	}
	return res, nil
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
