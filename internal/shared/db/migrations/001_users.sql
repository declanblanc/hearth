-- +goose Up
CREATE TABLE users (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT NOT NULL UNIQUE,
    email                TEXT NOT NULL UNIQUE,
    password_hash        TEXT NOT NULL,
    display_name         TEXT NOT NULL,
    photo_key            TEXT,
    bio                  TEXT,
    pronouns             TEXT,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    email_verified_at    TIMESTAMP,
    last_feed_loaded_at  TIMESTAMP,
    deleted_at           TIMESTAMP
);

CREATE INDEX users_deleted_at_idx ON users(deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose Down
DROP TABLE users;
