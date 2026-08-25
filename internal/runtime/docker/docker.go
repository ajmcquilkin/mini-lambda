// Package docker provides a Docker Engine API-backed implementation of the
// runtime.Runtime interface. It manages images and per-invocation containers
// via the moby client (github.com/docker/docker/client).
//
// Only public registries are supported (no registry auth plumbing) and local
// images are first-class: Pull is a no-op when the image already exists locally.
// Invocation traffic flows through the Lambda Runtime API rather than inbound
// HTTP, so containers are created without any published ports.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/ajmcquilkin/mini-lambda/internal/runtime"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// ErrContainerNotFound is returned by container operations when the target
// container does not exist. It wraps the underlying docker "not found" error so
// callers can match on a stable package-level sentinel without importing docker
// error types.
var ErrContainerNotFound = errors.New("docker: container not found")

// ErrDockerUnavailable is returned when the docker daemon cannot be reached
// (e.g. Docker Desktop is not running, or it listens on a socket we did not
// probe). It wraps the underlying connection error so callers can match on this
// stable sentinel via errors.Is and surface an actionable message.
var ErrDockerUnavailable = errors.New("cannot reach the docker daemon")

// stdDockerSocket is the conventional daemon socket most Linux installs and
// older Docker Desktop builds expose (or symlink to).
const stdDockerSocket = "/var/run/docker.sock"

// desktopSocketRel is Docker Desktop's macOS per-user socket, relative to $HOME.
// Recent installs create this instead of symlinking stdDockerSocket, so a bare
// client.FromEnv (which defaults to stdDockerSocket) probes the wrong endpoint.
const desktopSocketRel = "/.docker/run/docker.sock"

// Container ownership labels. ManagedLabel marks every container this runtime
// creates so they can be discovered and cleaned up later; the remaining labels
// record which daemon run owns a container so a later daemon's startup reaper
// can tell a dead owner's orphans (safe to remove) from a live peer's
// containers (must never be touched). The scheduler supplies the owner-* values
// via ContainerSpec.Labels; ManagedLabel is always stamped by Create.
const (
	ManagedLabel   = "mini-lambda.managed"
	InstanceLabel  = "mini-lambda.instance"
	OwnerPIDLabel  = "mini-lambda.owner-pid"
	OwnerHostLabel = "mini-lambda.owner-host"
)

// ManagedContainer is a container carrying ManagedLabel, reported by
// ListManaged with the labels the reaper needs to determine ownership.
type ManagedContainer struct {
	ID     string
	Labels map[string]string
}

// Runtime implements runtime.Runtime using the Docker Engine API.
type Runtime struct {
	cli *client.Client
}

// Compile-time assertion that Runtime satisfies the frozen interface.
var _ runtime.Runtime = (*Runtime)(nil)

// New constructs a Runtime, resolving the docker endpoint via resolveDockerHost
// (DOCKER_HOST, then the standard socket, then Docker Desktop's per-user socket)
// with automatic API version negotiation.
func New() (*Runtime, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host := resolveDockerHost(os.Getenv, fileExists); host != "" {
		opts = append(opts, client.WithHost(host))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: new client: %w", err)
	}
	return &Runtime{cli: cli}, nil
}

// resolveDockerHost selects the docker endpoint to connect to, probing in order:
//
//  1. DOCKER_HOST from the environment (honored as-is, unchanged behavior).
//  2. The standard socket /var/run/docker.sock.
//  3. Docker Desktop's macOS per-user socket $HOME/.docker/run/docker.sock,
//     which recent installs create instead of symlinking the standard socket.
//
// If none resolve it returns "" so the caller falls back to the SDK default. It
// is pure: env and statExists are injected so it is table-testable without a
// real environment or filesystem.
func resolveDockerHost(env func(string) string, statExists func(string) bool) string {
	if h := env("DOCKER_HOST"); h != "" {
		return h
	}
	if statExists(stdDockerSocket) {
		return "unix://" + stdDockerSocket
	}
	if home := env("HOME"); home != "" {
		if desktop := home + desktopSocketRel; statExists(desktop) {
			return "unix://" + desktop
		}
	}
	return ""
}

// fileExists reports whether path exists (a socket counts as existing).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Ping verifies the docker daemon is reachable. A connection failure maps to
// ErrDockerUnavailable (with the resolved endpoint and probed candidates) so the
// daemon can log an actionable startup warning.
func (r *Runtime) Ping(ctx context.Context) error {
	if _, err := r.cli.Ping(ctx); err != nil {
		return r.wrap("ping", err)
	}
	return nil
}

// Endpoint reports the docker host the client is configured to use.
func (r *Runtime) Endpoint() string {
	return r.cli.DaemonHost()
}

// Pull ensures the image is present locally. If the image already exists it
// returns immediately; otherwise it pulls from the registry and drains the
// progress stream so the pull completes before returning.
func (r *Runtime) Pull(ctx context.Context, imageRef string) error {
	if _, err := r.cli.ImageInspect(ctx, imageRef); err == nil {
		return nil
	} else if !client.IsErrNotFound(err) {
		return r.wrap(fmt.Sprintf("inspect image %q", imageRef), err)
	}

	rc, err := r.cli.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return r.wrap(fmt.Sprintf("pull image %q", imageRef), err)
	}
	defer rc.Close()

	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker: drain pull stream for %q: %w", imageRef, err)
	}
	return nil
}

