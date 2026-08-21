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
	"sort"

	"github.com/ajmcquilkin/mini-lambda/internal/runtime"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// ErrContainerNotFound is returned by container operations when the target
// container does not exist. It wraps the underlying docker "not found" error so
// callers can match on a stable package-level sentinel without importing docker
// error types.
var ErrContainerNotFound = errors.New("docker: container not found")

// ManagedLabel marks containers created by this runtime so they can be
// discovered and cleaned up later.
const ManagedLabel = "mini-lambda.managed"

// Runtime implements runtime.Runtime using the Docker Engine API.
type Runtime struct {
	cli *client.Client
}

// Compile-time assertion that Runtime satisfies the frozen interface.
var _ runtime.Runtime = (*Runtime)(nil)

// New constructs a Runtime using the ambient docker environment
// (DOCKER_HOST, etc.) with automatic API version negotiation.
func New() (*Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker: new client: %w", err)
	}
	return &Runtime{cli: cli}, nil
}

// Pull ensures the image is present locally. If the image already exists it
// returns immediately; otherwise it pulls from the registry and drains the
// progress stream so the pull completes before returning.
func (r *Runtime) Pull(ctx context.Context, imageRef string) error {
	if _, err := r.cli.ImageInspect(ctx, imageRef); err == nil {
		return nil
	} else if !client.IsErrNotFound(err) {
		return fmt.Errorf("docker: inspect image %q: %w", imageRef, err)
	}

	rc, err := r.cli.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull image %q: %w", imageRef, err)
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
		Image: spec.Image,
		Env:   mergeEnv(spec.Env, spec.RuntimeAPIEnv),
		Labels: map[string]string{
			ManagedLabel: "true",
		},
	}

	hostConfig := &container.HostConfig{
		ExtraHosts: spec.ExtraHosts,
		Resources: container.Resources{
			Memory: memoryBytes(spec.MemoryMB),
		},
	}

	resp, err := r.cli.ContainerCreate(ctx, config, hostConfig, nil, nil, "")
	if err != nil {
		return "", fmt.Errorf("docker: create container from %q: %w", spec.Image, err)
	}
	return resp.ID, nil
}

// Start starts a previously created container.
func (r *Runtime) Start(ctx context.Context, id string) error {
	if err := r.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return wrapNotFound(fmt.Sprintf("start container %q", id), err)
	}
	return nil
}

// Stop stops a running container, giving it a short grace period before the
// daemon forcibly kills it.
func (r *Runtime) Stop(ctx context.Context, id string) error {
	timeout := stopTimeoutSeconds
	if err := r.cli.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeout}); err != nil {
		return wrapNotFound(fmt.Sprintf("stop container %q", id), err)
	}
	return nil
}

// Remove force-deletes a container.
func (r *Runtime) Remove(ctx context.Context, id string) error {
	if err := r.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		return wrapNotFound(fmt.Sprintf("remove container %q", id), err)
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
		return nil, wrapNotFound(fmt.Sprintf("logs for container %q", id), err)
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

// wrapNotFound maps docker "not found" errors to ErrContainerNotFound and wraps
// everything else with context. The returned error still unwraps to the docker
// error for callers that want detail.
func wrapNotFound(op string, err error) error {
	if client.IsErrNotFound(err) {
		return fmt.Errorf("docker: %s: %w: %w", op, ErrContainerNotFound, err)
	}
	return fmt.Errorf("docker: %s: %w", op, err)
}
