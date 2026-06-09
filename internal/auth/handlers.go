package auth

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

// Handlers wires the auth service to HTTP routes.
type Handlers struct {
	Svc      *Service
	Renderer *render.Renderer
	Secret   []byte      // HMAC key for reading the pending_invite cookie
	Secure   bool        // true in production — controls Secure cookie flag
	Media    media.Store // may be nil in dev/test — signup photo upload is then skipped
}

func NewHandlers(svc *Service, r *render.Renderer, secret []byte, secure bool, m media.Store) *Handlers {
	return &Handlers{Svc: svc, Renderer: r, Secret: secret, Secure: secure, Media: m}
}

// postAuthRedirect picks where to send a user immediately after they log in or
// verify their email. A valid pending_invite cookie wins (it carries an invite
// across the signup/verify round-trip, CLAUDE.md §5); otherwise we fall back to
// the provided default (e.g. the login `next` param or "/"). When an invite
// redirect is used, the cookie is cleared.
func (h *Handlers) postAuthRedirect(w http.ResponseWriter, r *http.Request, fallback string) string {
	if target := connections.PendingInviteRedirect(r, h.Secret, time.Now()); target != "" {
		connections.ClearPendingInviteCookie(w, h.Secure)
		return target
	}
	return fallback
}

// Mount registers all auth routes on the given mux.
func (h *Handlers) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /signup", h.signupForm)
	mux.HandleFunc("POST /signup", h.signupSubmit)
	mux.HandleFunc("GET /verify", h.verify)
	mux.HandleFunc("POST /verify/resend", h.resendVerification)

	mux.HandleFunc("GET /login", h.loginForm)
	mux.HandleFunc("POST /login", h.loginSubmit)
	mux.HandleFunc("POST /logout", h.logout)

	mux.HandleFunc("GET /password/forgot", h.forgotForm)
	mux.HandleFunc("POST /password/forgot", h.forgotSubmit)
	mux.HandleFunc("GET /password/reset", h.resetForm)
	mux.HandleFunc("POST /password/reset", h.resetSubmit)
}

// user returns the authenticated user from context, or nil. Used to populate
// the User key in every render.Page call so base.html nav always renders.
func user(r *http.Request) *middleware.User {
	return middleware.UserFrom(r.Context())
}

// ---- signup ----

func (h *Handlers) signupForm(w http.ResponseWriter, r *http.Request) {
	h.Renderer.HTML(w, "signup.html", render.Page(user(r), render.M{
		"Errors": FieldErrors{},
	}))
}

