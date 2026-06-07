package connections

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/dblanc/hearth/internal/media"
	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

type Handlers struct {
	Svc      *Service
	Renderer *render.Renderer
	Media    media.Store // may be nil in dev
	Secret   []byte      // HMAC key for the pending_invite cookie
	Secure   bool
}

func NewHandlers(svc *Service, r *render.Renderer, m media.Store, secret []byte, secure bool) *Handlers {
	return &Handlers{Svc: svc, Renderer: r, Media: m, Secret: secret, Secure: secure}
}

// Mount registers connection routes. Public routes (/i/{token}, /welcome) are
// registered bare; authenticated routes are wrapped with RequireAuth.
func (h *Handlers) Mount(mux *http.ServeMux) {
	authed := func(fn http.HandlerFunc) http.Handler {
		return middleware.RequireAuth(http.HandlerFunc(fn))
	}
	// Public, user-aware.
	mux.HandleFunc("GET /i/{token}", h.openInvite)
	mux.HandleFunc("GET /welcome", h.welcome)

	// Authenticated.
	mux.Handle("POST /invites", authed(h.createInvite))
	mux.Handle("POST /i/{token}/request", authed(h.submitRequest))
	mux.Handle("GET /requests", authed(h.listRequests))
	mux.Handle("POST /requests/{id}/accept", authed(h.acceptRequest))
	mux.Handle("POST /requests/{id}/deny", authed(h.denyRequest))
	mux.Handle("GET /connections", authed(h.listConnections))
	mux.Handle("POST /connections/{user_id}/disconnect", authed(h.disconnect))
}

// ---- invite opening (the §4.2 state machine) ----

func (h *Handlers) openInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	viewer := middleware.UserFrom(r.Context())
	var viewerID int64
	if viewer != nil {
		viewerID = viewer.ID
	}

	res, err := h.Svc.Resolve(r.Context(), token, viewerID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}

	// Anonymous visitor: stash the token in a signed cookie and send them to
	// the welcome chooser. We do this only for otherwise-valid invites.
	if viewer == nil {
		if res.State == StateInvalid {
			h.Renderer.Status(w, http.StatusGone, "invite_invalid.html", render.Page(nil, nil))
			return
		}
		SetPendingInviteCookie(w, h.Secret, token, time.Now(), h.Secure)
		http.Redirect(w, r, "/welcome?invite="+token, http.StatusSeeOther)
		return
	}

	switch res.State {
	case StateInvalid:
		h.Renderer.Status(w, http.StatusGone, "invite_invalid.html", render.Page(viewer, nil))
	case StateSelf:
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "invite_self.html", render.Page(viewer, nil))
	case StateAlreadyConnected:
		h.Renderer.HTML(w, "invite_connected.html", render.Page(viewer, render.M{
			"SenderName": res.SenderName, "SenderUsername": res.SenderUsername,
		}))
	default: // StateValid
		h.Renderer.HTML(w, "invite_request.html", render.Page(viewer, render.M{
			"Token": token, "SenderName": res.SenderName,
			"SenderUsername": res.SenderUsername, "SenderPhotoURL": h.photoURL(res.SenderPhotoKey),
		}))
	}
}

func (h *Handlers) welcome(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("invite")
	// An already-signed-in user doesn't need the chooser; send them straight on.
	if middleware.UserFrom(r.Context()) != nil && token != "" {
		http.Redirect(w, r, "/i/"+token, http.StatusSeeOther)
		return
	}
	h.Renderer.HTML(w, "welcome.html", render.Page(nil, render.M{"Token": token}))
}

