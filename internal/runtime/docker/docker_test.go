package docker

import (
	"reflect"
	"testing"

	"github.com/ajmcquilkin/mini-lambda/internal/runtime"
)

// Belt-and-suspenders alongside the assertion in docker.go: the concrete type
// must satisfy the frozen runtime.Runtime interface.
var _ runtime.Runtime = (*Runtime)(nil)

func TestMergeEnvPrecedence(t *testing.T) {
	base := map[string]string{
		"SHARED":    "from-base",
		"ONLY_BASE": "base",
	}
	overrides := map[string]string{
		"SHARED":                 "from-runtime",
		"AWS_LAMBDA_RUNTIME_API": "127.0.0.1:9001",
	}

	got := mergeEnv(base, overrides)

	// Output is sorted "KEY=VALUE" for deterministic behavior.
	want := []string{
		"AWS_LAMBDA_RUNTIME_API=127.0.0.1:9001",
		"ONLY_BASE=base",
		"SHARED=from-runtime",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeEnv precedence/order wrong:\n got=%v\nwant=%v", got, want)
	}
}

func TestMergeEnvNilMaps(t *testing.T) {
	if got := mergeEnv(nil, nil); len(got) != 0 {
		t.Fatalf("mergeEnv(nil,nil) = %v, want empty", got)
	}

	got := mergeEnv(nil, map[string]string{"A": "1"})
	want := []string{"A=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeEnv(nil, override) = %v, want %v", got, want)
	}
}

func TestManagedLabels(t *testing.T) {
	// ManagedLabel is always present so ListManaged finds every created container.
	got := managedLabels(nil)
	if got[ManagedLabel] != "true" {
		t.Fatalf("managedLabels(nil)[%q] = %q, want \"true\"", ManagedLabel, got[ManagedLabel])
	}

	// Caller-supplied ownership labels are carried through alongside it.
	extra := map[string]string{
		InstanceLabel:  "inst-1",
		OwnerHostLabel: "hostA",
		OwnerPIDLabel:  "4242",
	}
	got = managedLabels(extra)
	want := map[string]string{
		ManagedLabel:   "true",
		InstanceLabel:  "inst-1",
		OwnerHostLabel: "hostA",
		OwnerPIDLabel:  "4242",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("managedLabels(extra) = %v, want %v", got, want)
	}

	// The caller cannot clobber the managed marker.
	got = managedLabels(map[string]string{ManagedLabel: "false"})
	if got[ManagedLabel] != "true" {
		t.Fatalf("managedLabels must not let callers override ManagedLabel; got %q", got[ManagedLabel])
	}
}

func TestMemoryBytes(t *testing.T) {
	cases := []struct {
		mb   int
		want int64
	}{
		{0, 0},
		{-5, 0},
		{1, 1024 * 1024},
		{128, 128 * 1024 * 1024},
		{512, 512 * 1024 * 1024},
	}
	for _, c := range cases {
		if got := memoryBytes(c.mb); got != c.want {
			t.Errorf("memoryBytes(%d) = %d, want %d", c.mb, got, c.want)
		}
	}
}
