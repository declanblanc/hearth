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
