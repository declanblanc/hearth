package posts

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dblanc/hearth/internal/connections"
	"github.com/dblanc/hearth/internal/notifications"
)

// newCommentSvc wires a CommentService against the real connections and
// notifications services, so the privacy check and notification fan-out are
// exercised end to end.
func newCommentSvc(d *sql.DB) *CommentService {
	notifSvc := notifications.New(d)
	connSvc := connections.New(d, notifSvc, "https://hearth.test")
	return NewCommentService(d, connSvc, notifSvc)
}

// connect inserts the canonical (lower, higher) connection row directly.
func connect(t *testing.T, d *sql.DB, x, y int64) {
	t.Helper()
	a, b := x, y
	if a > b {
		a, b = b, a
	}
	if _, err := d.Exec(
		`INSERT INTO connections (user_a_id, user_b_id) VALUES (?, ?)`, a, b); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// seedActivePost inserts an active post for the author and returns its id.
func seedActivePost(t *testing.T, d *sql.DB, authorID int64) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO posts (author_id, content, status) VALUES (?, 'hello', 'active')`, authorID)
	if err != nil {
		t.Fatalf("seed post: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// notifCount returns how many notifications of a type a user has — test-only;
// the product never surfaces counts.
func notifCount(t *testing.T, d *sql.DB, userID int64, notifType string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND type = ?`, userID, notifType,
	).Scan(&n); err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

func TestComment_CreateNotifiesPostAuthor(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	c, err := svc.Create(ctx, post, commenter, "  nice post  ")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.Content != "nice post" {
		t.Errorf("content not trimmed: %q", c.Content)
	}
	if c.ParentID != 0 {
		t.Errorf("top-level comment should have no parent, got %d", c.ParentID)
	}
	if n := notifCount(t, d, author, notifications.TypeCommentOnPost); n != 1 {
		t.Errorf("want 1 comment_on_post for author, got %d", n)
	}
}

