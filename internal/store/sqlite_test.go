package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	if got.LastInvokedAt != nil {
		t.Errorf("expected nil LastInvokedAt, got %v", got.LastInvokedAt)
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

	invoked := time.Now().UTC().Truncate(time.Second)
	fn.Image = "example/upd:v2"
	fn.MemoryMB = 512
	fn.TimeoutSec = 60
	fn.Env = map[string]string{"ONLY": "one"}
	fn.LastInvokedAt = &invoked

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
	if got.LastInvokedAt == nil || !got.LastInvokedAt.Equal(invoked) {
		t.Errorf("lastInvokedAt mismatch: got %v want %v", got.LastInvokedAt, invoked)
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
