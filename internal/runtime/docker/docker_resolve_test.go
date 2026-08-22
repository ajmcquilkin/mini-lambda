package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveDockerHost(t *testing.T) {
	const home = "/Users/dev"
	desktop := home + desktopSocketRel

	// envMap builds an env lookup from a fixed map (missing keys -> "").
	envMap := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	// existsSet builds a stat predicate that reports true only for listed paths.
	existsSet := func(paths ...string) func(string) bool {
		set := make(map[string]bool, len(paths))
		for _, p := range paths {
			set[p] = true
		}
		return func(p string) bool { return set[p] }
	}

	tests := []struct {
		name     string
		env      map[string]string
		exists   func(string) bool
		wantHost string
	}{
		{
			name:     "DOCKER_HOST wins and is honored as-is",
			env:      map[string]string{"DOCKER_HOST": "tcp://1.2.3.4:2375", "HOME": home},
			exists:   existsSet(stdDockerSocket, desktop),
			wantHost: "tcp://1.2.3.4:2375",
		},
		{
			name:     "standard socket is used when present",
			env:      map[string]string{"HOME": home},
			exists:   existsSet(stdDockerSocket),
			wantHost: "unix://" + stdDockerSocket,
		},
		{
			name:     "falls through to Docker Desktop per-user socket",
			env:      map[string]string{"HOME": home},
			exists:   existsSet(desktop),
			wantHost: "unix://" + desktop,
		},
		{
			name:     "standard socket preferred over desktop when both exist",
			env:      map[string]string{"HOME": home},
			exists:   existsSet(stdDockerSocket, desktop),
			wantHost: "unix://" + stdDockerSocket,
		},
		{
			name:     "nothing resolves -> empty host (SDK default)",
			env:      map[string]string{"HOME": home},
			exists:   existsSet(),
			wantHost: "",
		},
		{
			name:     "no HOME and no standard socket -> empty host",
			env:      map[string]string{},
			exists:   existsSet(),
			wantHost: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantHost, resolveDockerHost(envMap(tt.env), tt.exists))
		})
	}
}
