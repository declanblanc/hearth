package auth

import (
	"net/http"

	"github.com/dblanc/hearth/internal/shared/middleware"
)

// SessionLoader is HTTP middleware that resolves the session cookie to a User
// and attaches it to the request context. Missing/invalid cookies are silently
// ignored — downstream RequireAuth decides whether to redirect.
func SessionLoader(svc *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ReadSessionCookie(r)
			if token != "" {
				if su, err := svc.LoadSession(r.Context(), token); err == nil && su != nil {
					ctx := middleware.WithUser(r.Context(), &middleware.User{
						ID: su.ID, Username: su.Username, DisplayName: su.DisplayName, Verified: su.Verified,
					})
					r = r.WithContext(ctx)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
