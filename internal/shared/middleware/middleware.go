// Package middleware contains HTTP middleware: request IDs, structured JSON
// logging, panic recovery, and the session-loading authenticator.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyUser
)

// User is the minimal user identity attached to the request context by the
// session loader. Detailed fields are loaded by handlers that need them.
type User struct {
	ID          int64
	Username    string
	DisplayName string
	Verified    bool
	// UnreadDot drives the header notification dot. Populated per-request by
	// notifications.LoadUnread middleware (not by the session loader).
	UnreadDot bool
}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxKeyUser, u)
}

func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKeyUser).(*User)
	return u
}

func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// RequestID assigns a short random ID to every request and exposes it on the
// response so logs and client reports can be correlated.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Logger emits one structured JSON line per request. Errors (4xx/5xx) are
// logged at warn/error so they're easy to filter.
func Logger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &statusWriter{ResponseWriter: w, status: 200}
			next.ServeHTTP(rw, r)
			attrs := []any{
				"req_id", RequestIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"dur_ms", time.Since(start).Milliseconds(),
				"ip", ClientIP(r),
			}
			if u := UserFrom(r.Context()); u != nil {
				attrs = append(attrs, "user_id", u.ID)
			}
			switch {
			case rw.status >= 500:
				logger.Error("http", attrs...)
			case rw.status >= 400:
				logger.Warn("http", attrs...)
			default:
				logger.Info("http", attrs...)
			}
		})
	}
}

// Recoverer turns panics into 500s without leaking stack traces.
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic", "req_id", RequestIDFrom(r.Context()), "value", rec)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth redirects unauthenticated requests to /login?next=<path>. Place
// behind the session loader so UserFrom is populated.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()) == nil {
			http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MaxBodyBytes caps the size of request bodies for methods that carry one
// (everything except GET/HEAD). Bodies over the limit fail when a handler reads
// them, so multipart parsing and form decoding error out instead of buffering
// unbounded data to memory or disk. The limit must sit comfortably above the
// 8 MiB per-image cap so legitimate uploads still succeed.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SameOrigin rejects state-changing requests (anything but GET/HEAD/OPTIONS)
// that don't originate from the site itself. It's CSRF defence-in-depth on top
// of the SameSite=Lax session cookie: a stateless Origin/Referer check with no
// per-form tokens to plumb through templates or htmx.
//
// trustedOrigin is the site's own base URL (cfg.BaseURL); its scheme+host is the
// only origin we accept for mutations. htmx sends the Origin header, so it's
// covered automatically.
func SameOrigin(trustedOrigin string) func(http.Handler) http.Handler {
	// Precompute the trusted scheme://host once at startup.
	want := originOf(trustedOrigin)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			if !originAllowed(r, want) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isSafeMethod reports whether the method is read-only and thus exempt from the
// origin check. These methods must never mutate state.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// originAllowed checks that a state-changing request came from the trusted
// origin. It prefers the Origin header; when absent (some legitimate requests
// omit it) it falls back to the Referer's scheme+host. A request with neither a
// matching Origin nor a matching Referer is rejected — for a browser-only htmx
// app that's the safe default, since real browsers always send at least one on
// same-origin form posts and fetches.
func originAllowed(r *http.Request, want string) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return originOf(origin) == want
	}
	if referer := r.Header.Get("Referer"); referer != "" {
		return originOf(referer) == want
	}
	return false
}

// originOf reduces a URL to its "scheme://host" form for comparison, dropping
// any path, query, or fragment. Returns "" if the input can't be parsed or
// lacks a scheme/host.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// ClientIP returns the best-effort client IP for use as a rate-limit key.
//
// Only Fly-Client-IP is trusted. Fly.io is the sole supported reverse proxy,
// and its proxy sets Fly-Client-IP to the real client address, overwriting any
// value a client tries to supply. We deliberately do NOT read X-Forwarded-For:
// that header is client-appendable, so trusting it would let an attacker rotate
// spoofed IPs to evade the failed-login/password-reset throttle or pin a
// victim's IP in the limiter. When Fly-Client-IP is absent (local dev or any
// non-Fly path) we fall back to RemoteAddr, which cannot be spoofed.
func ClientIP(r *http.Request) string {
	if ip := r.Header.Get("Fly-Client-IP"); ip != "" {
		return ip
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusWriter) WriteHeader(c int) {
	if s.wrote {
		return
	}
	s.status = c
	s.wrote = true
	s.ResponseWriter.WriteHeader(c)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.wrote {
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}
