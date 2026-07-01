-- +goose Up

-- Threaded comments on posts (Technical Plan §3, Build Plan §2.2). A comment is
-- either top-level (parent_comment_id NULL) or a reply to another comment.
-- Access mirrors the post itself: only users connected to the post author may
-- read or add comments, enforced in the service via IsConnected (CLAUDE.md §1).
-- No count is ever derived from this table (CLAUDE.md §2).
--
-- Deleting a comment hard-deletes it and its entire reply subtree — no
-- tombstone is kept (Build Plan §2.2). The service collects the subtree with a
-- recursive CTE over parent_comment_id and deletes it explicitly; the ON DELETE
-- CASCADE on parent_comment_id is a belt-and-suspenders backstop, and it also
-- makes dropping a post remove its whole comment tree.
CREATE TABLE comments (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id           INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    parent_comment_id INTEGER REFERENCES comments(id) ON DELETE CASCADE,
    author_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    content           TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'active',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status IN ('active', 'deleted'))
);

-- Thread loading reads a whole post's comments oldest-first (chronological
-- ascending, Technical Plan §9.4 decision).
CREATE INDEX comments_post_created_idx ON comments(post_id, created_at);
-- Reply inserts and the "does this comment have children?" check filter by parent.
CREATE INDEX comments_parent_idx ON comments(parent_comment_id);

-- +goose Down
DROP TABLE comments;
