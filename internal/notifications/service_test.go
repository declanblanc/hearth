package notifications

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/dblanc/hearth/internal/shared/db"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.Migrate(d); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}
	return d
}

// seedUser inserts a bare user row and returns its ID.
func seedUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO users (username, email, password_hash, first_name, last_name)
		 VALUES (?, ?, 'x', ?, 'User')`,
		username, username+"@example.com", username)
	if err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestCreateAndList(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()

	recipient := seedUser(t, d, "recipient")
	actor := seedUser(t, d, "actor")

	if err := svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionRequest, ActorID: actor}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	items, err := svc.List(ctx, recipient)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 notification, got %d", len(items))
	}
	if items[0].Type != TypeConnectionRequest {
		t.Errorf("type: got %q", items[0].Type)
	}
	if items[0].ActorUsername != "actor" {
		t.Errorf("actor username: got %q", items[0].ActorUsername)
	}
	if items[0].Read {
		t.Error("new notification should be unread")
	}
}

func TestList_NewestFirst(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	recipient := seedUser(t, d, "r")

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return base }
	_ = svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionRequest})
	svc.Now = func() time.Time { return base.Add(time.Hour) }
	_ = svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionAccepted})

	items, _ := svc.List(ctx, recipient)
	if len(items) != 2 {
		t.Fatalf("want 2, got %d", len(items))
	}
	if items[0].Type != TypeConnectionAccepted {
		t.Errorf("newest should be first; got %q", items[0].Type)
	}
}

func TestHasUnreadAndMarkRead(t *testing.T) {
	d := newTestDB(t)
	svc := New(d)
	ctx := context.Background()
	recipient := seedUser(t, d, "r")

	if unread, _ := svc.HasUnread(ctx, recipient); unread {
		t.Error("no notifications yet — HasUnread should be false")
	}
	_ = svc.Create(ctx, nil, Params{UserID: recipient, Type: TypeConnectionRequest})
	if unread, _ := svc.HasUnread(ctx, recipient); !unread {
		t.Error("HasUnread should be true after create")
	}

	if err := svc.MarkAllRead(ctx, recipient); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if unread, _ := svc.HasUnread(ctx, recipient); unread {
		t.Error("HasUnread should be false after MarkAllRead")
	}
	items, _ := svc.List(ctx, recipient)
	if len(items) != 1 || !items[0].Read {
		t.Error("notification should still exist and be marked read")
	}
}
