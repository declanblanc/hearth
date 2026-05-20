-- +goose Up
CREATE TABLE email_verifications (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP NOT NULL,
    consumed_at  TIMESTAMP
);

CREATE INDEX email_verifications_user_id_idx ON email_verifications(user_id);

CREATE TABLE password_resets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP NOT NULL,
    consumed_at  TIMESTAMP,
    requester_ip TEXT
);

CREATE INDEX password_resets_user_id_idx ON password_resets(user_id);

-- Track failed login attempts per IP for the 10/15min limiter.
CREATE TABLE failed_logins (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ip           TEXT NOT NULL,
    attempted_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX failed_logins_ip_attempted_idx ON failed_logins(ip, attempted_at);

-- +goose Down
DROP TABLE failed_logins;
DROP TABLE password_resets;
DROP TABLE email_verifications;
