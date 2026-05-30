package connections

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PendingInviteCookie carries an invite token across the signup / email-verify
// round-trip, which typically happens in a different browser session
// (CLAUDE.md §5). It is HMAC-signed and self-expiring after 30 minutes.
const (
	PendingInviteCookie = "pending_invite"
	PendingInviteTTL    = 30 * time.Minute
)

// randomToken returns a URL-safe random token of n raw bytes. Local to this
// package so connections doesn't depend on auth (auth depends on us for the
// cookie helpers below).
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SignPendingInvite builds the signed cookie value: "token|expiryUnix|mac".
// The MAC covers token and expiry so neither can be tampered with, and the
// embedded expiry is enforced on read regardless of the browser's own cookie
// lifetime.
func SignPendingInvite(secret []byte, token string, now time.Time) string {
	expiry := now.Add(PendingInviteTTL).Unix()
	payload := token + "|" + strconv.FormatInt(expiry, 10)
	mac := sign(secret, payload)
	return payload + "|" + mac
}

// ReadPendingInvite verifies a signed cookie value and returns the token if the
// signature is valid and the embedded expiry is still in the future. Any
// tampering or staleness yields ok=false (the cookie is then ignored).
func ReadPendingInvite(secret []byte, raw string, now time.Time) (token string, ok bool) {
	parts := strings.Split(raw, "|")
	if len(parts) != 3 {
		return "", false
	}
	token, expiryStr, gotMAC := parts[0], parts[1], parts[2]
	payload := token + "|" + expiryStr
	wantMAC := sign(secret, payload)
	if !hmac.Equal([]byte(gotMAC), []byte(wantMAC)) {
		return "", false
	}
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil || now.Unix() >= expiry {
		return "", false
	}
	return token, true
}

func sign(secret []byte, payload string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SetPendingInviteCookie writes the signed pending-invite cookie.
func SetPendingInviteCookie(w http.ResponseWriter, secret []byte, token string, now time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     PendingInviteCookie,
		Value:    SignPendingInvite(secret, token, now),
		Path:     "/",
		MaxAge:   int(PendingInviteTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearPendingInviteCookie expires the pending-invite cookie.
func ClearPendingInviteCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     PendingInviteCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// PendingInviteRedirect returns the path to send a freshly-authenticated user
// to if they have a valid pending-invite cookie, or "" if there is none.
// Reading also requires the request to carry the cookie.
func PendingInviteRedirect(r *http.Request, secret []byte, now time.Time) string {
	c, err := r.Cookie(PendingInviteCookie)
	if err != nil {
		return ""
	}
	token, ok := ReadPendingInvite(secret, c.Value, now)
	if !ok {
		return ""
	}
	return fmt.Sprintf("/i/%s", token)
}
