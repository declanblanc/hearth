package config

import "testing"

func TestLoadAcceptsStaging(t *testing.T) {
	t.Setenv("HEARTH_ENV", "staging")
	t.Setenv("HEARTH_COOKIE_SECRET", "a-sufficiently-long-cookie-secret-value")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error for staging env: %v", err)
	}
	if cfg.Env != EnvStaging {
		t.Fatalf("Env = %q, want %q", cfg.Env, EnvStaging)
	}
	if !cfg.IsDeployed() {
		t.Error("IsDeployed() = false, want true for staging")
	}
	if cfg.IsProd() {
		t.Error("IsProd() = true, want false for staging")
	}
	if !cfg.ShouldSeed() {
		t.Error("ShouldSeed() = false, want true for staging")
	}
}

func TestStagingRequiresCookieSecret(t *testing.T) {
	t.Setenv("HEARTH_ENV", "staging")
	t.Setenv("HEARTH_COOKIE_SECRET", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() = nil error, want error when staging has no cookie secret")
	}
}

func TestSeedAndDeployMatrix(t *testing.T) {
	cases := []struct {
		env          Env
		wantSeed     bool
		wantDeployed bool
	}{
		{EnvDevelopment, true, false},
		{EnvTest, false, false},
		{EnvStaging, true, true},
		{EnvProduction, false, true},
	}
	for _, tc := range cases {
		cfg := Config{Env: tc.env}
		if got := cfg.ShouldSeed(); got != tc.wantSeed {
			t.Errorf("%s: ShouldSeed() = %v, want %v", tc.env, got, tc.wantSeed)
		}
		if got := cfg.IsDeployed(); got != tc.wantDeployed {
			t.Errorf("%s: IsDeployed() = %v, want %v", tc.env, got, tc.wantDeployed)
		}
	}
}
