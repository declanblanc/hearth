-- +goose Up

-- Likes are a private signal: liking a post notifies the author and shows up in
-- an author-only liker list. There is no public indicator and no count anywhere
-- (CLAUDE.md §2, §11; Technical Plan §4.4.5). One row per (post, user); the
-- unique constraint makes liking idempotent.
CREATE TABLE likes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id    INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (post_id, user_id)
);

-- The author's liker list reads every like for a single post.
CREATE INDEX likes_post_idx ON likes(post_id);
-- "Has the viewer liked this post?" and unlike both filter by (user, post).
CREATE INDEX likes_user_post_idx ON likes(user_id, post_id);

-- Extend the notifications type CHECK to allow 'like_on_post'. SQLite can't
-- alter a CHECK constraint in place, so rebuild the table and copy rows over.
-- Nothing references notifications, so the rebuild is referentially safe. The
-- old table's indexes are dropped with it before we recreate them by name.
ALTER TABLE notifications RENAME TO notifications_old;

CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type       TEXT NOT NULL,
    actor_id   INTEGER REFERENCES users(id) ON DELETE CASCADE,
    post_id    INTEGER REFERENCES posts(id) ON DELETE CASCADE,
    comment_id INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    read_at    TIMESTAMP,
    CHECK (type IN ('connection_request', 'connection_accepted', 'comment_on_post', 'reply_to_comment', 'like_on_post'))
);

INSERT INTO notifications (id, user_id, type, actor_id, post_id, comment_id, created_at, read_at)
SELECT id, user_id, type, actor_id, post_id, comment_id, created_at, read_at FROM notifications_old;

DROP TABLE notifications_old;

CREATE INDEX notifications_user_created_idx ON notifications(user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications(user_id) WHERE read_at IS NULL;

-- Email-preference flag for likes. Gates Phase 3 email only; the in-app
-- notification is always created. Defaults to off like every other flag.
ALTER TABLE notification_preferences ADD COLUMN likes_on_posts INTEGER NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE notification_preferences DROP COLUMN likes_on_posts;

-- Reverse the rebuild: drop any like notifications, then restore the original
-- CHECK constraint without 'like_on_post'.
ALTER TABLE notifications RENAME TO notifications_old;

CREATE TABLE notifications (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type       TEXT NOT NULL,
    actor_id   INTEGER REFERENCES users(id) ON DELETE CASCADE,
    post_id    INTEGER REFERENCES posts(id) ON DELETE CASCADE,
    comment_id INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    read_at    TIMESTAMP,
    CHECK (type IN ('connection_request', 'connection_accepted', 'comment_on_post', 'reply_to_comment'))
);

INSERT INTO notifications (id, user_id, type, actor_id, post_id, comment_id, created_at, read_at)
SELECT id, user_id, type, actor_id, post_id, comment_id, created_at, read_at
  FROM notifications_old WHERE type != 'like_on_post';

DROP TABLE notifications_old;

CREATE INDEX notifications_user_created_idx ON notifications(user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications(user_id) WHERE read_at IS NULL;

DROP TABLE likes;
