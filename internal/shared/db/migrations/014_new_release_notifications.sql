-- +goose Up

-- Add the 'new_release' notification type, sent to every user when a new
-- release ships (see notifications.Service.AnnounceRelease). SQLite can't alter
-- a CHECK constraint in place, so rebuild the table with the wider CHECK and
-- copy the surviving rows over, exactly as migration 010 did. Nothing
-- references notifications, so the rebuild is referentially safe.
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
    CHECK (type IN ('connection_request', 'connection_accepted', 'comment_on_post', 'reply_to_comment', 'new_release'))
);

INSERT INTO notifications (id, user_id, type, actor_id, post_id, comment_id, created_at, read_at)
SELECT id, user_id, type, actor_id, post_id, comment_id, created_at, read_at FROM notifications_old;

DROP TABLE notifications_old;

CREATE INDEX notifications_user_created_idx ON notifications(user_id, created_at DESC);
CREATE INDEX notifications_unread_idx ON notifications(user_id) WHERE read_at IS NULL;

-- Marks which releases have already been announced, so the startup broadcast
-- fires exactly once per release across restarts and deploys.
CREATE TABLE announced_releases (
    version      TEXT PRIMARY KEY,
    announced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down

DROP TABLE announced_releases;

-- Remove any new_release notifications before tightening the CHECK would reject
-- them, then rebuild the table without the 'new_release' type.
DELETE FROM notifications WHERE type = 'new_release';

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