func TestComment_NoSelfNotificationOnOwnPost(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	post := seedActivePost(t, d, author)

	if _, err := svc.Create(ctx, post, author, "talking to myself"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n := notifCount(t, d, author, notifications.TypeCommentOnPost); n != 0 {
		t.Errorf("commenting on your own post must not notify you, got %d", n)
	}
}

func TestComment_RequiresVisibility(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	stranger := seedUser(t, d, "stranger") // not connected
	post := seedActivePost(t, d, author)

	if _, err := svc.Create(ctx, post, stranger, "let me in"); !errors.Is(err, ErrCommentForbidden) {
		t.Errorf("non-connected commenter should be forbidden, got %v", err)
	}
	// A missing/inactive post is not found.
	if _, err := svc.Create(ctx, 99999, author, "ghost"); !errors.Is(err, ErrCommentNotFound) {
		t.Errorf("comment on missing post should be not found, got %v", err)
	}
}

func TestComment_Validation(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	post := seedActivePost(t, d, author)

	if _, err := svc.Create(ctx, post, author, "   "); !errors.Is(err, ErrCommentEmpty) {
		t.Errorf("empty comment should be rejected, got %v", err)
	}
	if _, err := svc.Create(ctx, post, author, strings.Repeat("x", MaxCommentLen+1)); !errors.Is(err, ErrCommentTooLong) {
		t.Errorf("over-long comment should be rejected, got %v", err)
	}
}

func TestReply_NotifiesParentAuthorOnly(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	// commenter leaves a top-level comment; author replies to it.
	top, err := svc.Create(ctx, post, commenter, "first!")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reply, err := svc.Reply(ctx, top.ID, author, "thanks")
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if reply.ParentID != top.ID {
		t.Errorf("reply parent = %d, want %d", reply.ParentID, top.ID)
	}
	// The parent's author (commenter) gets a reply_to_comment notification.
	if n := notifCount(t, d, commenter, notifications.TypeReplyToComment); n != 1 {
		t.Errorf("want 1 reply_to_comment for parent author, got %d", n)
	}
	// The post author (who wrote the reply) gets none — no self-notification.
	if n := notifCount(t, d, author, notifications.TypeReplyToComment); n != 0 {
		t.Errorf("replier must not be notified, got %d", n)
	}
}

func TestReply_AllowsArbitraryDepth(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	top, _ := svc.Create(ctx, post, commenter, "top-level")
	reply, err := svc.Reply(ctx, top.ID, author, "a reply")
	if err != nil {
		t.Fatalf("Reply to top-level: %v", err)
	}
	// Replying to a reply is now allowed: the data tree is arbitrary depth.
	deep, err := svc.Reply(ctx, reply.ID, commenter, "reply to a reply")
	if err != nil {
		t.Fatalf("reply to a reply should succeed, got %v", err)
	}
	if deep.ParentID != reply.ID {
		t.Errorf("deep reply parent = %d, want %d", deep.ParentID, reply.ID)
	}
}

func TestReply_CannotReplyToDeletedComment(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	// Deleting a comment removes its row outright, so a later reply targeting it
	// can't find a parent.
	parent, _ := svc.Create(ctx, post, commenter, "parent")
	if _, err := svc.Delete(ctx, parent.ID, commenter); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if _, err := svc.Reply(ctx, parent.ID, author, "into the void"); !errors.Is(err, ErrCommentNotFound) {
		t.Errorf("replying to a deleted comment should be ErrCommentNotFound, got %v", err)
	}
}

// TestListThread_FlattensDeepRepliesWithMention builds a 3-deep chain
// (comment → reply → reply-to-reply) and confirms rendering flattens both
// replies into one time-ordered tier under the root, each carrying the display
// name of the comment it directly answers.
func TestListThread_FlattensDeepRepliesWithMention(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	top, _ := svc.Create(ctx, post, commenter, "top-level")
	middle, _ := svc.Reply(ctx, top.ID, author, "middle reply")
	deep, _ := svc.Reply(ctx, middle.ID, commenter, "deep reply")

	roots, err := svc.ListThread(ctx, post, author)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != top.ID {
		t.Fatalf("want one root (the top-level comment), got %+v", roots)
	}
	root := roots[0]
	if len(root.Children) != 2 {
		t.Fatalf("want both replies flattened under the root, got %d", len(root.Children))
	}
	// Time-ordered: the middle reply precedes the deep one.
	if root.Children[0].ID != middle.ID || root.Children[1].ID != deep.ID {
		t.Errorf("flattened replies out of order: got %d, %d; want %d, %d",
			root.Children[0].ID, root.Children[1].ID, middle.ID, deep.ID)
	}
	// The deep reply answers the middle reply, so its mention names the middle
	// reply's author.
	if got := root.Children[1].ReplyToName; got != middle.AuthorName {
		t.Errorf("deep reply ReplyToName = %q, want %q", got, middle.AuthorName)
	}
}

// TestListThread_ReplyToSelf confirms a reply's ReplyToSelf flag is set only
// when the comment it answers belongs to the viewer, so the mention can read
// "replying to you" instead of the viewer's own name.
func TestListThread_ReplyToSelf(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	top, _ := svc.Create(ctx, post, author, "top-level by author")
	reply, _ := svc.Reply(ctx, top.ID, commenter, "commenter answers author")
	deep, _ := svc.Reply(ctx, reply.ID, author, "author answers commenter")

	// Viewing as the author: the commenter's reply answers the author's own
	// comment, so it's a self-reply; the author's deep reply answers the
	// commenter, so it is not.
	roots, err := svc.ListThread(ctx, post, author)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	root := roots[0]
	if root.ReplyToSelf {
		t.Error("top-level comment should not be marked ReplyToSelf")
	}
	byID := map[int64]*Comment{}
	for _, c := range root.Children {
		byID[c.ID] = c
	}
	if !byID[reply.ID].ReplyToSelf {
		t.Error("reply to the viewer's own comment should be ReplyToSelf")
	}
	if byID[deep.ID].ReplyToSelf {
		t.Error("reply to another user's comment should not be ReplyToSelf")
	}
}

// TestListThread_HidesNonConnectedAuthors is issue #23's exact scenario: Bob
// comments on Alice's post; Eve is connected to Alice but not Bob, so Eve must
// not see Bob's comment — and any reply nested under it goes with it.
func TestListThread_HidesNonConnectedAuthors(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	alice := seedUser(t, d, "alice")
	bob := seedUser(t, d, "bob")
	eve := seedUser(t, d, "eve")
	connect(t, d, alice, bob)
	connect(t, d, alice, eve) // Eve and Bob are NOT connected
	post := seedActivePost(t, d, alice)

	aliceComment, _ := svc.Create(ctx, post, alice, "alice's note")
	bobComment, _ := svc.Create(ctx, post, bob, "bob's note")
	// Alice replies under Bob's comment; Eve can't see Bob's, so this goes too.
	_, _ = svc.Reply(ctx, bobComment.ID, alice, "alice replying to bob")

	roots, err := svc.ListThread(ctx, post, eve)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != aliceComment.ID {
		t.Fatalf("Eve should see only Alice's comment, got %+v", roots)
	}
	if len(roots[0].Children) != 0 {
		t.Errorf("no replies should leak under a hidden comment, got %+v", roots[0].Children)
	}

	// Alice (connected to both) still sees the whole thread.
	roots, err = svc.ListThread(ctx, post, alice)
	if err != nil {
		t.Fatalf("ListThread for alice: %v", err)
	}
	if len(roots) != 2 {
		t.Errorf("post author should see both top-level comments, got %d", len(roots))
	}
}

func TestReply_NoSelfNotification(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	top, _ := svc.Create(ctx, post, commenter, "my own thread")
	if _, err := svc.Reply(ctx, top.ID, commenter, "replying to myself"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if n := notifCount(t, d, commenter, notifications.TypeReplyToComment); n != 0 {
		t.Errorf("replying to your own comment must not notify you, got %d", n)
	}
}

func TestDelete_PermissionRules(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	other := seedUser(t, d, "other")
	connect(t, d, author, commenter)
	connect(t, d, author, other)
	post := seedActivePost(t, d, author)

	c, _ := svc.Create(ctx, post, commenter, "delete me")
	// A third party (neither comment author nor post owner) may not delete.
	if _, err := svc.Delete(ctx, c.ID, other); !errors.Is(err, ErrCommentForbidden) {
		t.Errorf("stranger delete should be forbidden, got %v", err)
	}
	// The post owner may delete a commenter's comment.
	if _, err := svc.Delete(ctx, c.ID, author); err != nil {
		t.Errorf("post owner should be able to delete, got %v", err)
	}
}

// TestDelete_HardDeletesWholeSubtree confirms deleting a comment removes it and
// every descendant (replies, replies of replies) with no tombstone left behind —
// so the whole sub-thread disappears from the rendered thread.
func TestDelete_HardDeletesWholeSubtree(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	post := seedActivePost(t, d, author)

	// A 3-deep chain under the target, plus a sibling top-level comment that must
	// survive the delete untouched.
	parent, _ := svc.Create(ctx, post, commenter, "parent")
	reply, _ := svc.Reply(ctx, parent.ID, author, "child")
	deep, _ := svc.Reply(ctx, reply.ID, commenter, "grandchild")
	sibling, _ := svc.Create(ctx, post, commenter, "unrelated")

	if _, err := svc.Delete(ctx, parent.ID, commenter); err != nil {
		t.Fatalf("Delete parent: %v", err)
	}

	// Every comment in the deleted subtree is gone from the table.
	for _, id := range []int64{parent.ID, reply.ID, deep.ID} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM comments WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("comment %d in deleted subtree should be gone, found %d rows", id, n)
		}
	}

	// The unrelated sibling is untouched, and the thread renders it alone with no
	// tombstone standing in for the deleted subtree.
	roots, err := svc.ListThread(ctx, post, author)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	if len(roots) != 1 || roots[0].ID != sibling.ID {
		t.Fatalf("expected only the surviving sibling, got %+v", roots)
	}
	if len(roots[0].Children) != 0 {
		t.Errorf("surviving sibling should have no replies, got %+v", roots[0].Children)
	}
}

