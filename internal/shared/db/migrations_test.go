package db

import (
	"testing"
)

func TestMigrationsAgainstFreshDB(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	if err := Migrate(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	want := []string{"users", "sessions", "email_verifications", "password_resets", "failed_logins"}
	for _, name := range want {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
			t.Fatalf("query for %q: %v", name, err)
		}
		if n != 1 {
			t.Errorf("table %q missing", name)
		}
	}
}