func (h *Handlers) signupSubmit(w http.ResponseWriter, r *http.Request) {
	// The form is multipart when an optional profile photo is attached, plain
	// URL-encoded otherwise; fall back to ParseForm so both work (mirrors the
	// profile-edit handler).
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			h.Renderer.Error(w, http.StatusBadRequest)
			return
		}
	}
	in := SignupInput{
		Username:  NormaliseUsername(r.FormValue("username")),
		Email:     NormaliseEmail(r.FormValue("email")),
		Password:  r.FormValue("password"),
		FirstName: strings.TrimSpace(r.FormValue("first_name")),
		LastName:  strings.TrimSpace(r.FormValue("last_name")),
	}
	errs := FieldErrors{}
	if m := ValidateUsername(in.Username); m != "" {
		errs.Add("username", m)
	}
	if m := ValidateEmail(in.Email); m != "" {
		errs.Add("email", m)
	}
	if m := ValidatePassword(in.Password); m != "" {
		errs.Add("password", m)
	}
	if m := ValidateFirstName(in.FirstName); m != "" {
		errs.Add("first_name", m)
	}
	if m := ValidateLastName(in.LastName); m != "" {
		errs.Add("last_name", m)
	}

	// A profile photo is optional at signup (issue #26). Validate and normalize it
	// up front so any problem surfaces inline with the other fields; the bytes are
	// only stored once the account is successfully created below.
	var (
		photoData []byte
		photoType string
		photoExt  string
	)
	if h.Media != nil {
		if file, _, ferr := r.FormFile("photo"); ferr == nil {
			defer file.Close()
			data, rerr := io.ReadAll(io.LimitReader(file, media.MaxProfilePhotoSize+1))
			if rerr != nil {
				h.Renderer.Error(w, http.StatusInternalServerError)
				return
			}
			normalized, ct, ext, userMsg, perr := media.ProcessProfilePhoto(data)
			if perr != nil {
				h.Renderer.Error(w, http.StatusInternalServerError)
				return
			}
			if userMsg != "" {
				errs.Add("photo", userMsg)
			} else {
				photoData, photoType, photoExt = normalized, ct, ext
			}
		}
		// A missing file is normal — the photo is optional.
	}

	if errs.Has() {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "signup.html", render.Page(user(r), render.M{
			"Username": in.Username, "Email": in.Email,
			"FirstName": in.FirstName, "LastName": in.LastName, "Errors": errs,
		}))
		return
	}

	userID, err := h.Svc.Signup(r.Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrUsernameTaken):
			errs.Add("username", "That username is taken.")
		case errors.Is(err, ErrEmailTaken):
			errs.Add("email", "An account already exists for that email.")
		case errors.Is(err, ErrSendVerification):
			// Account was cleaned up — user can retry.
			slog.Error("signup: send verification email", "email", in.Email, "err", err)
			errs.Add("email", "We couldn't send a verification email to that address. Please try again.")
		default:
			slog.Error("signup", "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
			return
		}
		if errs.Has() {
			h.Renderer.Status(w, http.StatusUnprocessableEntity, "signup.html", render.Page(user(r), render.M{
				"Username": in.Username, "Email": in.Email,
				"FirstName": in.FirstName, "LastName": in.LastName, "Errors": errs,
			}))
			return
		}
	}

	// Store the optional photo now that the account exists. This is best-effort:
	// the account is already created and the verification email sent, so a failed
	// upload should not block signup — the user can add a photo later from
	// settings. We therefore log and continue rather than erroring out.
	if len(photoData) > 0 {
		key := media.NewProfileKey(photoExt)
		if uerr := h.Media.Upload(r.Context(), key, bytes.NewReader(photoData), photoType); uerr != nil {
			slog.Error("signup: profile photo upload", "user_id", userID, "err", uerr)
		} else if serr := h.Svc.SetPhotoKey(r.Context(), userID, key); serr != nil {
			_ = h.Media.Delete(r.Context(), key)
			slog.Error("signup: set photo key", "user_id", userID, "err", serr)
		}
	}

	h.Renderer.HTML(w, "signup_check_email.html", render.Page(user(r), render.M{"Email": in.Email}))
}