func (h *Handlers) submitRequest(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if !u.Verified {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	token := r.PathValue("token")
	err := h.Svc.CreateRequest(r.Context(), token, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, ErrInviteInvalid):
			h.Renderer.Status(w, http.StatusGone, "invite_invalid.html", render.Page(u, nil))
		case errors.Is(err, ErrSelfConnect):
			h.Renderer.Status(w, http.StatusUnprocessableEntity, "invite_self.html", render.Page(u, nil))
		case errors.Is(err, ErrAlreadyConnected):
			h.Renderer.HTML(w, "invite_connected.html", render.Page(u, render.M{}))
		default:
			slog.Error("connections: create request", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	// Clear the pending-invite cookie — its job is done.
	ClearPendingInviteCookie(w, h.Secure)
	h.Renderer.HTML(w, "request_sent.html", render.Page(u, nil))
}

// ---- invites management ----

func (h *Handlers) createInvite(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if !u.Verified {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	token, err := h.Svc.CreateInvite(r.Context(), u.ID)
	if err != nil {
		var rl *RateLimitError
		if errors.As(err, &rl) {
			h.renderInviteLink(w, r, u, http.StatusTooManyRequests, "", rateLimitMessage(rl))
			return
		}
		slog.Error("connections: create invite", "user_id", u.ID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.renderInviteLink(w, r, u, http.StatusOK, h.Svc.inviteURL(token), "")
}

// renderInviteLink shows a freshly minted, single-use invitation link. The
// connections-page modal (invite-modal.js) requests just the link fragment via
// X-Fragment; a plain form POST with no JavaScript instead gets the standalone
// page, so the flow still works without scripts. Invites are one-time use, so
// there is nothing to list — each generation just hands back the new link.
func (h *Handlers) renderInviteLink(w http.ResponseWriter, r *http.Request, u *middleware.User, status int, url, errMsg string) {
	if r.Header.Get("X-Fragment") == "1" {
		h.Renderer.Status(w, status, "invite_link_fragment.html", render.M{"URL": url, "Error": errMsg})
		return
	}
	h.Renderer.Status(w, status, "invite_created.html", render.Page(u, render.M{"URL": url, "Error": errMsg}))
}

// ---- requests ----

func (h *Handlers) listRequests(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	reqs, err := h.Svc.ListPendingRequests(r.Context(), u.ID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	views := make([]requestView, 0, len(reqs))
	for _, req := range reqs {
		views = append(views, requestView{
			ID: req.ID, Name: req.RequesterName, Username: req.RequesterUsername,
			PhotoURL: h.photoURL(req.RequesterPhotoKey), CreatedAt: req.CreatedAt,
		})
	}
	h.Renderer.HTML(w, "requests.html", render.Page(u, render.M{"Requests": views, "Error": ""}))
}

type requestView struct {
	ID        int64
	Name      string
	Username  string
	PhotoURL  string
	CreatedAt time.Time
}

func (h *Handlers) acceptRequest(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if !u.Verified {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	err := h.Svc.AcceptRequest(r.Context(), id, u.ID)
	if err != nil {
		var rl *RateLimitError
		switch {
		case errors.As(err, &rl):
			h.renderRequestsError(w, r, u, http.StatusTooManyRequests, rateLimitMessage(rl))
		case errors.Is(err, ErrRequestNotFound), errors.Is(err, ErrNotRecipient):
			h.Renderer.Error(w, http.StatusNotFound)
		case errors.Is(err, ErrRequestResolved):
			http.Redirect(w, r, "/requests", http.StatusSeeOther)
		default:
			slog.Error("connections: accept", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/requests", http.StatusSeeOther)
}

func (h *Handlers) denyRequest(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	id, ok := pathID(r, "id")
	if !ok {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	err := h.Svc.DenyRequest(r.Context(), id, u.ID)
	if err != nil {
		switch {
		case errors.Is(err, ErrRequestNotFound), errors.Is(err, ErrNotRecipient):
			h.Renderer.Error(w, http.StatusNotFound)
		case errors.Is(err, ErrRequestResolved):
			http.Redirect(w, r, "/requests", http.StatusSeeOther)
		default:
			slog.Error("connections: deny", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/requests", http.StatusSeeOther)
}

func (h *Handlers) renderRequestsError(w http.ResponseWriter, r *http.Request, u *middleware.User, status int, msg string) {
	reqs, _ := h.Svc.ListPendingRequests(r.Context(), u.ID)
	views := make([]requestView, 0, len(reqs))
	for _, req := range reqs {
		views = append(views, requestView{
			ID: req.ID, Name: req.RequesterName, Username: req.RequesterUsername,
			PhotoURL: h.photoURL(req.RequesterPhotoKey), CreatedAt: req.CreatedAt,
		})
	}
	h.Renderer.Status(w, status, "requests.html", render.Page(u, render.M{"Requests": views, "Error": msg}))
}

// ---- connections list / disconnect ----

func (h *Handlers) listConnections(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	people, err := h.Svc.ListConnections(r.Context(), u.ID)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	views := make([]personView, 0, len(people))
	for _, p := range people {
		views = append(views, personView{
			ID: p.ID, Name: p.Name, Username: p.Username, PhotoURL: h.photoURL(p.PhotoKey),
		})
	}
	h.Renderer.HTML(w, "connections.html", render.Page(u, render.M{"People": views}))
}

type personView struct {
	ID       int64
	Name     string
	Username string
	PhotoURL string
}

func (h *Handlers) disconnect(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	otherID, ok := pathID(r, "user_id")
	if !ok {
		h.Renderer.Error(w, http.StatusBadRequest)
		return
	}
	if err := h.Svc.Disconnect(r.Context(), u.ID, otherID); err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/connections", http.StatusSeeOther)
}

// ---- helpers ----

func (h *Handlers) photoURL(key string) string {
	if h.Media == nil || key == "" {
		return ""
	}
	return h.Media.URL(key)
}

func pathID(r *http.Request, name string) (int64, bool) {
	v := r.PathValue(name)
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func rateLimitMessage(rl *RateLimitError) string {
	noun := "invites"
	if rl.Kind == "connection" {
		noun = "new connections"
	}
	return "You've reached your weekly limit for " + noun + ". Try again on " +
		rl.LiftAt.Format("Jan 2, 2006 at 3:04 PM") + " UTC."
}
