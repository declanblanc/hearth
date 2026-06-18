// Package config loads typed runtime configuration from environment variables once at startup.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

type Env string

const (
	EnvDevelopment Env = "development"
	EnvTest        Env = "test"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

type Config struct {
	Env          Env
	Addr         string
	BaseURL      string
	DBPath       string
	CookieSecret []byte

	ResendAPIKey string
	EmailFrom    string

	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Bucket          string
	R2PublicHost      string
}

func (c Config) IsProd() bool { return c.Env == EnvProduction }

// IsDeployed reports whether this is a publicly reachable environment
// (production or staging), as opposed to local development or tests.
func (c Config) IsDeployed() bool { return c.Env == EnvProduction || c.Env == EnvStaging }

// ShouldSeed reports whether mock data should be seeded at startup.
// Never true in production.
func (c Config) ShouldSeed() bool { return c.Env == EnvDevelopment || c.Env == EnvStaging }

// R2Configured reports whether enough R2 credentials are present to enable media uploads.
func (c Config) R2Configured() bool {
	return c.R2AccountID != "" && c.R2AccessKeyID != "" && c.R2SecretAccessKey != "" && c.R2Bucket != ""
}

// ResendConfigured reports whether real Resend email sending is enabled.
// When false, callers should use the stub email sender.
func (c Config) ResendConfigured() bool { return c.ResendAPIKey != "" }

func Load() (Config, error) {
	cfg := Config{
		Env:          Env(getenv("HEARTH_ENV", "development")),
		Addr:         getenv("HEARTH_ADDR", ":8080"),
		BaseURL:      strings.TrimRight(getenv("HEARTH_BASE_URL", "http://localhost:8080"), "/"),
		DBPath:       getenv("HEARTH_DB_PATH", "./data/hearth.db"),
		ResendAPIKey: os.Getenv("RESEND_API_KEY"),
		EmailFrom:    getenv("HEARTH_EMAIL_FROM", "Hearth <noreply@hearth.app>"),

		R2AccountID:       os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:          os.Getenv("R2_BUCKET"),
		R2PublicHost:      os.Getenv("R2_PUBLIC_HOST"),
	}

	secret := os.Getenv("HEARTH_COOKIE_SECRET")
	if secret == "" {
		if cfg.IsDeployed() {
			return cfg, errors.New("HEARTH_COOKIE_SECRET is required in production and staging")
		}
		// Development default — stable across restarts so dev cookies survive.
		secret = "dev-cookie-secret-not-for-production-use-32b"
	}
	if len(secret) < 32 {
		return cfg, fmt.Errorf("HEARTH_COOKIE_SECRET must be at least 32 bytes, got %d", len(secret))
	}
	cfg.CookieSecret = []byte(secret)

	switch cfg.Env {
	case EnvDevelopment, EnvTest, EnvStaging, EnvProduction:
	default:
		return cfg, fmt.Errorf("invalid HEARTH_ENV: %q", cfg.Env)
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
