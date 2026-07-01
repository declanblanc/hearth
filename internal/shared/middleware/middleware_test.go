package middleware

import (
	"net/http/httptest"
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
