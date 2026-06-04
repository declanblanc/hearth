-- +goose Up

-- Images attached to a post. One row per file, ordered by `position` (0-based).
-- The R2 object key intentionally embeds neither user_id nor post_id
-- (CLAUDE.md §6); access is gated per-request by signing a short-lived URL only
-- after the viewer's connection to the author is confirmed.
CREATE TABLE post_media (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id      INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    object_key   TEXT NOT NULL,
    content_type TEXT NOT NULL,
    position     INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Media for a post is always read all-at-once, ordered by position.
CREATE INDEX post_media_post_idx ON post_media(post_id, position);

-- +goose Down
DROP TABLE post_media;
