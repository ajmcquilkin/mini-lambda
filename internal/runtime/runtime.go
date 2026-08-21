// Package runtime defines the container-runtime interface used to pull images
// and manage per-invocation containers. This package is interface-only; a
// concrete Docker-backed implementation is provided in a later round.
package runtime

import (
	"context"
	"io"
)

// ContainerSpec describes a container to create for a function invocation.
type ContainerSpec struct {
	// Image is the OCI image reference to run.
	Image string
	// Env is the function's configured environment.
	Env map[string]string
	// MemoryMB is the memory limit in megabytes.
	MemoryMB int

	// RuntimeAPIEnv carries the environment variables the emulator injects so the
	// container's runtime bootstrap can reach the local Lambda Runtime API — e.g.
	// AWS_LAMBDA_RUNTIME_API (host:port) and any per-slot token. Keeping this as a
	// single map lets the scheduler decide the exact variable set without widening
	// this struct each time.
	RuntimeAPIEnv map[string]string
}

// Runtime manages images and containers for function invocations.
type Runtime interface {
	// Pull ensures the image is present locally.
	Pull(ctx context.Context, image string) error

	// Create creates a container from spec and returns its id.
	Create(ctx context.Context, spec ContainerSpec) (string, error)

	// Start starts a previously created container.
	Start(ctx context.Context, id string) error

	// Stop stops a running container.
	Stop(ctx context.Context, id string) error

	// Remove deletes a container.
	Remove(ctx context.Context, id string) error

	// Logs returns the container's log stream. When follow is true the stream
	// stays open and yields new output until the container exits or the reader is
	// closed.
	Logs(ctx context.Context, id string, follow bool) (io.ReadCloser, error)
}
