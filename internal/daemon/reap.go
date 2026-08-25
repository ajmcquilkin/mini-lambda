package daemon

import (
	"context"
	"os"
	"strconv"
	"syscall"
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/runtime/docker"
)

// ownerID identifies the daemon run that owns a container. It is stamped onto
// every managed container as labels and reconstructed from those labels by the
// reaper to decide liveness.
//
// The ownership mechanism is deliberately the simplest thing that satisfies the
// two hard requirements: (a) two live daemons on the same docker host must never
// reap each other's containers, and (b) a daemon that died uncleanly has its
// containers removed by the next daemon's startup. We tag each container with a
// per-run instance id, the owner process's pid, and the owner's hostname; the
// reaper removes a managed container only when it belongs to a *different*
// instance on the *same* host whose pid is no longer a live process.
//
// Limitations (documented, accepted): pid liveness is only meaningful on the
// same host, so containers labeled with a different host are never reaped
// (harmless for the local/CI use case where every daemon shares one docker
// host; a remote/shared docker daemon across hosts is out of scope). Pid reuse
// can make a dead owner look alive — the orphan is then left for a later startup
// once that pid frees, which is safe (we err toward leaking, never toward
// reaping a live peer's container).
type ownerID struct {
	instance string
	host     string
	pid      int
}

// newOwnerID builds the current process's ownership identity. instance is a
// fresh random id unique to this daemon run.
func newOwnerID(instance string) ownerID {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return ownerID{instance: instance, host: host, pid: os.Getpid()}
}

// labels renders the ownership identity as the container label set the
// scheduler stamps on every container it creates.
func (o ownerID) labels() map[string]string {
	return map[string]string{
		docker.InstanceLabel:  o.instance,
		docker.OwnerHostLabel: o.host,
		docker.OwnerPIDLabel:  strconv.Itoa(o.pid),
	}
}

// reapVerdict is the pure decision for a single managed container.
type reapVerdict struct {
	reap   bool
	reason string
}

// classifyContainer decides whether the starting daemon (self) should reap a
// managed container carrying labels. alive reports whether a pid is a live
// process on this host. It is a pure function of its inputs so the whole policy
// is table-testable without docker:
//
//   - our own instance                     -> keep (never reap a container we made)
//   - a different host                      -> keep (can't judge liveness there)
//   - missing/invalid owner pid             -> reap (unattributable orphan on our host)
//   - owner pid still alive                 -> keep (a live peer daemon owns it)
//   - owner pid dead                        -> reap (its daemon died uncleanly)
func classifyContainer(labels map[string]string, self ownerID, alive func(pid int) bool) reapVerdict {
	if inst := labels[docker.InstanceLabel]; inst != "" && inst == self.instance {
		return reapVerdict{reap: false, reason: "own instance"}
	}
	if host := labels[docker.OwnerHostLabel]; host != "" && host != self.host {
		return reapVerdict{reap: false, reason: "different host"}
	}
	pid, err := strconv.Atoi(labels[docker.OwnerPIDLabel])
	if err != nil || pid <= 0 {
		return reapVerdict{reap: true, reason: "no owner pid"}
	}
	if alive(pid) {
		return reapVerdict{reap: false, reason: "owner alive"}
	}
	return reapVerdict{reap: true, reason: "owner dead"}
}

// pidAlive reports whether pid is a live process on this host. On Unix, signal 0
// probes for existence without delivering a signal: nil means the process
// exists, ESRCH means it does not, and EPERM means it exists but is owned by
// another user (still alive).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}

// managedReaper is the minimal slice of the docker runtime the startup reaper
// needs: list every managed container and remove one by id. *docker.Runtime
// satisfies it. Remove is already on runtime.Runtime; ListManaged is a concrete
// *docker.Runtime method, so this local interface keeps the frozen
// runtime.Runtime interface untouched (mirroring dockerPinger).
type managedReaper interface {
	ListManaged(ctx context.Context) ([]docker.ManagedContainer, error)
	Remove(ctx context.Context, id string) error
}

// reapTimeout bounds the whole startup reap sweep so a slow/unreachable docker
// daemon can't wedge startup.
const reapTimeout = 15 * time.Second

// reapOrphans removes managed containers whose owning daemon is dead, per
// classifyContainer. It is best-effort: listing or removing failures are logged
// and startup continues. Returns the number of containers removed (for tests
// and logging).
func reapOrphans(ctx context.Context, r managedReaper, self ownerID, alive func(pid int) bool, logf func(string, ...any)) int {
	rctx, cancel := context.WithTimeout(ctx, reapTimeout)
	defer cancel()

	containers, err := r.ListManaged(rctx)
	if err != nil {
		logf("startup reaper: list managed containers: %v — skipping orphan reap", err)
		return 0
	}

	removed := 0
	for _, c := range containers {
		v := classifyContainer(c.Labels, self, alive)
		if !v.reap {
			continue
		}
		if err := r.Remove(rctx, c.ID); err != nil {
			logf("startup reaper: remove orphan %s (%s): %v", short(c.ID), v.reason, err)
			continue
		}
		logf("startup reaper: removed orphan container %s (%s)", short(c.ID), v.reason)
		removed++
	}
	if removed > 0 {
		logf("startup reaper: reaped %d orphaned container(s)", removed)
	}
	return removed
}

// short truncates a container id to its conventional 12-char short form for
// logging.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
