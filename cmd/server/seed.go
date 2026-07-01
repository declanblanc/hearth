package main

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/dblanc/hearth/internal/auth"
	"github.com/dblanc/hearth/internal/notifications"
)

// Dev seed credentials. These exist only to make local development and manual
// UI testing fast — you can log in immediately without running the
// signup → email-verify → login round-trip. Never enabled outside development
// (see seedDevUser's env guard in main).
const (
	devSeedEmail    = "dev@hearth.test"
	devSeedUsername = "dev"
	devSeedPassword = "devpassword123" // ≥12 chars, satisfies the signup rule.
	devSeedFirst    = "Dev"
	devSeedLast     = "User"

	// Shared password for the seeded friend accounts, so they can be logged
	// into for testing the other side of a conversation.
	devFriendPassword = "friendpassword123" // ≥12 chars.
)

// seedDevUser ensures a pre-verified user exists for local development so the
// app is usable without going through email verification, then seeds a handful
// of connected friends, posts, and threaded comments so the UI has realistic
// content to render. It is idempotent: re-running it never duplicates rows.
// Only call this in development.
func seedDevUser(db *sql.DB, logger *slog.Logger) {
	devID := ensureUser(db, logger, devSeedUsername, devSeedEmail, devSeedPassword,
		devSeedFirst, devSeedLast, "Building Hearth, one warm corner at a time.", "they/them")
	if devID == 0 {
		return
	}

	seedDevContent(db, logger, devID)
}