func TestListThread_RequiresVisibility(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	stranger := seedUser(t, d, "stranger")
	post := seedActivePost(t, d, author)

	if _, err := svc.ListThread(ctx, post, stranger); !errors.Is(err, ErrCommentForbidden) {
		t.Errorf("non-connected viewer should not load the thread, got %v", err)
	}
}

// setCommentVisibility flips a user's two cross-network comment flags directly.
func setCommentVisibility(t *testing.T, d *sql.DB, userID int64, share, show bool) {
	t.Helper()
	if _, err := d.Exec(
		`UPDATE users SET share_comments_with_non_connections = ?, show_non_connection_comments = ? WHERE id = ?`,
		share, show, userID,
	); err != nil {
		t.Fatalf("setCommentVisibility: %v", err)
	}
}

// TestListThread_CrossNetworkVisibility walks the full opt-in truth table. Carol
// owns the post; Alice (author of the comment) and Eve (the viewer) are each
// connected to Carol but NOT to each other. Eve sees Alice's comment only when
// Alice has opted in to sharing AND Eve has opted in to seeing — both required.
func TestListThread_CrossNetworkVisibility(t *testing.T) {
	cases := []struct {
		name        string
		aliceShares bool
		eveShows    bool
		wantVisible bool
	}{
		{"both off (default)", false, false, false},
		{"only author shares", true, false, false},
		{"only viewer shows", false, true, false},
		{"both on", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDB(t)
			svc := newCommentSvc(d)
			ctx := context.Background()
			carol := seedUser(t, d, "carol")
			alice := seedUser(t, d, "alice")
			eve := seedUser(t, d, "eve")
			connect(t, d, carol, alice)
			connect(t, d, carol, eve) // Alice and Eve are NOT connected
			post := seedActivePost(t, d, carol)

			aliceComment, _ := svc.Create(ctx, post, alice, "alice's note")
			setCommentVisibility(t, d, alice, tc.aliceShares, false)
			setCommentVisibility(t, d, eve, false, tc.eveShows)

			roots, err := svc.ListThread(ctx, post, eve)
			if err != nil {
				t.Fatalf("ListThread: %v", err)
			}
			var sawAlice bool
			for _, r := range roots {
				if r.ID == aliceComment.ID {
					sawAlice = true
				}
			}
			if sawAlice != tc.wantVisible {
				t.Errorf("Eve sees Alice's comment = %v, want %v", sawAlice, tc.wantVisible)
			}
		})
	}
}

