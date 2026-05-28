package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

// Handlers wires the auth service to HTTP routes.
type Handlers struct {
	Svc      *Service
	Renderer *render.Renderer
	Secure   bool // true in production — controls Secure cookie flag
}

func NewHandlers(svc *Service, r *render.Renderer, secure bool) *Handlers {
	return &Handlers{Svc: svc, Renderer: r, Secure: secure}
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
	if err := r.ParseForm(); err != nil {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
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
	if errs.Has() {
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "signup.html", render.Page(user(r), render.M{
			"Username": in.Username, "Email": in.Email,
			"FirstName": in.FirstName, "LastName": in.LastName, "Errors": errs,
		}))
		return
	}

	_, err := h.Svc.Signup(r.Context(), in)
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
	emailIn := r.FormValue("email")
	password := r.FormValue("password")
	next := r.FormValue("next")
	ip := middleware.ClientIP(r)

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
	http.Redirect(w, r, target, http.StatusSeeOther)
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
