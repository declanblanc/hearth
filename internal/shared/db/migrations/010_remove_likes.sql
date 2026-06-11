-- +goose Up

-- The like feature is removed entirely: no more liking posts, no liker lists,
-- no like notifications (reverses migration 007). Drop the table, the unused
-- email-preference flag, and the 'like_on_post' notification type.

-- Existing like notifications have nothing to point at once likes are gone;
-- remove them before tightening the CHECK below would otherwise reject them.
DELETE FROM notifications WHERE type = 'like_on_post';

-- SQLite can't alter a CHECK constraint in place, so rebuild the table without
-- 'like_on_post' and copy the surviving rows over. Nothing references
-- notifications, so the rebuild is referentially safe.
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
SELECT id, user_id, type, actor_id, post_id, comment_id, created_at, read_at FROM notifications_old;

DROP TABLE notifications_old;

CREATE INDEX notifications_user_created_idx ON notifications(user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications(user_id) WHERE read_at IS NULL;

-- The email-preference flag for likes is no longer meaningful.
ALTER TABLE notification_preferences DROP COLUMN likes_on_posts;

-- Finally, drop the likes table itself.
DROP TABLE likes;

-- +goose Down

-- Recreate the likes table and its indexes (from migration 007).
CREATE TABLE likes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id    INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (post_id, user_id)
);

CREATE INDEX likes_post_idx ON likes(post_id);
CREATE INDEX likes_user_post_idx ON likes(user_id, post_id);

-- Restore the email-preference flag.
ALTER TABLE notification_preferences ADD COLUMN likes_on_posts INTEGER NOT NULL DEFAULT 0;

-- Restore the 'like_on_post' notification type by rebuilding the CHECK.
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