// ensureUser inserts a pre-verified user if one with the given email does not
// already exist, returning the user's id (existing or newly created). Returns 0
// only on an unexpected error, which is logged. All seeded users are verified so
// they can post immediately.
func ensureUser(db *sql.DB, logger *slog.Logger, username, email, password, first, last, bio, pronouns string) int64 {
	var existingID int64
	err := db.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&existingID)
	if err == nil {
		return existingID
	}
	if err != sql.ErrNoRows {
		logger.Error("dev seed: user lookup failed", "email", email, "err", err)
		return 0
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		logger.Error("dev seed: hash password", "email", email, "err", err)
		return 0
	}

	// email_verified_at is set so the account is immediately able to post.
	res, err := db.Exec(
		`INSERT INTO users (username, email, password_hash, first_name, last_name, bio, pronouns, email_verified_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		username, email, hash, first, last, bio, pronouns, time.Now(),
	)
	if err != nil {
		logger.Error("dev seed: insert user", "email", email, "err", err)
		return 0
	}

	id, err := res.LastInsertId()
	if err != nil {
		logger.Error("dev seed: read new user id", "email", email, "err", err)
		return 0
	}

	logger.Info("dev seed: created verified user", "email", email, "username", username, "id", id)
	return id
}

// seedDevContent populates the dev user's world with connected friends, posts,
// and threaded comments so post/comment styling can be evaluated against
// realistic data. It is idempotent: if the dev user already has any posts it
// assumes content was seeded before and does nothing.
func seedDevContent(db *sql.DB, logger *slog.Logger, devID int64) {
	var existingPosts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM posts WHERE author_id = ?`, devID).Scan(&existingPosts); err != nil {
		logger.Error("dev seed: count posts", "err", err)
		return
	}
	if existingPosts > 0 {
		logger.Info("dev seed: content already present, skipping")
		return
	}

	// Connected friends, each verified and ready to post.
	mayaID := ensureUser(db, logger, "maya", "maya@hearth.test", devFriendPassword,
		"Maya", "Okonkwo", "Gardener, baker, perpetual tea-drinker.", "she/her")
	theoID := ensureUser(db, logger, "theo", "theo@hearth.test", devFriendPassword,
		"Theo", "Hartley", "Reads too much, sleeps too little.", "he/him")
	nadiaID := ensureUser(db, logger, "nadia", "nadia@hearth.test", devFriendPassword,
		"Nadia", "Rahman", "Early riser. Collector of small, quiet moments.", "she/her")
	if mayaID == 0 || theoID == 0 || nadiaID == 0 {
		logger.Error("dev seed: friend creation failed, aborting content seed")
		return
	}

	// Connect the dev user to every friend. Connections are stored once per
	// pair as (min_id, max_id) — never both directions (CLAUDE.md §3).
	for _, friendID := range []int64{mayaID, theoID, nadiaID} {
		connectUsers(db, logger, devID, friendID)
	}

	now := time.Now()
	hoursAgo := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }

	posts := []seedPost{
		{
			author:   devID,
			ageHours: 2,
			content:  "Finally nailed the sourdough crumb this weekend after three loaves of bricks. The trick was a longer cold proof — almost 36 hours in the fridge. Patience, apparently, is an ingredient.",
			comments: []reply{
				{author: mayaID, content: "This looks incredible! That crust is doing exactly what a crust should do.", children: []reply{
					{author: devID, content: "Thank you! Happy to write the whole thing up if you want it.", children: []reply{
						{author: mayaID, content: "Yes please — I'll trade you for the focaccia recipe."},
					}},
				}},
				{author: theoID, content: "The crumb structure is genuinely beautiful. Open but not gummy."},
			},
		},
		{
			author:   mayaID,
			ageHours: 6,
			content:  "The garden is finally waking up. First tomatoes went in this morning, and the basil from last year somehow overwintered. Spring is showing off.",
			comments: []reply{
				{author: devID, content: "What varieties did you plant?", children: []reply{
					{author: mayaID, content: "Sungold and a San Marzano this year. Aiming for sauce by August."},
				}},
				{author: nadiaID, content: "So lovely. There's nothing like the smell of basil first thing."},
			},
		},
		{
			author:   devID,
			ageHours: 28,
			content:  "Repainted the back fence a deep, sun-warmed green. Took the whole afternoon and most of a podcast backlog, but it changes the entire feel of the yard.",
			comments: []reply{
				{author: theoID, content: "Bold choice — and it absolutely works with the brick."},
			},
		},
		{
			author:   theoID,
			ageHours: 30,
			content:  "Finished a book that completely undid me in the best way. Going to sit with it for a few days before I can talk about it properly.",
			comments: []reply{
				{author: nadiaID, content: "The mark of a good one. No spoilers — just tell me the title when you're ready."},
				{author: devID, content: "Adding it to the pile sight unseen. I trust your taste."},
			},
		},
		{
			author:   nadiaID,
			ageHours: 52,
			content:  "Caught the sunrise from the hill this morning. Cold enough to see my breath, quiet enough to hear the whole town still asleep. Worth every lost minute of sleep.",
			comments: []reply{
				{author: mayaID, content: "This is the kind of thing that makes the early alarm worth it."},
			},
		},
		{
			author:   devID,
			ageHours: 74,
			content:  "Small win: finally fixed the squeaky stair that's been announcing my every midnight snack for two years. A little glue, a single screw. Two years.",
			comments: nil,
		},
	}

	for _, p := range posts {
		postID := insertPost(db, logger, p.author, p.content, hoursAgo(p.ageHours))
		if postID == 0 {
			continue
		}
		// Comments start a few minutes after the post and step forward as the
		// thread descends, so the conversation reads in a believable order.
		commentTime := hoursAgo(p.ageHours).Add(15 * time.Minute)
		for _, c := range p.comments {
			commentTime = insertCommentTree(db, logger, postID, p.author, 0, 0, c, commentTime)
		}
	}

	logger.Info("dev seed: created friends, posts, comments, and notifications",
		"friends", 3, "posts", len(posts))
}

