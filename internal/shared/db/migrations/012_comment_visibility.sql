-- +goose Up

-- Two opt-in, per-user controls for cross-network comment visibility. By default
-- a viewer only sees comments authored by themselves, the post author, or someone
-- they are directly connected with. These flags together open a narrow extra path:
-- a non-connection's comment on a shared post becomes visible only when the author
-- has opted in to sharing AND the viewer has opted in to seeing (symmetric consent).
--
-- Both default to 0, preserving today's strictly-by-connection behaviour for
-- everyone until they explicitly opt in.
ALTER TABLE users ADD COLUMN share_comments_with_non_connections INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN show_non_connection_comments INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN show_non_connection_comments;
ALTER TABLE users DROP COLUMN share_comments_with_non_connections;
