-- +goose Up

-- Connections store one row per pair, always (lower_id, higher_id), so the
-- pair is unique and we never insert both directions (CLAUDE.md §3).
CREATE TABLE connections (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_a_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    user_b_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (user_a_id < user_b_id)
);

CREATE UNIQUE INDEX connections_pair_idx ON connections(user_a_id, user_b_id);
CREATE INDEX connections_user_a_idx ON connections(user_a_id);
CREATE INDEX connections_user_b_idx ON connections(user_b_id);
-- created_at is used by the 10-accepts-per-7-days rate-limit query.
CREATE INDEX connections_created_at_idx ON connections(created_at);

-- Invites are single-use links. The token is consumed when the recipient
-- submits a connection request, not when the link is opened (CLAUDE.md §5).
CREATE TABLE invites (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    sender_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token       TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP NOT NULL,
    consumed_at TIMESTAMP
);

-- Rate-limit query: invites sent by a user in the trailing 7 days.
CREATE INDEX invites_sender_created_idx ON invites(sender_id, created_at);

CREATE TABLE connection_requests (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    invite_id    INTEGER NOT NULL REFERENCES invites(id) ON DELETE CASCADE,
    requester_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    recipient_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status       TEXT NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at  TIMESTAMP,
    CHECK (status IN ('pending', 'accepted', 'denied'))
);

-- The /requests view lists pending incoming requests for a recipient.
CREATE INDEX connection_requests_recipient_idx ON connection_requests(recipient_id, status);

CREATE TABLE posts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    author_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content    TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status IN ('active', 'archived', 'deleted'))
);

-- Feed and profile queries select a single author's active posts, newest first.
CREATE INDEX posts_author_created_idx ON posts(author_id, created_at DESC);

-- One row per user; all email flags default false. In-app notifications are
-- always created regardless — these flags only gate email (Phase 3).
CREATE TABLE notification_preferences (
    user_id             INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    connection_requests INTEGER NOT NULL DEFAULT 0,
    comments_on_posts   INTEGER NOT NULL DEFAULT 0,
    replies_to_comments INTEGER NOT NULL DEFAULT 0
);

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

-- Newest-first listing for the /notifications view.
CREATE INDEX notifications_user_created_idx ON notifications(user_id, created_at DESC);
-- Cheap "any unread?" check for the header dot.
CREATE INDEX notifications_unread_idx ON notifications(user_id) WHERE read_at IS NULL;

-- +goose Down
DROP TABLE notifications;
DROP TABLE notification_preferences;
DROP TABLE posts;
DROP TABLE connection_requests;
DROP TABLE invites;
DROP TABLE connections;