func (h *Handlers) verify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		h.Renderer.Status(w, http.StatusBadRequest, "verify_invalid.html", render.Page(user(r), nil))
		return
	}
	userID, err := h.Svc.VerifyEmail(r.Context(), token)
	if err != nil {
		switch {
		case errors.Is(err, ErrTokenExpired):
			h.Renderer.Status(w, http.StatusGone, "verify_expired.html", render.Page(user(r), nil))
		case errors.Is(err, ErrTokenInvalid):
			h.Renderer.Status(w, http.StatusBadRequest, "verify_invalid.html", render.Page(user(r), nil))
		default:
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	// Log the user in by issuing a session.
	sess, err := h.Svc.CreateSession(r.Context(), userID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	SetSessionCookie(w, sess.Token, sess.ExpiresAt, h.Secure)
	http.Redirect(w, r, h.postAuthRedirect(w, r, "/"), http.StatusSeeOther)
}

func (h *Handlers) resendVerification(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	emailIn := NormaliseEmail(r.FormValue("email"))
	if emailIn == "" {
		h.Renderer.Status(w, http.StatusBadRequest, "verify_invalid.html", render.Page(user(r), nil))
		return
	}
	if err := h.Svc.ResendVerificationByEmail(r.Context(), emailIn); err != nil && !errors.Is(err, ErrTooMany) {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	// Always show the same confirmation page — don't reveal whether the email exists.
	h.Renderer.HTML(w, "verify_resent.html", render.Page(user(r), nil))
}

// ---- login ----

func (h *Handlers) loginForm(w http.ResponseWriter, r *http.Request) {
	h.Renderer.HTML(w, "login.html", render.Page(user(r), render.M{
		"Next": r.URL.Query().Get("next"),
	}))
}

func (h *Handlers) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	emailIn := NormaliseEmail(r.FormValue("email"))
	password := r.FormValue("password")
	next := r.FormValue("next")
	ip := middleware.ClientIP(r)

	if msg := ValidateEmail(emailIn); msg != "" {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "login.html", render.Page(user(r), render.M{
			"Email": emailIn, "Next": next, "EmailError": msg,
		}))
		return
	}

	sess, err := h.Svc.Authenticate(r.Context(), emailIn, password, ip)
	if err != nil {
		status := http.StatusUnauthorized
		data := render.Page(user(r), render.M{
			"Email": emailIn, "Next": next, "Error": "Email or password is incorrect.",
		})
		if errors.Is(err, ErrTooMany) {
			status = http.StatusTooManyRequests
			data["Error"] = "Too many failed attempts. Try again in 15 minutes."
			data["Locked"] = true
		}
		h.Renderer.Status(w, status, "login.html", data)
		return
	}
	SetSessionCookie(w, sess.Token, sess.ExpiresAt, h.Secure)
	target := "/"
	if next != "" && strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		target = next
	}
	// A pending invite takes precedence over the default/next target.
	http.Redirect(w, r, h.postAuthRedirect(w, r, target), http.StatusSeeOther)
}

func (h *Handlers) logout(w http.ResponseWriter, r *http.Request) {
	token := ReadSessionCookie(r)
	if err := h.Svc.DeleteSession(r.Context(), token); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	ClearSessionCookie(w, h.Secure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---- password reset ----

func (h *Handlers) forgotForm(w http.ResponseWriter, r *http.Request) {
	h.Renderer.HTML(w, "password_forgot.html", render.Page(user(r), nil))
}

func (h *Handlers) forgotSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	emailIn := r.FormValue("email")
	ip := middleware.ClientIP(r)
	if err := h.Svc.RequestPasswordReset(r.Context(), emailIn, ip); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "password_forgot_sent.html", render.Page(user(r), nil))
}

func (h *Handlers) resetForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		h.Renderer.Status(w, http.StatusBadRequest, "password_reset_invalid.html", render.Page(user(r), nil))
		return
	}
	h.Renderer.HTML(w, "password_reset.html", render.Page(user(r), render.M{
		"Token": token, "Errors": FieldErrors{},
	}))
}

func (h *Handlers) resetSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	token := r.FormValue("token")
	password := r.FormValue("password")
	errs := FieldErrors{}
	if m := ValidatePassword(password); m != "" {
		errs.Add("password", m)
	}
	if errs.Has() {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "password_reset.html", render.Page(user(r), render.M{
			"Token": token, "Errors": errs,
		}))
		return
	}
	err := h.Svc.ConsumePasswordReset(r.Context(), token, password)
	if err != nil {
		switch {
		case errors.Is(err, ErrTokenInvalid):
			h.Renderer.Status(w, http.StatusBadRequest, "password_reset_invalid.html", render.Page(user(r), nil))
		case errors.Is(err, ErrTokenExpired):
			h.Renderer.Status(w, http.StatusGone, "password_reset_expired.html", render.Page(user(r), nil))
		default:
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	h.Renderer.HTML(w, "password_reset_done.html", render.Page(user(r), nil))
}
