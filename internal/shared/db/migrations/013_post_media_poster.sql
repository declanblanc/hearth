-- +goose Up

-- A video attachment can't be shown inline before it loads the way an image can,
-- so each video carries a poster: a still frame extracted on the client at upload
-- time, stored as its own object and referenced here. The poster fills the
-- video's box (and gives the player a thumbnail) until the viewer hits play.
--
-- poster_key is NULL for image rows — content_type already distinguishes images
-- (image/*) from video (video/mp4), so no separate kind column is needed. The
-- video's width/height (migration 011) reuse the poster's pixel dimensions, which
-- the server derives from the poster image rather than trusting the client.
ALTER TABLE post_media ADD COLUMN poster_key TEXT;

-- +goose Down
ALTER TABLE post_media DROP COLUMN poster_key;
