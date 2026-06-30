package posts

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
)

// CommentHandlers serve the comment endpoints. An htmx request gets just the
// re-rendered comment thread to swap in place, while a plain form POST (no JS)
// is redirected back to the page it came from, where the thread re-renders
// server-side.
type CommentHandlers struct {
	Svc      *CommentService
	Renderer *render.Renderer
}

func NewCommentHandlers(svc *CommentService, r *render.Renderer) *CommentHandlers {
	return &CommentHandlers{Svc: svc, Renderer: r}
}

// Mount registers the comment routes. Caller wraps with RequireAuth. Browsers
// can't issue DELETE from a form, so delete is reachable via both DELETE and a
// POST fallback, mirroring the posts package.
func (h *CommentHandlers) Mount(mux *http.ServeMux) {
	mux.Handle("POST /posts/{id}/comments", middleware.RequireAuth(http.HandlerFunc(h.create)))
	mux.Handle("POST /comments/{id}/replies", middleware.RequireAuth(http.HandlerFunc(h.reply)))
	mux.Handle("DELETE /comments/{id}", middleware.RequireAuth(http.HandlerFunc(h.delete)))
	mux.Handle("POST /comments/{id}/delete", middleware.RequireAuth(http.HandlerFunc(h.delete)))
}

func (h *CommentHandlers) create(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if !u.Verified {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	postID, ok := h.pathID(w, r)
	if !ok {
		return
	}
	_, err := h.Svc.Create(r.Context(), postID, u.ID, r.FormValue("content"))
	if err != nil {
		h.handleWriteError(w, r, "create comment", u.ID, err)
		return
	}
	h.renderThread(w, r, postID, u.ID)
}

func (h *CommentHandlers) reply(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	if !u.Verified {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	parentID, ok := h.pathID(w, r)
	if !ok {
		return
	}
	comment, err := h.Svc.Reply(r.Context(), parentID, u.ID, r.FormValue("content"))
	if err != nil {
		h.handleWriteError(w, r, "reply comment", u.ID, err)
		return
	}
	h.renderThread(w, r, comment.PostID, u.ID)
}

func (h *CommentHandlers) delete(w http.ResponseWriter, r *http.Request) {
	u := middleware.UserFrom(r.Context())
	commentID, ok := h.pathID(w, r)
	if !ok {
		return
	}
	postID, err := h.Svc.Delete(r.Context(), commentID, u.ID)
	switch {
	case errors.Is(err, ErrCommentNotFound):
		h.Renderer.Error(w, http.StatusNotFound)
		return
	case errors.Is(err, ErrCommentForbidden):
		h.Renderer.Error(w, http.StatusForbidden)
		return
	case err != nil:
		slog.Error("comments: delete", "user_id", u.ID, "comment_id", commentID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	h.renderThread(w, r, postID, u.ID)
}

// handleWriteError maps create/reply errors to responses. Validation problems
// (empty/too long) come back as 422; the privacy errors render as 404/403 to
// match how the rest of the app hides inaccessible content.
func (h *CommentHandlers) handleWriteError(w http.ResponseWriter, r *http.Request, op string, userID int64, err error) {
	switch {
	case errors.Is(err, ErrCommentEmpty), errors.Is(err, ErrCommentTooLong):
		h.Renderer.Status(w, http.StatusUnprocessableEntity, "error.html",
			render.M{"Status": http.StatusUnprocessableEntity, "Message": "Your comment couldn’t be posted."})
	case errors.Is(err, ErrCommentNotFound):
		h.Renderer.Error(w, http.StatusNotFound)
	case errors.Is(err, ErrCommentForbidden):
		h.Renderer.Error(w, http.StatusForbidden)
	default:
		slog.Error("comments: "+op, "user_id", userID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
	}
}

// renderThread re-renders a single post's comment thread. htmx requests get the
// bare thread fragment to swap in place; everyone else (no-JS) is redirected
// back to where they were, where the thread re-renders with the page.
func (h *CommentHandlers) renderThread(w http.ResponseWriter, r *http.Request, postID, viewerID int64) {
	if r.Header.Get("HX-Request") != "true" {
		target := r.Header.Get("Referer")
		if target == "" {
			target = "/"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	roots, err := h.Svc.ListThread(r.Context(), postID, viewerID)
	if err != nil {
		slog.Error("comments: list thread", "user_id", viewerID, "post_id", postID, "err", err)
		h.Renderer.Error(w, http.StatusInternalServerError)
		return
	}
	// commentthread expects a Post carrying .ID and .Comments.
	h.Renderer.HTML(w, "comment_thread_fragment.html", &Post{ID: postID, Comments: roots})
}

// pathID parses the {id} path value, writing a 400 and returning ok=false when
// missing or malformed.
func (h *CommentHandlers) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		h.Renderer.Error(w, http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
