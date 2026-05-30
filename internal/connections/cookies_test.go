package connections

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var testSecret = []byte("a-test-secret-that-is-at-least-32-bytes")

func TestPendingInvite_RoundTrip(t *testing.T) {
	now := time.Now()
	raw := SignPendingInvite(testSecret, "tok123", now)
	got, ok := ReadPendingInvite(testSecret, raw, now)
	if !ok {
		t.Fatal("freshly signed cookie should read back ok")
	}
	if got != "tok123" {
		t.Errorf("token: got %q, want tok123", got)
	}
}

func TestPendingInvite_Expired(t *testing.T) {
	signed := time.Now()
	raw := SignPendingInvite(testSecret, "tok", signed)
	// Read 31 minutes later — past the 30-minute TTL.
	later := signed.Add(PendingInviteTTL + time.Minute)
	if _, ok := ReadPendingInvite(testSecret, raw, later); ok {
		t.Error("a stale cookie should be rejected")
	}
}

func TestPendingInvite_BadHMAC(t *testing.T) {
	now := time.Now()
	raw := SignPendingInvite(testSecret, "tok", now)

	// Tampered signature.
	if _, ok := ReadPendingInvite(testSecret, raw+"x", now); ok {
		t.Error("tampered cookie should be rejected")
	}
	// Wrong secret.
	if _, ok := ReadPendingInvite([]byte("a-different-secret-also-32-bytes-long!"), raw, now); ok {
		t.Error("cookie signed with another secret should be rejected")
	}
	// Garbage shape.
	if _, ok := ReadPendingInvite(testSecret, "not-a-valid-cookie", now); ok {
		t.Error("malformed cookie should be rejected")
	}
}

func TestPendingInviteRedirect(t *testing.T) {
	now := time.Now()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.AddCookie(&http.Cookie{Name: PendingInviteCookie, Value: SignPendingInvite(testSecret, "abc", now)})

	if got := PendingInviteRedirect(r, testSecret, now); got != "/i/abc" {
		t.Errorf("redirect: got %q, want /i/abc", got)
	}

	// No cookie → no redirect.
	bare := httptest.NewRequest(http.MethodGet, "/login", nil)
	if got := PendingInviteRedirect(bare, testSecret, now); got != "" {
		t.Errorf("no cookie should yield empty redirect, got %q", got)
	}
}
