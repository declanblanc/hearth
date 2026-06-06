package main

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/dblanc/hearth/internal/auth"
)

// Dev seed credentials. These exist only to make local development and manual
// UI testing fast — you can log in immediately without running the
// signup → email-verify → login round-trip. Never enabled outside development
// (see seedDevUser's env guard in main).
const (
	devSeedEmail    = "dev@hearth.test"
	devSeedUsername = "dev"
	devSeedPassword = "devpassword123" // ≥12 chars, satisfies the signup rule.
	devSeedFirst    = "Dev"
	devSeedLast     = "User"
)

// seedDevUser ensures a pre-verified user exists for local development so the
// app is usable without going through email verification. It is idempotent:
// if the user already exists it does nothing. Only call this in development.
func seedDevUser(db *sql.DB, logger *slog.Logger) {
	var existingID int64
	err := db.QueryRow(`SELECT id FROM users WHERE email = ?`, devSeedEmail).Scan(&existingID)
	if err == nil {
		logger.Info("dev seed: user already present", "email", devSeedEmail, "username", devSeedUsername)
		return
	}
	if err != sql.ErrNoRows {
		logger.Error("dev seed: lookup failed", "err", err)
		return
	}

	hash, err := auth.HashPassword(devSeedPassword)
	if err != nil {
		logger.Error("dev seed: hash password", "err", err)
		return
	}

	// email_verified_at is set so the account is immediately able to post.
	_, err = db.Exec(
		`INSERT INTO users (username, email, password_hash, first_name, last_name, email_verified_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		devSeedUsername, devSeedEmail, hash, devSeedFirst, devSeedLast, time.Now(),
	)
	if err != nil {
		logger.Error("dev seed: insert user", "err", err)
		return
	}

	logger.Info("dev seed: created verified user",
		"email", devSeedEmail, "username", devSeedUsername, "password", devSeedPassword)
}
