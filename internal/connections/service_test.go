package connections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if invites[0].Exhausted || invites[0].Expired {
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

	// Exhausted invite: accept_count has reached max_uses, so the link is spent
	// even though it has not expired (issue #28).
	exhaustedTok := randToken(t)
	if _, err := d.Exec(
		`INSERT INTO invites (sender_id, token, created_at, expires_at, accept_count, max_uses) VALUES (?, ?, ?, ?, ?, ?)`,
		sender, exhaustedTok, time.Now(), time.Now().Add(InviteLifetime), InviteMaxUses, InviteMaxUses); err != nil {
		t.Fatal(err)
	}
	if r, _ := svc.Resolve(ctx, exhaustedTok, 0); r.State != StateInvalid {
		t.Errorf("exhausted invite should be invalid, got %d", r.State)
	}
}

// ---- accepting an invite ----

// TestAcceptInvite_ConnectsAndNotifiesSender proves the whole handshake happens
// in one step: accepting an invite creates the canonical connection and notifies
// the *sender* (the inviter) that their invitation was accepted, with the
// accepter as the actor.
func TestAcceptInvite_ConnectsAndNotifiesSender(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	// Make the accepter the LOWER id so we can prove canonical ordering.
	accepter := seedUser(t, d, "accepter") // lower id
	sender := seedUser(t, d, "sender")     // higher id
	token, _ := svc.CreateInvite(ctx, sender)

	res, err := svc.AcceptInvite(ctx, token, accepter)
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if res.SenderUsername != "sender" {
		t.Errorf("resolution should carry the sender identity, got %+v", res)
	}

	// They are now connected (no pending state).
	if ok, _ := svc.IsConnected(ctx, accepter, sender); !ok {
		t.Error("accepter and sender should be connected immediately")
	}

	// Connection stored as (min, max).
	var a, b int64
	if err := d.QueryRow(`SELECT user_a_id, user_b_id FROM connections`).Scan(&a, &b); err != nil {
		t.Fatalf("read connection: %v", err)
	}
	if a != min64(sender, accepter) || b != max64(sender, accepter) {
		t.Errorf("connection not canonical: got (%d,%d)", a, b)
	}

	// The SENDER (inviter) is notified that the invite was accepted; the
	// accepter is not notified.
	if n := unreadCount(t, d, sender, notifications.TypeConnectionAccepted); n != 1 {
		t.Errorf("want 1 connection_accepted for sender, got %d", n)
	}
	if n := unreadCount(t, d, accepter, notifications.TypeConnectionAccepted); n != 0 {
		t.Errorf("want 0 connection_accepted for accepter, got %d", n)
	}

	// The notification's actor is the accepter, so its profile link points at
	// the new connection (letting the sender disconnect if unintended).
	var actorID int64
	if err := d.QueryRow(
		`SELECT actor_id FROM notifications WHERE user_id = ? AND type = ?`,
		sender, notifications.TypeConnectionAccepted).Scan(&actorID); err != nil {
		t.Fatalf("read notification actor: %v", err)
	}
	if actorID != accepter {
		t.Errorf("notification actor: got %d, want accepter %d", actorID, accepter)
	}
}

// TestAcceptInvite_Idempotent proves a second accept of the same link by the
// same person is a harmless no-op: it surfaces ErrAlreadyConnected, claims no
// extra slot, and creates no duplicate connection or notification.
func TestAcceptInvite_Idempotent(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	accepter := seedUser(t, d, "accepter")
	token, _ := svc.CreateInvite(ctx, sender)

	if _, err := svc.AcceptInvite(ctx, token, accepter); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if _, err := svc.AcceptInvite(ctx, token, accepter); !errors.Is(err, ErrAlreadyConnected) {
		t.Errorf("second accept should report ErrAlreadyConnected, got %v", err)
	}

	var conns int
	_ = d.QueryRow(`SELECT COUNT(*) FROM connections`).Scan(&conns)
	if conns != 1 {
		t.Errorf("want exactly 1 connection, got %d", conns)
	}
	if n := unreadCount(t, d, sender, notifications.TypeConnectionAccepted); n != 1 {
		t.Errorf("want exactly 1 notification, got %d", n)
	}
	// Only one of the link's uses was claimed.
	var acceptCount int
	_ = d.QueryRow(`SELECT accept_count FROM invites WHERE token = ?`, token).Scan(&acceptCount)
	if acceptCount != 1 {
		t.Errorf("want accept_count 1, got %d", acceptCount)
	}
}

