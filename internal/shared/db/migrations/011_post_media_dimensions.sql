-- +goose Up

-- Record each image's pixel dimensions so the client can reserve the exact box
-- (via the <img> width/height attributes) before the image loads. This is what
-- prevents the feed from jumping as photos stream in, and lets a skeleton fill
-- that same box while loading.
--
-- Both columns are nullable and added without touching existing rows: photos
-- uploaded before this migration simply have no recorded dimensions and render
-- without a reserved aspect ratio, exactly as they did before.
ALTER TABLE post_media ADD COLUMN width INTEGER;
ALTER TABLE post_media ADD COLUMN height INTEGER;

-- +goose Down
ALTER TABLE post_media DROP COLUMN height;
ALTER TABLE post_media DROP COLUMN width;
