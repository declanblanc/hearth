package connections

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/dblanc/hearth/internal/notifications"
	"github.com/dblanc/hearth/internal/shared/db"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return d
}

func seedUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO users (username, email, password_hash, first_name, last_name)
		 VALUES (?, ?, 'x', ?, 'User')`,
		username, username+"@example.com", username)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newSvc(d *sql.DB) *Service {
	return New(d, notifications.New(d), "https://hearth.test")
}

// unreadCount is a test-only helper; the product never surfaces counts.
func unreadCount(t *testing.T, d *sql.DB, userID int64, notifType string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = ?`, userID, notifType,
	).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

// ---- IsConnected / ConnectionIDs ----

func TestIsConnected(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	a := seedUser(t, d, "a")
	b := seedUser(t, d, "b")
	c := seedUser(t, d, "c")
	if _, err := d.Exec(`INSERT INTO connections (user_a_id, user_b_id) VALUES (?, ?)`, min64(a, b), max64(a, b)); err != nil {
		t.Fatal(err)
	}

	if ok, _ := svc.IsConnected(ctx, a, b); !ok {
		t.Error("a and b should be connected")
	}
	if ok, _ := svc.IsConnected(ctx, b, a); !ok {
		t.Error("connection is symmetric")
	}
	if ok, _ := svc.IsConnected(ctx, a, c); ok {
		t.Error("a and c are not connected")
	}
	if ok, _ := svc.IsConnected(ctx, a, a); ok {
		t.Error("a user is never connected to themselves")
	}
}

// ---- invites & rate limit ----

func TestCreateInvite_AndList(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")

	token, err := svc.CreateInvite(ctx, sender)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if len(token) < 40 {
		t.Errorf("token looks too short to be 32 bytes base64: %q", token)
	}
	invites, err := svc.ListInvites(ctx, sender)
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 1 || invites[0].Token != token {
		t.Fatalf("expected the created invite in the list")
	}
	if invites[0].Consumed || invites[0].Expired {
		t.Error("a fresh invite should be active")
	}
}

func TestCreateInvite_RateLimitAt20(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")

	for i := 0; i < InviteLimit; i++ {
		if _, err := svc.CreateInvite(ctx, sender); err != nil {
			t.Fatalf("invite %d should succeed: %v", i+1, err)
		}
	}
	_, err := svc.CreateInvite(ctx, sender)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("21st invite should hit the rate limit, got %v", err)
	}
	if rl.Kind != "invite" {
		t.Errorf("rate limit kind: got %q", rl.Kind)
	}
	if !rl.LiftAt.After(time.Now()) {
		t.Errorf("lift time should be in the future, got %v", rl.LiftAt)
	}
}

func TestCreateInvite_RollingWindow(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")

	// 20 invites from just over 7 days ago — these have rolled out of the window.
	old := time.Now().Add(-InviteWindow - time.Second)
	for i := 0; i < InviteLimit; i++ {
		if _, err := d.Exec(
			`INSERT INTO invites (sender_id, token, created_at, expires_at) VALUES (?, ?, ?, ?)`,
			sender, randToken(t), old, old.Add(InviteLifetime)); err != nil {
			t.Fatal(err)
		}
	}
	// One more today must succeed because the old ones no longer count.
	if _, err := svc.CreateInvite(ctx, sender); err != nil {
		t.Fatalf("invite within window should succeed after old ones expired: %v", err)
	}
}

// ---- resolve state machine ----

func TestResolve_StateMachine(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	viewer := seedUser(t, d, "viewer")

	token, _ := svc.CreateInvite(ctx, sender)

	// Valid, anonymous.
	if r, _ := svc.Resolve(ctx, token, 0); r.State != StateValid {
		t.Errorf("anon valid: got state %d", r.State)
	}
	// Valid, signed in as a stranger.
	if r, _ := svc.Resolve(ctx, token, viewer); r.State != StateValid {
		t.Errorf("signed-in valid: got state %d", r.State)
	}
	// Self.
	if r, _ := svc.Resolve(ctx, token, sender); r.State != StateSelf {
		t.Errorf("self: got state %d", r.State)
	}
	// Not found.
	if r, _ := svc.Resolve(ctx, "no-such-token", viewer); r.State != StateInvalid {
		t.Errorf("not found: got state %d", r.State)
	}

	// Already connected.
	if _, err := d.Exec(`INSERT INTO connections (user_a_id, user_b_id) VALUES (?, ?)`,
		min64(sender, viewer), max64(sender, viewer)); err != nil {
		t.Fatal(err)
	}
	if r, _ := svc.Resolve(ctx, token, viewer); r.State != StateAlreadyConnected {
		t.Errorf("already connected: got state %d", r.State)
	}
}

