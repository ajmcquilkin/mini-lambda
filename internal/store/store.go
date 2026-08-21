// Package store defines the persistence interface for function configuration.
// This package is interface-only; a concrete SQLite-backed implementation is
// provided in a later round.
package store

import (
	"context"
	"errors"

	"github.com/ajmcquilkin/mini-lambda/internal/model"
)

// Sentinel errors returned by Store implementations. Callers should match with
// errors.Is.
var (
	// ErrNotFound is returned when a function does not exist.
	ErrNotFound = errors.New("store: function not found")
	// ErrConflict is returned when creating a function whose name already exists.
	ErrConflict = errors.New("store: function already exists")
)

// Store persists function configuration. Open and schema migration are the
// responsibility of the implementation's constructor; Migrate is exposed so a
// caller can (re-)run migrations explicitly.
type Store interface {
	// Migrate initializes/updates the schema to the latest version.
	Migrate(ctx context.Context) error

	// CreateFunction inserts a new function, returning ErrConflict if a function
	// with the same name already exists.
	CreateFunction(ctx context.Context, fn *model.Function) error

	// GetFunction returns the function by name, or ErrNotFound if absent.
	GetFunction(ctx context.Context, name string) (*model.Function, error)

	// ListFunctions returns all functions.
	ListFunctions(ctx context.Context) ([]*model.Function, error)

	// UpdateFunctionConfiguration persists configuration changes for an existing
	// function, returning ErrNotFound if it does not exist.
	UpdateFunctionConfiguration(ctx context.Context, fn *model.Function) error

	// DeleteFunction removes a function by name, returning ErrNotFound if absent.
	DeleteFunction(ctx context.Context, name string) error

	// Close releases underlying resources.
	Close() error
}
