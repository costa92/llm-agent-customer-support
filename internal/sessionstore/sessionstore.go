package sessionstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("sessionstore: session not found")

type Store interface {
	Get(context.Context, string) (Session, error)
	Save(context.Context, Session) error
	Close() error
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Session struct {
	ID        string    `json:"id"`
	Messages  []Message `json:"messages"`
	UpdatedAt time.Time `json:"updated_at"`
}

type dialect string

const (
	dialectSQLite   dialect = "sqlite"
	dialectPostgres dialect = "postgres"
)

type sqlStore struct {
	db      *sql.DB
	dialect dialect
}

func OpenSQLite(ctx context.Context, dsn string) (Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	store, err := newSQLStore(ctx, db, dialectSQLite)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func OpenPostgres(ctx context.Context, dsn string) (Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	store, err := newSQLStore(ctx, db, dialectPostgres)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func newSQLStore(ctx context.Context, db *sql.DB, d dialect) (*sqlStore, error) {
	store := &sqlStore{db: db, dialect: d}
	if err := store.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *sqlStore) Get(ctx context.Context, id string) (Session, error) {
	query := "SELECT messages_json, updated_at FROM support_sessions WHERE session_id = ?"
	if s.dialect == dialectPostgres {
		query = "SELECT messages_json, updated_at FROM support_sessions WHERE session_id = $1"
	}
	var raw string
	var updatedAt time.Time
	err := s.db.QueryRowContext(ctx, query, id).Scan(&raw, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	var messages []Message
	if err := json.Unmarshal([]byte(raw), &messages); err != nil {
		return Session{}, fmt.Errorf("sessionstore: decode messages: %w", err)
	}
	return Session{
		ID:        id,
		Messages:  messages,
		UpdatedAt: updatedAt.UTC(),
	}, nil
}

func (s *sqlStore) Save(ctx context.Context, session Session) error {
	if session.ID == "" {
		return errors.New("sessionstore: session id is required")
	}
	updatedAt := session.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(session.Messages)
	if err != nil {
		return fmt.Errorf("sessionstore: encode messages: %w", err)
	}

	query := `
INSERT INTO support_sessions (session_id, messages_json, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
	messages_json = excluded.messages_json,
	updated_at = excluded.updated_at
`
	if s.dialect == dialectPostgres {
		query = `
INSERT INTO support_sessions (session_id, messages_json, updated_at)
VALUES ($1, $2, $3)
ON CONFLICT(session_id) DO UPDATE SET
	messages_json = excluded.messages_json,
	updated_at = excluded.updated_at
`
	}
	_, err = s.db.ExecContext(ctx, query, session.ID, string(payload), updatedAt)
	return err
}

func (s *sqlStore) Close() error {
	return s.db.Close()
}

// PingContext exposes the underlying *sql.DB.PingContext for readiness probes.
// Returns nil if the connection is alive. Callers detect this capability via
// a type assertion (sibling capability, not part of the Store interface — the
// exported Store contract is K-frozen).
func (s *sqlStore) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *sqlStore) ensureSchema(ctx context.Context) error {
	stmt := `
CREATE TABLE IF NOT EXISTS support_sessions (
	session_id TEXT PRIMARY KEY,
	messages_json TEXT NOT NULL,
	updated_at TIMESTAMP NOT NULL
)
`
	_, err := s.db.ExecContext(ctx, stmt)
	return err
}
