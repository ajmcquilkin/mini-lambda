package daemon

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ajmcquilkin/mini-lambda/internal/runtime/docker"
)

// self is the ownership identity of the "current" daemon in these tests.
var self = ownerID{instance: "self-inst", host: "hostA", pid: 100}

// labelsFor builds a container label set from ownership pieces; empty values
// are omitted so tests can exercise missing labels.
func labelsFor(instance, host, pid string) map[string]string {
	m := map[string]string{docker.ManagedLabel: "true"}
	if instance != "" {
		m[docker.InstanceLabel] = instance
	}
	if host != "" {
		m[docker.OwnerHostLabel] = host
	}
	if pid != "" {
		m[docker.OwnerPIDLabel] = pid
	}
	return m
}

func TestClassifyContainer(t *testing.T) {
	// alive treats pid 100 (self) and 200 (a live peer) as running; everything
	// else is dead.
	alive := func(pid int) bool { return pid == 100 || pid == 200 }

	tests := []struct {
		name       string
		labels     map[string]string
		wantReap   bool
		wantReason string
	}{
		{
			name:       "own instance is never reaped",
			labels:     labelsFor("self-inst", "hostA", "100"),
			wantReap:   false,
			wantReason: "own instance",
		},
		{
			name:       "own instance kept even if its recorded pid looks dead",
			labels:     labelsFor("self-inst", "hostA", "999999"),
			wantReap:   false,
			wantReason: "own instance",
		},
		{
			name:       "live peer on same host is never reaped",
			labels:     labelsFor("peer-inst", "hostA", "200"),
			wantReap:   false,
			wantReason: "owner alive",
		},
		{
			name:       "dead owner on same host is reaped",
			labels:     labelsFor("dead-inst", "hostA", "300"),
			wantReap:   true,
			wantReason: "owner dead",
		},
		{
			name:       "different host is never reaped even if pid is dead",
			labels:     labelsFor("dead-inst", "hostB", "300"),
			wantReap:   false,
			wantReason: "different host",
		},
		{
			name:       "missing owner pid on our host is reaped as unattributable",
			labels:     labelsFor("legacy-inst", "hostA", ""),
			wantReap:   true,
			wantReason: "no owner pid",
		},
		{
			name:       "invalid owner pid is treated as no owner",
			labels:     labelsFor("legacy-inst", "hostA", "not-a-number"),
			wantReap:   true,
			wantReason: "no owner pid",
		},
		{
			name:       "no host label falls through to pid liveness (dead -> reap)",
			labels:     labelsFor("legacy-inst", "", "300"),
			wantReap:   true,
			wantReason: "owner dead",
		},
		{
			name:       "no host label, live pid -> kept",
			labels:     labelsFor("legacy-inst", "", "200"),
			wantReap:   false,
			wantReason: "owner alive",
		},
		{
			name:       "bare managed container (no ownership labels) on our host is reaped",
			labels:     labelsFor("", "", ""),
			wantReap:   true,
			wantReason: "no owner pid",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := classifyContainer(tt.labels, self, alive)
			assert.Equal(t, tt.wantReap, v.reap)
			assert.Equal(t, tt.wantReason, v.reason)
		})
	}
}

func TestOwnerIDLabelsRoundTrip(t *testing.T) {
	o := ownerID{instance: "abc", host: "hostZ", pid: 4242}
	labels := o.labels()

	assert.Equal(t, "abc", labels[docker.InstanceLabel])
	assert.Equal(t, "hostZ", labels[docker.OwnerHostLabel])
	assert.Equal(t, "4242", labels[docker.OwnerPIDLabel])

	// The labels a daemon stamps must classify as "own instance" for that same
	// daemon, guaranteeing a daemon never reaps its own live containers.
	v := classifyContainer(labels, o, func(int) bool { return false })
	assert.False(t, v.reap)
	assert.Equal(t, "own instance", v.reason)
}

// fakeReaper is a hand-rolled managedReaper: it serves a canned container list
// and records every id passed to Remove.
type fakeReaper struct {
	list      []docker.ManagedContainer
	listErr   error
	removeErr map[string]error

	removed []string
}

func (f *fakeReaper) ListManaged(context.Context) ([]docker.ManagedContainer, error) {
	return f.list, f.listErr
}

func (f *fakeReaper) Remove(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	if f.removeErr != nil {
		return f.removeErr[id]
	}
	return nil
}

func TestReapOrphansRemovesOnlyDeadOwners(t *testing.T) {
	alive := func(pid int) bool { return pid == 100 || pid == 200 }
	f := &fakeReaper{list: []docker.ManagedContainer{
		{ID: "self-container", Labels: labelsFor("self-inst", "hostA", "100")},
		{ID: "live-peer", Labels: labelsFor("peer-inst", "hostA", "200")},
		{ID: "dead-owner-1", Labels: labelsFor("dead-a", "hostA", "300")},
		{ID: "dead-owner-2", Labels: labelsFor("dead-b", "hostA", "400")},
		{ID: "other-host", Labels: labelsFor("dead-c", "hostB", "300")},
	}}

	n := reapOrphans(t.Context(), f, self, alive, func(string, ...any) {})

	assert.Equal(t, 2, n)
	sort.Strings(f.removed)
	assert.Equal(t, []string{"dead-owner-1", "dead-owner-2"}, f.removed)
}

func TestReapOrphansListErrorIsBestEffort(t *testing.T) {
	f := &fakeReaper{listErr: errors.New("docker down")}

	n := reapOrphans(t.Context(), f, self, func(int) bool { return false }, func(string, ...any) {})

	assert.Equal(t, 0, n)
	assert.Empty(t, f.removed, "nothing removed when listing fails")
}

func TestReapOrphansRemoveErrorDoesNotCountOrAbort(t *testing.T) {
	alive := func(int) bool { return false }
	f := &fakeReaper{
		list: []docker.ManagedContainer{
			{ID: "dead-1", Labels: labelsFor("d1", "hostA", "300")},
			{ID: "dead-2", Labels: labelsFor("d2", "hostA", "301")},
		},
		removeErr: map[string]error{"dead-1": errors.New("boom")},
	}

	n := reapOrphans(t.Context(), f, self, alive, func(string, ...any) {})

	// dead-1's removal failed (not counted) but dead-2 still got reaped.
	assert.Equal(t, 1, n)
	require.Contains(t, f.removed, "dead-1")
	require.Contains(t, f.removed, "dead-2")
}

func TestPidAlive(t *testing.T) {
	// The current test process is definitely alive.
	assert.True(t, pidAlive(os.Getpid()))
	// Non-positive pids are never alive.
	assert.False(t, pidAlive(0))
	assert.False(t, pidAlive(-1))
}
