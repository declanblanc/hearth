package notifications

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dblanc/hearth/internal/shared/middleware"
	"github.com/dblanc/hearth/internal/shared/render"
	"github.com/dblanc/hearth/web"
)

// TestRender_NotificationsDeepLinkAndText is a render-path smoke test for issue
// #22: it renders the real notifications.html template through the production
// renderer and asserts (a) comment/like notifications produce a deep link to
// the post and meaningful text, and (b) the generic "You have a new
// notification" fallback is never reached for known types.
func TestRender_NotificationsDeepLinkAndText(t *testing.T) {
	r, err := render.NewFromEmbed(web.TemplatesFS, "templates", true)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}

	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	items := []Item{
		// Like and comment notifications go to the post author (alice).
		{ID: 1, Type: TypeLikeOnPost, ActorUsername: "bob", ActorName: "Bob", PostID: 42, PostAuthorUsername: "alice", CreatedAt: now},
		{ID: 2, Type: TypeCommentOnPost, ActorUsername: "carol", ActorName: "Carol", PostID: 7, PostAuthorUsername: "alice", CommentID: 3, CreatedAt: now},
		// A reply notification can reach alice even when the post is someone
		// else's (frank's); the link must target frank's profile, not alice's.
		{ID: 3, Type: TypeReplyToComment, ActorUsername: "dave", ActorName: "Dave", PostID: 9, PostAuthorUsername: "frank", CommentID: 5, CreatedAt: now},
		{ID: 4, Type: TypeConnectionAccepted, ActorUsername: "erin", ActorName: "Erin", CreatedAt: now},
	}
	user := &middleware.User{ID: 100, Username: "alice"}

	rec := httptest.NewRecorder()
	r.HTML(rec, "notifications.html", render.Page(user, render.M{"Items": items}))
	if rec.Code != 200 {
		t.Fatalf("render status: got %d", rec.Code)
	}
	html := rec.Body.String()

	// Like and comment notifications must deep-link to the recipient's own
	// profile, anchored to the specific post.
	for _, want := range []string{
		`href="/u/alice#post-42"`, // liked post (author = recipient)
		`href="/u/alice#post-7"`,  // commented-on post (author = recipient)
		`href="/u/frank#post-9"`,  // replied-to: link targets the POST author, not the recipient
		"commented on",
		"replied to",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered notifications missing %q\n---\n%s", want, html)
		}
	}

	// The generic fallback must never appear for these known types.
	if strings.Contains(html, "You have a new notification") {
		t.Errorf("generic fallback text leaked for a known notification type\n---\n%s", html)
	}
}
