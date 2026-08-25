// Package runtime defines the container-runtime interface used to pull images
// and manage per-invocation containers. This package is interface-only; a
// concrete Docker-backed implementation is provided in a later round.
package runtime

import (
	"context"
	"io"
)

//go:generate mockgen -destination=runtimemock/mock_runtime.go -package=runtimemock github.com/ajmcquilkin/mini-lambda/internal/runtime Runtime

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

	// ExtraHosts adds "hostname:IP" entries to the container's /etc/hosts (the
	// docker HostConfig.ExtraHosts field). The emulator uses this to inject
	// "host.docker.internal:host-gateway" so the container can reach the
	// daemon's Runtime API listener on the host.
	ExtraHosts []string

	// Labels are extra container labels the runtime stamps alongside its own
	// managed label. The scheduler uses these to record per-daemon ownership
	// (instance id, owner pid/host) so a later daemon's startup reaper can
	// identify and remove containers whose owning daemon has died.
	Labels map[string]string
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