func TestAcceptInvite_Self(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	token, _ := svc.CreateInvite(ctx, sender)

	if _, err := svc.AcceptInvite(ctx, token, sender); !errors.Is(err, ErrSelfConnect) {
		t.Errorf("accepting your own invite should fail with ErrSelfConnect, got %v", err)
	}
}

// TestAcceptInvite_MultiUseUpToCap proves a single invite link can be accepted
// by InviteMaxUses distinct people, and the (cap+1)th is rejected as exhausted
// (issue #28). The cap enforced here is the real backend cap (10), independent
// of the lower figure shown in the UI (InviteShownUses).
func TestAcceptInvite_MultiUseUpToCap(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	token, _ := svc.CreateInvite(ctx, sender)

	// InviteMaxUses distinct accepters can each connect via the same link.
	for i := 0; i < InviteMaxUses; i++ {
		accepter := seedUser(t, d, fmt.Sprintf("accepter%d", i))
		if _, err := svc.AcceptInvite(ctx, token, accepter); err != nil {
			t.Fatalf("accept %d/%d should succeed: %v", i+1, InviteMaxUses, err)
		}
	}

	// All slots are now claimed; the next accept must be rejected.
	overflow := seedUser(t, d, "overflow")
	if _, err := svc.AcceptInvite(ctx, token, overflow); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("accept %d (past the cap) should fail with ErrInviteInvalid, got %v", InviteMaxUses+1, err)
	}

	// The exhausted link now resolves as invalid for a fresh viewer too.
	if r, _ := svc.Resolve(ctx, token, 0); r.State != StateInvalid {
		t.Errorf("exhausted link should resolve StateInvalid, got %d", r.State)
	}

	// Exactly InviteMaxUses connections were created — no more.
	var conns int
	_ = d.QueryRow(`SELECT COUNT(*) FROM connections WHERE user_a_id = ? OR user_b_id = ?`, sender, sender).Scan(&conns)
	if conns != InviteMaxUses {
		t.Errorf("want %d connections, got %d", InviteMaxUses, conns)
	}
}

// TestAcceptInvite_ExpiredLinkRejected proves the 72-hour expiry still rejects
// accepts independently of the multi-use capacity (issue #28).
func TestAcceptInvite_ExpiredLinkRejected(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	sender := seedUser(t, d, "sender")
	accepter := seedUser(t, d, "accepter")

	// A fresh (unused) link whose expiry is already in the past.
	past := time.Now().Add(-time.Hour)
	expiredTok := randToken(t)
	if _, err := d.Exec(
		`INSERT INTO invites (sender_id, token, created_at, expires_at, accept_count, max_uses) VALUES (?, ?, ?, ?, 0, ?)`,
		sender, expiredTok, past.Add(-InviteLifetime), past, InviteMaxUses); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AcceptInvite(ctx, expiredTok, accepter); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("accept on an expired link should fail, got %v", err)
	}
}

// TestAcceptInvite_NotRateLimited proves accepting invites is not rate-limited:
// a user already holding many recent connections can still accept another invite
// and connect (the old 10-connections-per-7-days cap was removed).
func TestAcceptInvite_NotRateLimited(t *testing.T) {
	d := newTestDB(t)
	svc := newSvc(d)
	ctx := context.Background()
	accepter := seedUser(t, d, "accepter")

	// Seed well past the old weekly cap of recent connections.
	for i := 0; i < 15; i++ {
		other := seedUser(t, d, fmt.Sprintf("other%d", i))
		if _, err := d.Exec(`INSERT INTO connections (user_a_id, user_b_id, created_at) VALUES (?, ?, ?)`,
			min64(accepter, other), max64(accepter, other), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	sender := seedUser(t, d, "sender")
	token, _ := svc.CreateInvite(ctx, sender)

	if _, err := svc.AcceptInvite(ctx, token, accepter); err != nil {
		t.Fatalf("accept should succeed without a rate limit, got %v", err)
	}
	if ok, _ := svc.IsConnected(ctx, accepter, sender); !ok {
		t.Error("accepter and sender should be connected")
	}
	if n := unreadCount(t, d, sender, notifications.TypeConnectionAccepted); n != 1 {
		t.Errorf("want 1 connection_accepted for sender, got %d", n)
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
