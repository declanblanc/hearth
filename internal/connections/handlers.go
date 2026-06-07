package connections

import (
	"context"
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
	mux.Handle("POST /i/{token}/accept", authed(h.acceptInvite))
	mux.Handle("GET /connections", authed(h.listConnections))
	mux.Handle("POST /connections/{id}/confirm", authed(h.confirmConnection))
	mux.Handle("POST /connections/{id}/decline", authed(h.declineConnection))
	mux.Handle("POST /connections/{user_id}/disconnect", authed(h.disconnect))
}

// LoadPendingDot is middleware that flags the Connections tab dot for
// authenticated users who have a pending connection awaiting their confirm or
// decline (issue #13). It mirrors notifications.LoadUnread: it mutates the
// context User so every page's base template can show the dot without each
// handler threading it through. Errors are swallowed — a missing dot is not
// worth a 500.
func (h *Handlers) LoadPendingDot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := middleware.UserFrom(r.Context()); u != nil {
			if pending, err := h.Svc.HasPendingRequests(r.Context(), u.ID); err == nil {
				u.ConnectionsDot = pending
			}
		}
		next.ServeHTTP(w, r)
	})
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

func (h *Handlers) acceptInvite(w http.ResponseWriter, r *http.Request) {
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
	h.Renderer.HTML(w, "invite_accepted.html", render.Page(u, nil))
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

// renderInviteLink shows a freshly minted, single-use invite link. The
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

// ---- pending connections (incoming requests) ----

type requestView struct {
	ID        int64
	Name      string
	Username  string
	PhotoURL  string
	CreatedAt time.Time
}

// confirmConnection completes a connection for someone who accepted the
// viewer's invite. On success — or when the pending connection has already been
// resolved — it returns to the connections page where pending confirmations now
// live (the dedicated requests page was removed).
func (h *Handlers) confirmConnection(w http.ResponseWriter, r *http.Request) {
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
			h.renderConnectionsError(w, r, u, http.StatusTooManyRequests, rateLimitMessage(rl))
		case errors.Is(err, ErrRequestNotFound), errors.Is(err, ErrNotRecipient):
			h.Renderer.Error(w, http.StatusNotFound)
		case errors.Is(err, ErrRequestResolved):
			http.Redirect(w, r, "/connections", http.StatusSeeOther)
		default:
			slog.Error("connections: confirm", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/connections", http.StatusSeeOther)
}

// declineConnection turns away a pending connection without notifying the
// requester (deny is silent), then returns to the connections page.
func (h *Handlers) declineConnection(w http.ResponseWriter, r *http.Request) {
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
			http.Redirect(w, r, "/connections", http.StatusSeeOther)
		default:
			slog.Error("connections: decline", "user_id", u.ID, "err", err)
			h.Renderer.Error(w, http.StatusInternalServerError)
		}
		return
	}
	http.Redirect(w, r, "/connections", http.StatusSeeOther)
}

// ---- connections list / disconnect ----

func (h *Handlers) listConnections(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	data, err := h.connectionsPageData(r.Context(), u, "")
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.HTML(w, "connections.html", render.Page(u, data))
}

// renderConnectionsError re-renders the connections page with a banner message,
// used for the rate-limit path when confirming a connection.
func (h *Handlers) renderConnectionsError(w http.ResponseWriter, r *http.Request, u *middleware.User, status int, msg string) {
	data, err := h.connectionsPageData(r.Context(), u, msg)
	if err != nil {
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.Renderer.Status(w, status, "connections.html", render.Page(u, data))
}

// connectionsPageData assembles everything the connections page shows: the
// pending incoming requests pinned to the top (people who accepted the viewer's
// invite and await confirmation) and the established connections below.
func (h *Handlers) connectionsPageData(ctx context.Context, u *middleware.User, errMsg string) (render.M, error) {
	reqs, err := h.Svc.ListPendingRequests(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	requestViews := make([]requestView, 0, len(reqs))
	for _, req := range reqs {
		requestViews = append(requestViews, requestView{
			ID: req.ID, Name: req.RequesterName, Username: req.RequesterUsername,
			PhotoURL: h.photoURL(req.RequesterPhotoKey), CreatedAt: req.CreatedAt,
		})
	}

	// Outgoing pending: people the viewer accepted an invite from and is now
	// waiting on to confirm (issue #15). Read-only — the viewer can't act on
	// these; the entry clears when the recipient resolves the request.
	outgoing, err := h.Svc.ListOutgoingPending(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	outgoingViews := make([]personView, 0, len(outgoing))
	for _, p := range outgoing {
		outgoingViews = append(outgoingViews, personView{
			ID: p.ID, Name: p.Name, Username: p.Username, PhotoURL: h.photoURL(p.PhotoKey),
		})
	}

	people, err := h.Svc.ListConnections(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	peopleViews := make([]personView, 0, len(people))
	for _, p := range people {
		peopleViews = append(peopleViews, personView{
			ID: p.ID, Name: p.Name, Username: p.Username, PhotoURL: h.photoURL(p.PhotoKey),
		})
	}

	return render.M{
		"Requests": requestViews,
		"Outgoing": outgoingViews,
		"People":   peopleViews,
		"Error":    errMsg,
	}, nil
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
