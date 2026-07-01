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

// TestRender_NotificationsDeepLinkAndText renders the real notifications.html
// template through the production renderer and asserts each notification is a
// single block-level link to its point of interest — a comment/reply to the
// comment on the post author's profile, a connection to the connected person's
// profile — with meaningful text and no separate inner links.
func TestRender_NotificationsDeepLinkAndText(t *testing.T) {
	r, err := render.NewFromEmbed(web.TemplatesFS, "templates", true)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}

	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	items := []Item{
		// Comment notifications go to the post author (alice).
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

	// Comment and reply notifications deep-link into the post author's profile,
	// anchored to the new comment itself (#comment-{id}) so the thread scrolls to
	// and highlights it (#59). The connection_accepted notification must name the
	// accepter, link to their profile (so the inviter can disconnect), and use
	// the "accepted your invitation" wording.
	for _, want := range []string{
		`href="/u/alice#comment-3"`, // commented-on: anchors the new comment (author = recipient)
		`href="/u/frank#comment-5"`, // replied-to: anchors the reply comment on the POST author's profile
		"commented on",
		"replied to",
		`href="/u/erin"`, // connection_accepted: actor profile link
		"accepted your invitation",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered notifications missing %q\n---\n%s", want, html)
		}
	}

	// The generic fallback must never appear for these known types.
	if strings.Contains(html, "You have a new notification") {
		t.Errorf("generic fallback text leaked for a known notification type\n---\n%s", html)
	}

	// The whole item is the only link: the actor's name must NOT be a separate
	// profile link, and there should be exactly one anchor per notification.
	if strings.Contains(html, `href="/u/carol"`) || strings.Contains(html, `href="/u/dave"`) {
		t.Errorf("actor name should not be a separate link; only the item links\n---\n%s", html)
	}
	if n := strings.Count(html, `class="notification-item`); n != len(items) {
		t.Errorf("expected exactly one notification-item anchor per notification (%d), got %d\n---\n%s", len(items), n, html)
	}
}
