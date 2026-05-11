package sessionstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteStore_PersistsTranscriptAcrossInstances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")

	store, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	if err := store.Save(ctx, Session{
		ID: "session-1",
		Messages: []Message{
			{Role: "user", Content: "I need a refund"},
			{Role: "assistant", Content: "Please share your order ID."},
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("OpenSQLite() reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	got, err := reopened.Get(ctx, "session-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want %d", len(got.Messages), 2)
	}
	if got.Messages[0].Content != "I need a refund" {
		t.Fatalf("Messages[0].Content = %q, want %q", got.Messages[0].Content, "I need a refund")
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero")
	}
}

func TestStore_GetMissingReturnsErrNotFound(t *testing.T) {
	store, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatalf("OpenSQLite() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestPostgresDialect_SharesStorageContract(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer func() { _ = db.Close() }()

	store, err := newSQLStore(context.Background(), db, dialectPostgres)
	if err != nil {
		t.Fatalf("newSQLStore() error = %v", err)
	}

	now := time.Now().UTC().Round(time.Second)
	if err := store.Save(context.Background(), Session{
		ID: "pg-like",
		Messages: []Message{
			{Role: "user", Content: "Refund order 123"},
		},
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Get(context.Background(), "pg-like")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want %d", len(got.Messages), 1)
	}
	if got.Messages[0].Content != "Refund order 123" {
		t.Fatalf("Messages[0].Content = %q, want %q", got.Messages[0].Content, "Refund order 123")
	}
}
