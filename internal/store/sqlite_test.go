package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ajmcquilkin/mini-lambda/internal/model"
)

func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func sampleFn(name string) *model.Function {
	now := time.Now().UTC().Truncate(time.Second)
	return &model.Function{
		Name:       name,
		Image:      "example/" + name + ":latest",
		Env:        map[string]string{"FOO": "bar", "BAZ": "qux"},
		MemoryMB:   256,
		TimeoutSec: 30,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestMigrateIdempotent(t *testing.T) {
	s := newTestStore(t)
	// Migrate again; ErrNoChange must be swallowed.
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("third Migrate: %v", err)
	}
}

// TestMigrateUpgradesLegacyLastInvokedAtSchema builds a database in the 0001
// ("last_invoked_at" present) shape, stamped at migration version 1, then runs
// Migrate. 0002 must drop the column cleanly while preserving existing rows.
func TestMigrateUpgradesLegacyLastInvokedAtSchema(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Reconstruct the 0001 schema by hand, including the dropped column.
	_, err = s.db.ExecContext(ctx, `CREATE TABLE functions (
		name            TEXT    PRIMARY KEY,
		image           TEXT    NOT NULL,
		env             TEXT    NOT NULL,
		memory_mb       INTEGER NOT NULL,
		timeout_sec     INTEGER NOT NULL,
		created_at      TEXT    NOT NULL,
		updated_at      TEXT    NOT NULL,
		last_invoked_at TEXT
	)`)
	require.NoError(t, err)

	// Stamp golang-migrate's version table so Migrate sees a v1 database and
	// applies only 0002.
	_, err = s.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version uint64, dirty bool)`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO schema_migrations (version, dirty) VALUES (1, 0)`)
	require.NoError(t, err)

	// Seed a legacy row (with a last_invoked_at value) that must survive the drop.
	_, err = s.db.ExecContext(ctx, `INSERT INTO functions
		(name, image, env, memory_mb, timeout_sec, created_at, updated_at, last_invoked_at)
		VALUES ('legacy', 'img:1', '{}', 128, 30,
		        '2024-01-01T00:00:00Z', '2024-01-01T00:00:00Z', '2024-01-02T00:00:00Z')`)
	require.NoError(t, err)

	require.NoError(t, s.Migrate(ctx))

	// The column is gone.
	var cols int
	require.NoError(t, s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('functions') WHERE name = 'last_invoked_at'`).Scan(&cols))
	assert.Zero(t, cols, "last_invoked_at column should be dropped")

	// Pre-existing data still reads back through the store.
	got, err := s.GetFunction(ctx, "legacy")
	require.NoError(t, err)
	assert.Equal(t, "img:1", got.Image)
	assert.Equal(t, 128, got.MemoryMB)

	// The store is fully functional post-migration (insert path has no column).
	require.NoError(t, s.CreateFunction(ctx, sampleFn("fresh")))
	fresh, err := s.GetFunction(ctx, "fresh")
	require.NoError(t, err)
	assert.Equal(t, "example/fresh:latest", fresh.Image)
}

func TestCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	want := sampleFn("alpha")

	if err := s.CreateFunction(ctx, want); err != nil {
		t.Fatalf("CreateFunction: %v", err)
	}

	got, err := s.GetFunction(ctx, "alpha")
	if err != nil {
		t.Fatalf("GetFunction: %v", err)
	}
	if got.Name != want.Name || got.Image != want.Image {
		t.Errorf("name/image mismatch: got %+v want %+v", got, want)
	}
	if got.MemoryMB != want.MemoryMB || got.TimeoutSec != want.TimeoutSec {
		t.Errorf("mem/timeout mismatch: got %+v want %+v", got, want)
	}
	if len(got.Env) != 2 || got.Env["FOO"] != "bar" || got.Env["BAZ"] != "qux" {
		t.Errorf("env mismatch: got %v", got.Env)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("createdAt mismatch: got %v want %v", got.CreatedAt, want.CreatedAt)
	}
}

func TestCreateConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	fn := sampleFn("dup")
	if err := s.CreateFunction(ctx, fn); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := s.CreateFunction(ctx, sampleFn("dup"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetFunction(t.Context(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestList(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	empty, err := s.ListFunctions(ctx)
	if err != nil {
		t.Fatalf("ListFunctions empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected empty list, got %d", len(empty))
	}

	for _, n := range []string{"gamma", "alpha", "beta"} {
		if err := s.CreateFunction(ctx, sampleFn(n)); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}

	got, err := s.ListFunctions(ctx)
	if err != nil {
		t.Fatalf("ListFunctions: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d want %d", len(got), len(want))
	}
	for i, fn := range got {
		if fn.Name != want[i] {
			t.Errorf("order mismatch at %d: got %s want %s", i, fn.Name, want[i])
		}
	}
}

func TestUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	fn := sampleFn("upd")
	if err := s.CreateFunction(ctx, fn); err != nil {
		t.Fatalf("create: %v", err)
	}

	origUpdated := fn.UpdatedAt
	time.Sleep(5 * time.Millisecond)

	fn.Image = "example/upd:v2"
	fn.MemoryMB = 512
	fn.TimeoutSec = 60
	fn.Env = map[string]string{"ONLY": "one"}

	if err := s.UpdateFunctionConfiguration(ctx, fn); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !fn.UpdatedAt.After(origUpdated) {
		t.Errorf("expected updated_at bumped: orig %v new %v", origUpdated, fn.UpdatedAt)
	}

	got, err := s.GetFunction(ctx, "upd")
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Image != "example/upd:v2" || got.MemoryMB != 512 || got.TimeoutSec != 60 {
		t.Errorf("update not persisted: %+v", got)
	}
	if len(got.Env) != 1 || got.Env["ONLY"] != "one" {
		t.Errorf("env not updated: %v", got.Env)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateFunctionConfiguration(t.Context(), sampleFn("ghost"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	if err := s.CreateFunction(ctx, sampleFn("del")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DeleteFunction(ctx, "del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetFunction(ctx, "del"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDeleteNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.DeleteFunction(t.Context(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestEmptyEnvRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	fn := sampleFn("noenv")
	fn.Env = nil
	if err := s.CreateFunction(ctx, fn); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetFunction(ctx, "noenv")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Env) != 0 {
		t.Errorf("expected empty env, got %v", got.Env)
	}
}