// connectUsers creates a connection between two users, normalizing the pair to
// (min_id, max_id) so it is stored once (CLAUDE.md §3). Idempotent via the
// unique pair index — a duplicate insert is ignored.
func connectUsers(db *sql.DB, logger *slog.Logger, a, b int64) {
	low, high := a, b
	if low > high {
		low, high = high, low
	}
	_, err := db.Exec(
		`INSERT OR IGNORE INTO connections (user_a_id, user_b_id) VALUES (?, ?)`,
		low, high,
	)
	if err != nil {
		logger.Error("dev seed: connect users", "a", a, "b", b, "err", err)
	}
}

// insertPost inserts an active post with explicit timestamps and returns its id
// (0 on error). Seeded timestamps let the feed's new/old split be exercised.
func insertPost(db *sql.DB, logger *slog.Logger, authorID int64, content string, createdAt time.Time) int64 {
	res, err := db.Exec(
		`INSERT INTO posts (author_id, content, status, created_at, updated_at)
		 VALUES (?, ?, 'active', ?, ?)`,
		authorID, content, createdAt, createdAt,
	)
	if err != nil {
		logger.Error("dev seed: insert post", "author", authorID, "err", err)
		return 0
	}
	id, err := res.LastInsertId()
	if err != nil {
		logger.Error("dev seed: read new post id", "err", err)
		return 0
	}
	return id
}

// reply is one node in a seeded comment thread: an author, what they said, and
// any nested replies beneath it.
type reply struct {
	author   int64
	content  string
	children []reply
}

// seedPost is a post to seed plus the comment tree hanging off it. ageHours
// spreads posts across time so the feed's new/old split has content on each side.
type seedPost struct {
	author   int64
	content  string
	ageHours int
	comments []reply
}

// insertCommentTree inserts one comment and then, recursively, its replies.
// parentID is 0 for a top-level comment; postAuthorID and parentAuthorID are the
// authors of the post and of the parent comment, used to route notifications.
// Each comment is timestamped a few minutes after the previous one so the thread
// reads in conversational order; the running clock is threaded through and
// returned so siblings advance too.
func insertCommentTree(db *sql.DB, logger *slog.Logger, postID, postAuthorID, parentID, parentAuthorID int64, node reply, at time.Time) time.Time {
	var parent any
	if parentID != 0 {
		parent = parentID
	}

	res, err := db.Exec(
		`INSERT INTO comments (post_id, parent_comment_id, author_id, content, status, created_at)
		 VALUES (?, ?, ?, ?, 'active', ?)`,
		postID, parent, node.author, node.content, at,
	)
	if err != nil {
		logger.Error("dev seed: insert comment", "post", postID, "err", err)
		return at
	}
	commentID, err := res.LastInsertId()
	if err != nil {
		logger.Error("dev seed: read new comment id", "err", err)
		return at
	}

	// Mirror the real fan-out (CLAUDE.md §8) so the dev user has comment
	// notifications to look at: a top-level comment notifies the post author, a
	// reply notifies the parent comment's author. Never notify yourself.
	recipient, notifType := postAuthorID, notifications.TypeCommentOnPost
	if parentID != 0 {
		recipient, notifType = parentAuthorID, notifications.TypeReplyToComment
	}
	if recipient != node.author {
		insertNotification(db, logger, recipient, notifType, node.author, postID, commentID, at)
	}

	next := at.Add(7 * time.Minute)
	for _, child := range node.children {
		next = insertCommentTree(db, logger, postID, postAuthorID, commentID, node.author, child, next)
	}
	return next
}

// insertNotification writes one notification row directly, bypassing the
// service so the seeded created_at matches the comment it refers to (the service
// always stamps time.Now()). Seeding these lets the notifications page, its
// unread dot, and its deep-links be exercised locally.
func insertNotification(db *sql.DB, logger *slog.Logger, userID int64, notifType string, actorID, postID, commentID int64, createdAt time.Time) {
	_, err := db.Exec(
		`INSERT INTO notifications (user_id, type, actor_id, post_id, comment_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		userID, notifType, actorID, postID, commentID, createdAt,
	)
	if err != nil {
		logger.Error("dev seed: insert notification", "user", userID, "type", notifType, "err", err)
	}
}