// Create creates a container from spec and returns its id. Env is spec.Env
// merged with spec.RuntimeAPIEnv, with RuntimeAPIEnv taking precedence on key
// collisions. The memory limit is applied in bytes; no ports are published.
func (r *Runtime) Create(ctx context.Context, spec runtime.ContainerSpec) (string, error) {
	config := &container.Config{
		Image:  spec.Image,
		Env:    mergeEnv(spec.Env, spec.RuntimeAPIEnv),
		Labels: managedLabels(spec.Labels),
	}

	hostConfig := &container.HostConfig{
		ExtraHosts: spec.ExtraHosts,
		Resources: container.Resources{
			Memory: memoryBytes(spec.MemoryMB),
		},
	}

	resp, err := r.cli.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
	if err != nil {
		return "", r.wrap(fmt.Sprintf("create container from %q", spec.Image), err)
	}
	return resp.ID, nil
}

// ListManaged returns every container carrying ManagedLabel (running or not,
// across all daemon runs on this docker host), with the labels needed to
// determine ownership. It is the "list managed containers by label" capability
// the startup reaper needs; it lives on the concrete *Runtime rather than the
// frozen runtime.Runtime interface, mirroring how Ping/Endpoint are kept off
// that interface.
func (r *Runtime) ListManaged(ctx context.Context) ([]ManagedContainer, error) {
	f := filters.NewArgs()
	f.Add("label", ManagedLabel+"=true")
	list, err := r.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, r.wrap("list managed containers", err)
	}
	out := make([]ManagedContainer, 0, len(list))
	for _, c := range list {
		out = append(out, ManagedContainer{ID: c.ID, Labels: c.Labels})
	}
	return out, nil
}

// Start starts a previously created container.
func (r *Runtime) Start(ctx context.Context, id string) error {
	if err := r.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return r.wrap(fmt.Sprintf("start container %q", id), err)
	}
	return nil
}

// Stop stops a running container, giving it a short grace period before the
// daemon forcibly kills it.
func (r *Runtime) Stop(ctx context.Context, id string) error {
	timeout := stopTimeoutSeconds
	if err := r.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		return r.wrap(fmt.Sprintf("stop container %q", id), err)
	}
	return nil
}

// Remove force-deletes a container.
func (r *Runtime) Remove(ctx context.Context, id string) error {
	if err := r.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		return r.wrap(fmt.Sprintf("remove container %q", id), err)
	}
	return nil
}

// Logs returns the container's combined stdout+stderr stream. The multiplexed
// docker stream is demultiplexed with stdcopy into a single pipe. When follow
// is true the stream stays open until the container exits or the reader closes.
func (r *Runtime) Logs(ctx context.Context, id string, follow bool) (io.ReadCloser, error) {
	rc, err := r.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Timestamps: false,
	})
	if err != nil {
		return nil, r.wrap(fmt.Sprintf("logs for container %q", id), err)
	}

	pr, pw := io.Pipe()
	go func() {
		_, copyErr := stdcopy.StdCopy(pw, pw, rc)
		rc.Close()
		pw.CloseWithError(copyErr)
	}()
	return pr, nil
}

// stopTimeoutSeconds is the grace period given to a container to stop before it
// is forcibly killed.
const stopTimeoutSeconds = 5

// memoryBytes converts a megabyte limit to bytes. A non-positive value yields 0
// (unlimited), matching docker's semantics.
func memoryBytes(memoryMB int) int64 {
	if memoryMB <= 0 {
		return 0
	}
	return int64(memoryMB) * 1024 * 1024
}

// managedLabels builds a container's label set: ManagedLabel is always
// present, plus any caller-supplied ownership labels. Callers cannot override
// ManagedLabel, so ListManaged always finds every container Create made.
func managedLabels(extra map[string]string) map[string]string {
	labels := make(map[string]string, len(extra)+1)
	for k, v := range extra {
		labels[k] = v
	}
	labels[ManagedLabel] = "true"
	return labels
}

// mergeEnv merges base env with overrides (overrides win on collision) and
// returns a deterministically ordered "KEY=VALUE" slice for docker.
func mergeEnv(base, overrides map[string]string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(merged))
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out
}

// wrap classifies a docker client error for op using this runtime's resolved
// endpoint.
func (r *Runtime) wrap(op string, err error) error {
	return wrapDockerErr(op, r.cli.DaemonHost(), err)
}

// wrapDockerErr maps a docker client error to a stable sentinel where it helps
// callers, preserving the underlying error for errors.Is/As in every case:
//
//   - not-found        -> ErrContainerNotFound, with op context.
//   - connection-failed -> ErrDockerUnavailable, with the endpoint in use and an
//     "is Docker running?" hint (no op prefix, so the actionable message
//     surfaces cleanly even through outer wrap chains).
//   - anything else     -> op context only.
//
// It is a pure function (host injected) so it is table-testable.
func wrapDockerErr(op, host string, err error) error {
	switch {
	case client.IsErrNotFound(err):
		return fmt.Errorf("docker: %s: %w: %w", op, ErrContainerNotFound, err)
	case client.IsErrConnectionFailed(err):
		return fmt.Errorf("%w at %s — is Docker running?: %w", ErrDockerUnavailable, host, err)
	default:
		return fmt.Errorf("docker: %s: %w", op, err)
	}
}