// TestListThread_CrossNetworkReply confirms the opt-in path applies per comment:
// a reply by a non-connection rides the same both-flags-on rule, and the viewer
// always sees their own and the post author's comments regardless of the flags.
func TestListThread_CrossNetworkReply(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	carol := seedUser(t, d, "carol")
	alice := seedUser(t, d, "alice")
	eve := seedUser(t, d, "eve")
	connect(t, d, carol, alice)
	connect(t, d, carol, eve) // Alice and Eve are NOT connected
	post := seedActivePost(t, d, carol)

	// Carol's own comment, Eve's own comment, and Alice's top-level comment with
	// a reply from Alice under it.
	carolComment, _ := svc.Create(ctx, post, carol, "carol's note")
	eveComment, _ := svc.Create(ctx, post, eve, "eve's note")
	aliceTop, _ := svc.Create(ctx, post, alice, "alice top")
	aliceReply, _ := svc.Reply(ctx, aliceTop.ID, alice, "alice reply")

	// Both opt in: Eve should now see Alice's whole sub-thread plus her own and
	// the post author's comments.
	setCommentVisibility(t, d, alice, true, false)
	setCommentVisibility(t, d, eve, false, true)

	roots, err := svc.ListThread(ctx, post, eve)
	if err != nil {
		t.Fatalf("ListThread: %v", err)
	}
	seen := map[int64]*Comment{}
	for _, r := range roots {
		seen[r.ID] = r
	}
	for _, id := range []int64{carolComment.ID, eveComment.ID, aliceTop.ID} {
		if seen[id] == nil {
			t.Errorf("expected comment %d to be visible to Eve", id)
		}
	}
	if top := seen[aliceTop.ID]; top == nil || len(top.Children) != 1 || top.Children[0].ID != aliceReply.ID {
		t.Errorf("Alice's reply should be nested and visible once both opt in, got %+v", seen[aliceTop.ID])
	}
}

func TestAttachToPosts_NestsRepliesAndSetsCanDelete(t *testing.T) {
	d := newTestDB(t)
	svc := newCommentSvc(d)
	ctx := context.Background()
	author := seedUser(t, d, "author")
	commenter := seedUser(t, d, "commenter")
	connect(t, d, author, commenter)
	postID := seedActivePost(t, d, author)

	top, _ := svc.Create(ctx, postID, commenter, "top")
	_, _ = svc.Reply(ctx, top.ID, author, "reply")

	ps := []Post{{ID: postID, AuthorID: author}}
	if err := svc.AttachToPosts(ctx, author, ps); err != nil {
		t.Fatalf("AttachToPosts: %v", err)
	}
	if len(ps[0].Comments) != 1 {
		t.Fatalf("want 1 root comment, got %d", len(ps[0].Comments))
	}
	root := ps[0].Comments[0]
	if len(root.Children) != 1 {
		t.Fatalf("want 1 nested reply, got %d", len(root.Children))
	}
	// Viewer is the post author: they may delete the commenter's top-level
	// comment, and (as its author) their own reply.
	if !root.CanDelete {
		t.Error("post author should be able to delete a comment on their post")
	}
	if !root.Children[0].CanDelete {
		t.Error("author should be able to delete their own reply")
	}
}
