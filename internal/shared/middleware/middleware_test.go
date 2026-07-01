package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientIP verifies that only Fly-Client-IP is trusted and that the
// client-spoofable X-Forwarded-For header is ignored.
func TestClientIP(t *testing.T) {
	t.Run("falls back to RemoteAddr host without port", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "203.0.113.7:54321"

		if got := ClientIP(r); got != "203.0.113.7" {
			t.Fatalf("ClientIP = %q, want %q", got, "203.0.113.7")
		}
	})

	t.Run("trusts Fly-Client-IP", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "203.0.113.7:54321"
		r.Header.Set("Fly-Client-IP", "198.51.100.42")

		if got := ClientIP(r); got != "198.51.100.42" {
			t.Fatalf("ClientIP = %q, want %q", got, "198.51.100.42")
		}
	})

	t.Run("ignores spoofed X-Forwarded-For", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "203.0.113.7:54321"
		r.Header.Set("X-Forwarded-For", "10.0.0.1, 172.16.0.1")

		if got := ClientIP(r); got != "203.0.113.7" {
			t.Fatalf("ClientIP = %q, want %q (X-Forwarded-For must be ignored)", got, "203.0.113.7")
		}
	})
}

// readBodyHandler drains the request body and reports whether reading it failed
// (which is how http.MaxBytesReader signals an over-limit body).
func readBodyHandler(t *testing.T, readErr *error) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		*readErr = err
	})
}

func TestMaxBodyBytesRejectsOversizedBody(t *testing.T) {
	const limit = 1 << 10 // 1 KiB

	var readErr error
	handler := MaxBodyBytes(limit)(readBodyHandler(t, &readErr))

	body := strings.NewReader(strings.Repeat("x", limit+1))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("expected reading an over-limit body to fail, got nil error")
	}
}

func TestMaxBodyBytesAllowsBodyUnderLimit(t *testing.T) {
	const limit = 1 << 10 // 1 KiB

	var readErr error
	handler := MaxBodyBytes(limit)(readBodyHandler(t, &readErr))

	body := strings.NewReader(strings.Repeat("x", limit-1))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil {
		t.Fatalf("expected a body under the limit to read cleanly, got %v", readErr)
	}
}

// GET requests carry no body, so the cap must not wrap them.
func TestMaxBodyBytesSkipsGET(t *testing.T) {
	const limit = 1 << 10

	var readErr error
	handler := MaxBodyBytes(limit)(readBodyHandler(t, &readErr))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil {
		t.Fatalf("expected GET body read to succeed, got %v", readErr)
	}
}

const trustedOrigin = "https://hearth.test"

// newSameOriginHandler wraps a handler that records whether it was reached, so
// tests can distinguish a pass-through from a 403 rejection.
func newSameOriginHandler(reached *bool) http.Handler {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
	return SameOrigin(trustedOrigin)(next)
}

func TestSameOriginGetPassesRegardlessOfOrigin(t *testing.T) {
	var reached bool
	h := newSameOriginHandler(&reached)

	req := httptest.NewRequest(http.MethodGet, "/feed", nil)
	req.Header.Set("Origin", "https://evil.example")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if !reached {
		t.Fatal("GET should pass through even with a foreign Origin")
	}
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
}

func TestSameOriginPostMatchingOriginPasses(t *testing.T) {
	var reached bool
	h := newSameOriginHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	req.Header.Set("Origin", trustedOrigin)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if !reached {
		t.Fatal("POST with matching Origin should pass through")
	}
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
}

func TestSameOriginPostForeignOriginRejected(t *testing.T) {
	var reached bool
	h := newSameOriginHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	req.Header.Set("Origin", "https://evil.example")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if reached {
		t.Fatal("POST with a foreign Origin should be rejected")
	}
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rw.Code)
	}
}

func TestSameOriginPostRefererFallbackPasses(t *testing.T) {
	var reached bool
	h := newSameOriginHandler(&reached)

	// No Origin header; a matching Referer should be accepted.
	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	req.Header.Set("Referer", trustedOrigin+"/feed")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if !reached {
		t.Fatal("POST with matching Referer and no Origin should pass through")
	}
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rw.Code)
	}
}

func TestSameOriginPostNoHeadersRejected(t *testing.T) {
	var reached bool
	h := newSameOriginHandler(&reached)

	req := httptest.NewRequest(http.MethodPost, "/posts", nil)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)

	if reached {
		t.Fatal("POST with neither Origin nor Referer should be rejected")
	}
	if rw.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rw.Code)
	}
}
