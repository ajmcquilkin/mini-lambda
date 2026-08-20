//go:build tools

// Package deps pre-stages third-party modules that later rounds implement
// against, so they are pinned in go.mod / go.sum now and parallel agents don't
// each mutate dependency metadata and collide.
//
// This file never compiles into the binary: the "tools" build tag excludes it
// from normal builds. It exists only so `go mod tidy` retains these modules.
package deps

import (
	// Pure-Go SQLite driver (no cgo) for internal/store.
	_ "modernc.org/sqlite"

	// Schema migrations for internal/store.
	_ "github.com/golang-migrate/migrate/v4"

	// Docker Engine API client for internal/runtime. We use the canonical
	// github.com/docker/docker/client module (moby's Go client); it builds
	// cleanly under Bazel/gazelle.
	_ "github.com/docker/docker/client"

	// UUID generation, imported directly by internal/model.
	_ "github.com/google/uuid"
)
