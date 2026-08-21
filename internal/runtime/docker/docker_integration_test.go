package docker

import (
	"bufio"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/runtime"
)

const (
	dockerSocket = "/var/run/docker.sock"
	testImage    = "alpine:latest"
)

// skipWithoutDocker skips the test unless a docker daemon socket is present,
// so the integration suite runs automatically only where a daemon is available.
func skipWithoutDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}
	if _, err := os.Stat(dockerSocket); err != nil {
		t.Skipf("skipping: docker daemon unavailable (%s not found and DOCKER_HOST unset)", dockerSocket)
	}
}

func TestIntegrationLifecycle(t *testing.T) {
	skipWithoutDocker(t)

	rt, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	if err := rt.Pull(ctx, testImage); err != nil {
		t.Fatalf("Pull(%q): %v", testImage, err)
	}

	// Pull again to exercise the local-image fast path.
	if err := rt.Pull(ctx, testImage); err != nil {
		t.Fatalf("Pull(%q) second time: %v", testImage, err)
	}

	spec := runtime.ContainerSpec{
		Image:    testImage,
		Env:      map[string]string{"FOO": "bar"},
		MemoryMB: 64,
		RuntimeAPIEnv: map[string]string{
			"AWS_LAMBDA_RUNTIME_API": "127.0.0.1:9001",
		},
	}

	id, err := rt.Create(ctx, spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Logf("created container %s", id)

	defer func() {
		if err := rt.Remove(t.Context(), id); err != nil {
			t.Errorf("Remove: %v", err)
		}
	}()

	if err := rt.Start(ctx, id); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give it a moment; alpine with default cmd exits quickly.
	rc, err := rt.Logs(ctx, id, false)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	// Drain logs to ensure the demultiplexed pipe works end to end.
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		_ = sc.Text()
	}
	rc.Close()
	if err := sc.Err(); err != nil {
		t.Errorf("scanning logs: %v", err)
	}

	if err := rt.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestIntegrationNotFoundMapping(t *testing.T) {
	skipWithoutDocker(t)

	rt, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	const missing = "mini-lambda-nonexistent-container-id"
	err = rt.Stop(ctx, missing)
	if err == nil {
		t.Fatal("Stop(missing) = nil, want error")
	}
	if !strings.Contains(err.Error(), ErrContainerNotFound.Error()) {
		t.Errorf("Stop(missing) error = %v, want it to wrap ErrContainerNotFound", err)
	}
}