func TestResolve_ExpiredAndConsumed(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")

	// Expired invite.
	past := time.Now().Add(-time.Hour)
	expiredTok := randToken(t)
	if _, err := d.Exec(
		`INSERT INTO invites (sender_id, token, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		sender, expiredTok, past.Add(-InviteLifetime), past); err != nil {
		t.Fatal(err)
	}
	if r, _ := svc.Resolve(ctx, expiredTok, 0); r.State != StateInvalid {
		t.Errorf("expired invite should be invalid, got %d", r.State)
	}

	// Consumed invite.
	consumedTok := randToken(t)
	if _, err := d.Exec(
		`INSERT INTO invites (sender_id, token, created_at, expires_at, consumed_at) VALUES (?, ?, ?, ?, ?)`,
		sender, consumedTok, time.Now(), time.Now().Add(InviteLifetime), time.Now()); err != nil {
		t.Fatal(err)
	}
	if r, _ := svc.Resolve(ctx, consumedTok, 0); r.State != StateInvalid {
		t.Errorf("consumed invite should be invalid, got %d", r.State)
	}
}

// ---- request / accept / deny ----

func TestCreateRequest_ConsumesInviteAndNotifies(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	requester := seedUser(t, d, "requester")
	token, _ := svc.CreateInvite(ctx, sender)

	if err := svc.CreateRequest(ctx, token, requester); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	// Invite is now consumed — a second request fails.
	if err := svc.CreateRequest(ctx, token, requester); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("second request on a consumed invite should fail, got %v", err)
	}

	// Recipient (sender) got a connection_request notification.
	if n := unreadCount(t, d, sender, notifications.TypeConnectionRequest); n != 1 {
		t.Errorf("want 1 connection_request notification for sender, got %d", n)
	}

	reqs, _ := svc.ListPendingRequests(ctx, sender)
	if len(reqs) != 1 || reqs[0].RequesterUsername != "requester" {
		t.Errorf("expected one pending request from requester, got %+v", reqs)
	}
}

func TestAccept_CreatesCanonicalConnectionAndNotifies(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	// Make sender the HIGHER id so we can prove canonical ordering on accept.
	requester := seedUser(t, d, "requester") // lower id
	sender := seedUser(t, d, "sender")       // higher id
	token, _ := svc.CreateInvite(ctx, sender)
	if err := svc.CreateRequest(ctx, token, requester); err != nil {
		t.Fatal(err)
	}
	reqs, _ := svc.ListPendingRequests(ctx, sender)
	reqID := reqs[0].ID

	if err := svc.AcceptRequest(ctx, reqID, sender); err != nil {
		t.Fatalf("AcceptRequest: %v", err)
	}

	// Connection stored as (min, max).
	var a, b int64
	if err := d.QueryRow(`SELECT user_a_id, user_b_id FROM connections`).Scan(&a, &b); err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if a != min64(sender, requester) || b != max64(sender, requester) {
		t.Errorf("connection not canonical: got (%d,%d)", a, b)
	}

	// Original requester got a connection_accepted notification.
	if n := unreadCount(t, d, requester, notifications.TypeConnectionAccepted); n != 1 {
		t.Errorf("want 1 connection_accepted for requester, got %d", n)
	}

	// Accepting again fails — already resolved.
	if err := svc.AcceptRequest(ctx, reqID, sender); !errors.Is(err, ErrRequestResolved) {
		t.Errorf("re-accept should fail, got %v", err)
	}
}

func TestAccept_NonRecipientRejected(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	requester := seedUser(t, d, "requester")
	intruder := seedUser(t, d, "intruder")
	token, _ := svc.CreateInvite(ctx, sender)
	_ = svc.CreateRequest(ctx, token, requester)
	reqs, _ := svc.ListPendingRequests(ctx, sender)

	if err := svc.AcceptRequest(ctx, reqs[0].ID, intruder); !errors.Is(err, ErrNotRecipient) {
		t.Errorf("a non-recipient must not accept, got %v", err)
	}
}

func TestAccept_RateLimit(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	accepter := seedUser(t, d, "accepter")

	// Seed 10 recent connections so the accepter is at the weekly limit.
	for i := 0; i < ConnectLimit; i++ {
		other := seedUser(t, d, "other"+string(rune('a'+i)))
		if _, err := d.Exec(`INSERT INTO connections (user_a_id, user_b_id, created_at) VALUES (?, ?, ?)`,
			min64(accepter, other), max64(accepter, other), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	requester := seedUser(t, d, "requester")
	token, _ := svc.CreateInvite(ctx, accepter)
	_ = svc.CreateRequest(ctx, token, requester)
	reqs, _ := svc.ListPendingRequests(ctx, accepter)

	err := svc.AcceptRequest(ctx, reqs[0].ID, accepter)
	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("accept at limit should be rate-limited, got %v", err)
	}
	if rl.Kind != "connection" {
		t.Errorf("rate limit kind: got %q", rl.Kind)
	}
}

func TestDeny_SilentNoConnectionNoNotification(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	requester := seedUser(t, d, "requester")
	token, _ := svc.CreateInvite(ctx, sender)
	_ = svc.CreateRequest(ctx, token, requester)
	reqs, _ := svc.ListPendingRequests(ctx, sender)

	if err := svc.DenyRequest(ctx, reqs[0].ID, sender); err != nil {
		t.Fatalf("DenyRequest: %v", err)
	}

	var conns int
	_ = d.QueryRow(`SELECT COUNT(*) FROM connections`).Scan(&conns)
	if conns != 0 {
		t.Error("deny must not create a connection")
	}
	if n := unreadCount(t, d, requester, notifications.TypeConnectionAccepted); n != 0 {
		t.Error("deny must not notify the requester")
	}
	// No longer pending.
	after, _ := svc.ListPendingRequests(ctx, sender)
	if len(after) != 0 {
		t.Error("denied request should no longer be pending")
	}
}

func TestPendingRequestFrom(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	recipient := seedUser(t, d, "recipient")
	requester := seedUser(t, d, "requester")
	stranger := seedUser(t, d, "stranger")
	token, _ := svc.CreateInvite(ctx, recipient)
	if err := svc.CreateRequest(ctx, token, requester); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	reqs, _ := svc.ListPendingRequests(ctx, recipient)
	wantID := reqs[0].ID

	// The matching (recipient, requester) pair resolves to the request id.
	gotID, ok, err := svc.PendingRequestFrom(ctx, recipient, requester)
	if err != nil {
		t.Fatalf("PendingRequestFrom: %v", err)
	}
	if !ok || gotID != wantID {
		t.Errorf("want request id %d (ok), got id=%d ok=%v", wantID, gotID, ok)
	}

	// Reversed direction does not match: the request is one-directional.
	if _, ok, _ := svc.PendingRequestFrom(ctx, requester, recipient); ok {
		t.Error("reversed (requester as recipient) should not match")
	}

	// An unrelated viewer has no pending request.
	if _, ok, _ := svc.PendingRequestFrom(ctx, recipient, stranger); ok {
		t.Error("stranger should not match")
	}

	// Once the request is resolved (accepted), it is no longer 'pending' and
	// must stop matching — the preview access ends when the request does.
	if err := svc.AcceptRequest(ctx, wantID, recipient); err != nil {
		t.Fatalf("AcceptRequest: %v", err)
	}
	if _, ok, _ := svc.PendingRequestFrom(ctx, recipient, requester); ok {
		t.Error("accepted request should no longer be pending")
	}
}

func TestPendingRequestFrom_IgnoresSoftDeletedRequester(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	recipient := seedUser(t, d, "recipient")
	requester := seedUser(t, d, "requester")
	token, _ := svc.CreateInvite(ctx, recipient)
	if err := svc.CreateRequest(ctx, token, requester); err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}

	// Soft-delete the requester; their pending request must stop matching so a
	// deleted account can't keep granting a profile preview.
	if _, err := d.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, time.Now(), requester); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := svc.PendingRequestFrom(ctx, recipient, requester); ok {
		t.Error("soft-deleted requester should not match")
	}
}

func TestDisconnect_RemovesRowBothWays(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	a := seedUser(t, d, "a")
	b := seedUser(t, d, "b")
	if _, err := d.Exec(`INSERT INTO connections (user_a_id, user_b_id) VALUES (?, ?)`,
		min64(a, b), max64(a, b)); err != nil {
		t.Fatal(err)
	}

	// Disconnect initiated by b (the higher/lower either way) still works.
	if err := svc.Disconnect(ctx, b, a); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if ok, _ := svc.IsConnected(ctx, a, b); ok {
		t.Error("users should not be connected after disconnect")
	}
}

// ---- small test helpers ----

func randToken(t *testing.T) string {
	t.Helper()
	tok, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
