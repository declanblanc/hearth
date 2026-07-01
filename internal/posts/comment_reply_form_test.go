package posts

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dblanc/hearth/internal/shared/render"
	"github.com/dblanc/hearth/web"
)

// TestRender_ReplyFormPerRootAfterReplies guards issue #58's final placement:
// each root comment carries exactly one reply form, and that form renders after
// the root's replies (at the bottom of the sub-thread) rather than once at the
// bottom of the whole post. A second root gets its own form, so a reply opens
// under the comment it belongs to.
func TestRender_ReplyFormPerRootAfterReplies(t *testing.T) {
	r, err := render.NewFromEmbed(web.TemplatesFS, "templates", true)
	if err != nil {
		t.Fatalf("renderer: %v", err)
	}

	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	// Root 1 has two flattened replies; root 2 has none.
	root1 := &Comment{ID: 1, PostID: 42, ParentID: 0, AuthorName: "Alice", CreatedAt: now, Children: []*Comment{
		{ID: 2, PostID: 42, ParentID: 1, AuthorName: "Bob", ReplyToName: "Alice", CreatedAt: now},
		{ID: 3, PostID: 42, ParentID: 2, AuthorName: "Carol", ReplyToName: "Bob", CreatedAt: now},
	}}
	root2 := &Comment{ID: 9, PostID: 42, ParentID: 0, AuthorName: "Dave", CreatedAt: now}

	rec := httptest.NewRecorder()
	r.HTML(rec, "comment_thread_fragment.html", &Post{ID: 42, Comments: []*Comment{root1, root2}})
	if rec.Code != 200 {
		t.Fatalf("render status: got %d", rec.Code)
	}
	html := rec.Body.String()

	// Exactly one reply form per root comment (id reply-<rootID>), and none
	// keyed to a flattened child (reply-2, reply-3 must not exist).
	forms := regexp.MustCompile(`id="reply-(\d+)"`).FindAllStringSubmatch(html, -1)
	got := map[string]bool{}
	for _, m := range forms {
		got[m[1]] = true
	}
	if !got["1"] || !got["9"] {
		t.Errorf("expected a reply form for each root (reply-1, reply-9); got ids %v", got)
	}
	if got["2"] || got["3"] {
		t.Errorf("flattened children must not carry their own reply form; got ids %v", got)
	}
	if len(forms) != 2 {
		t.Errorf("expected exactly 2 reply forms (one per root), got %d", len(forms))
	}

	// Root 1's form must come AFTER its last reply (Carol) so it sits at the
	// bottom of the sub-thread, and BEFORE root 2 opens — i.e. still inside
	// root 1's <li>.
	posCarol := strings.Index(html, ">Carol<") // Carol's rendered author link text
	posForm1 := strings.Index(html, `id="reply-1"`)
	posRoot2 := strings.Index(html, `id="comment-9"`)
	if posCarol < 0 || posForm1 < 0 || posRoot2 < 0 {
		t.Fatalf("missing expected markers: carol=%d form1=%d root2=%d\n%s", posCarol, posForm1, posRoot2, html)
	}
	if !(posCarol < posForm1 && posForm1 < posRoot2) {
		t.Errorf("root 1's reply form must render after its replies and before root 2: carol=%d form1=%d root2=%d", posCarol, posForm1, posRoot2)
	}
}
