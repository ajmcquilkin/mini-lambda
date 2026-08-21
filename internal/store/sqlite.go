package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	"github.com/ajmcquilkin/mini-lambda/internal/model"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// timeFormat is the canonical on-disk timestamp encoding. All time columns are
// stored as RFC3339 (nanosecond precision) UTC strings.
const timeFormat = time.RFC3339Nano

// SQLite is a Store backed by a single SQLite database file, using the pure-Go
// modernc.org/sqlite driver (no cgo). The daemon is the sole writer, so WAL and
// a busy timeout are enabled via DSN pragmas to keep single-writer access
// robust without additional locking.
type SQLite struct {
	db   *sql.DB
	path string
}

var _ Store = (*SQLite)(nil)

// Open opens (creating if necessary) the SQLite database at path and returns a
// ready-to-use *SQLite. WAL journaling, a busy timeout, and foreign keys are
// enabled via DSN pragmas. Call Migrate to initialize the schema and Close to
// release resources.
func Open(path string) (*SQLite, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}

	// modernc.org/sqlite accepts PRAGMA statements via the _pragma query
	// parameter. WAL + a generous busy timeout make the single-writer daemon
	// resilient to transient locks; foreign_keys is on for correctness.
	dsn := path + "?" + url.Values{
		"_pragma": []string{
			"journal_mode(WAL)",
			"busy_timeout(5000)",
			"foreign_keys(ON)",
		},
	}.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}

	// A single connection avoids WAL/locking surprises for the lone writer and
	// keeps in-memory databases (":memory:") coherent across calls.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping sqlite: %w", err)
	}

	return &SQLite{db: db, path: path}, nil
}

// Migrate applies all pending schema migrations. It is idempotent: running it
// against an already-current database is a no-op.
func (s *SQLite) Migrate(ctx context.Context) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: load migrations: %w", err)
	}

	driver, err := migratesqlite.WithInstance(s.db, &migratesqlite.Config{})
	if err != nil {
		return fmt.Errorf("store: migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return fmt.Errorf("store: migrate init: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("store: migrate up: %w", err)
	}
	return nil
}

// CreateFunction inserts fn, returning ErrConflict if a function with the same
// name already exists.
func (s *SQLite) CreateFunction(ctx context.Context, fn *model.Function) error {
	env, err := encodeEnv(fn.Env)
	if err != nil {
		return err
	}

	const q = `INSERT INTO functions
		(name, image, env, memory_mb, timeout_sec, created_at, updated_at, last_invoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	_, err = s.db.ExecContext(ctx, q,
		fn.Name,
		fn.Image,
		env,
		fn.MemoryMB,
		fn.TimeoutSec,
		fn.CreatedAt.UTC().Format(timeFormat),
		fn.UpdatedAt.UTC().Format(timeFormat),
		nullableTime(fn.LastInvokedAt),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return fmt.Errorf("store: create function: %w", err)
	}
	return nil
}

// GetFunction returns the function named name, or ErrNotFound.
func (s *SQLite) GetFunction(ctx context.Context, name string) (*model.Function, error) {
	const q = `SELECT name, image, env, memory_mb, timeout_sec, created_at, updated_at, last_invoked_at
		FROM functions WHERE name = ?`

	fn, err := scanFunction(s.db.QueryRowContext(ctx, q, name))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: get function: %w", err)
	}
	return fn, nil
}

// ListFunctions returns all functions ordered by name.
func (s *SQLite) ListFunctions(ctx context.Context) ([]*model.Function, error) {
	const q = `SELECT name, image, env, memory_mb, timeout_sec, created_at, updated_at, last_invoked_at
		FROM functions ORDER BY name`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list functions: %w", err)
	}
	defer rows.Close()

	out := make([]*model.Function, 0)
	for rows.Next() {
		fn, err := scanFunction(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan function: %w", err)
		}
		out = append(out, fn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list functions: %w", err)
	}
	return out, nil
}

// UpdateFunctionConfiguration persists configuration changes for an existing
// function, returning ErrNotFound if it does not exist. updated_at is bumped to
// now (UTC) regardless of the value carried on fn.
func (s *SQLite) UpdateFunctionConfiguration(ctx context.Context, fn *model.Function) error {
	env, err := encodeEnv(fn.Env)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	const q = `UPDATE functions SET
		image = ?, env = ?, memory_mb = ?, timeout_sec = ?, updated_at = ?, last_invoked_at = ?
		WHERE name = ?`

	res, err := s.db.ExecContext(ctx, q,
		fn.Image,
		env,
		fn.MemoryMB,
		fn.TimeoutSec,
		now.Format(timeFormat),
		nullableTime(fn.LastInvokedAt),
		fn.Name,
	)
	if err != nil {
		return fmt.Errorf("store: update function: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update function rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}

	fn.UpdatedAt = now
	return nil
}

// DeleteFunction removes the function named name, returning ErrNotFound if
// absent.
func (s *SQLite) DeleteFunction(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM functions WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("store: delete function: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete function rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Close releases the underlying database handle.
func (s *SQLite) Close() error {
	return s.db.Close()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanFunction(r rowScanner) (*model.Function, error) {
	var (
		fn      model.Function
		env     string
		created string
		updated string
		invoked sql.NullString
	)
	if err := r.Scan(&fn.Name, &fn.Image, &env, &fn.MemoryMB, &fn.TimeoutSec, &created, &updated, &invoked); err != nil {
		return nil, err
	}

	if err := decodeEnv(env, &fn.Env); err != nil {
		return nil, err
	}

	var err error
	if fn.CreatedAt, err = parseTime(created); err != nil {
		return nil, fmt.Errorf("store: parse created_at: %w", err)
	}
	if fn.UpdatedAt, err = parseTime(updated); err != nil {
		return nil, fmt.Errorf("store: parse updated_at: %w", err)
	}
	if invoked.Valid {
		t, err := parseTime(invoked.String)
		if err != nil {
			return nil, fmt.Errorf("store: parse last_invoked_at: %w", err)
		}
		fn.LastInvokedAt = &t
	}
	return &fn, nil
}

func encodeEnv(env map[string]string) (string, error) {
	if env == nil {
		return "{}", nil
	}
	b, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("store: encode env: %w", err)
	}
	return string(b), nil
}

func decodeEnv(s string, dst *map[string]string) error {
	if s == "" || s == "{}" {
		return nil
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return fmt.Errorf("store: decode env: %w", err)
	}
	if len(m) > 0 {
		*dst = m
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(timeFormat)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeFormat, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func isUniqueViolation(err error) bool {
	var serr *sqlite.Error
	if errors.As(err, &serr) {
		code := serr.Code()
		return code == sqlitelib.SQLITE_CONSTRAINT_UNIQUE ||
			code == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY ||
			code == sqlitelib.SQLITE_CONSTRAINT
	}
	return false
}
