package scheduler

import (
	"errors"
	"sync"
	"time"
)

// Sentinel outcomes from pool.acquire. The Engine (the imperative shell) maps
// these to *apierror.Error; the pool (the core) stays free of the HTTP wire
// vocabulary.
var (
	errPoolClosed  = errors.New("scheduler: pool closed")
	errGlobalCap   = errors.New("scheduler: daemon-wide concurrency limit reached")
	errFunctionCap = errors.New("scheduler: function concurrency limit reached")
)

// pool is the scheduler's stateful core: it owns every slot behind a single
// mutex — the token index, the per-function slot sets, the per-function idle
// stacks, and the live-slot count. All the invariants (total == sum of per-fn
// sets, idle ⊆ byFn, byToken kept in sync) live here and nowhere else, so they
// can be reasoned about in isolation. Every method is safe for concurrent use;
// the Engine never touches this state directly.
type pool struct {
	maxGlobal int
	maxPerFn  int

	mu      sync.Mutex
	closed  bool
	byToken map[string]*slot
	byFn    map[string]map[*slot]struct{}
	idle    map[string][]*slot
	total   int
}

func newPool(maxGlobal, maxPerFn int) *pool {
	return &pool{
		maxGlobal: maxGlobal,
		maxPerFn:  maxPerFn,
		byToken:   make(map[string]*slot),
		byFn:      make(map[string]map[*slot]struct{}),
		idle:      make(map[string][]*slot),
	}
}

// acquire returns a warm idle slot for fn (warm=true), or reserves and returns a
// fresh not-yet-started slot (warm=false), or fails with errPoolClosed /
// errGlobalCap / errFunctionCap. mkToken mints the per-slot token and is only
// called when a new slot is reserved.
func (p *pool) acquire(fn string, mkToken func() (string, error)) (s *slot, warm bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, false, errPoolClosed
	}
	if stack := p.idle[fn]; len(stack) > 0 {
		s := stack[len(stack)-1]
		p.idle[fn] = stack[:len(stack)-1]
		if len(p.idle[fn]) == 0 {
			delete(p.idle, fn)
		}
		return s, true, nil
	}
	if p.total >= p.maxGlobal {
		return nil, false, errGlobalCap
	}
	if len(p.byFn[fn]) >= p.maxPerFn {
		return nil, false, errFunctionCap
	}

	token, err := mkToken()
	if err != nil {
		return nil, false, err
	}
	s = newSlot(token, fn)
	p.byToken[token] = s
	if p.byFn[fn] == nil {
		p.byFn[fn] = make(map[*slot]struct{})
	}
	p.byFn[fn][s] = struct{}{}
	p.total++
	return s, false, nil
}

// markIdle returns a live slot to its function's idle stack, stamping lastUsed.
// A slot that was concurrently removed (raced with destroy/reap) or a closed
// pool is silently dropped.
func (p *pool) markIdle(s *slot, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if _, live := p.byToken[s.token]; !live {
		return
	}
	s.lastUsed = at
	p.idle[s.fnName] = append(p.idle[s.fnName], s)
}

// remove deletes s from all bookkeeping and reports whether it was still present,
// so destroy is idempotent across the timeout / init-error / reaper paths.
func (p *pool) remove(s *slot) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.removeLocked(s)
}

// expired removes and returns idle slots whose lastUsed is older than ttl.
func (p *pool) expired(now time.Time, ttl time.Duration) []*slot {
	p.mu.Lock()
	defer p.mu.Unlock()

	var out []*slot
	for fn, stack := range p.idle {
		kept := stack[:0]
		for _, s := range stack {
			if now.Sub(s.lastUsed) >= ttl {
				out = append(out, s)
			} else {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			delete(p.idle, fn)
		} else {
			p.idle[fn] = kept
		}
	}
	// Idle slots are removed from the idle stacks above; drop them from the token
	// and per-function indexes too.
	for _, s := range out {
		p.detachLocked(s)
	}
	return out
}

// drain marks the pool closed and returns every live slot, clearing all state.
// A second call returns nil, letting the caller run one-shot teardown exactly
// once.
func (p *pool) drain() []*slot {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true

	all := make([]*slot, 0, len(p.byToken))
	for _, s := range p.byToken {
		all = append(all, s)
	}
	p.byToken = make(map[string]*slot)
	p.byFn = make(map[string]map[*slot]struct{})
	p.idle = make(map[string][]*slot)
	p.total = 0
	return all
}

// lookup resolves a token to its slot.
func (p *pool) lookup(token string) (*slot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.byToken[token]
	return s, ok
}

// containerIDs returns the non-empty container IDs of fn's live slots.
func (p *pool) containerIDs(fn string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	set := p.byFn[fn]
	ids := make([]string, 0, len(set))
	for s := range set {
		if s.containerID != "" {
			ids = append(ids, s.containerID)
		}
	}
	return ids
}

// liveTotal reports the number of live slots.
func (p *pool) liveTotal() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.total
}

// removeLocked drops s from every index (including the idle stack). Caller holds mu.
func (p *pool) removeLocked(s *slot) bool {
	if _, live := p.byToken[s.token]; !live {
		return false
	}
	p.removeIdleLocked(s)
	p.detachLocked(s)
	return true
}

// detachLocked drops s from the token and per-function indexes and decrements
// the count. It does NOT touch the idle stack. Caller holds mu.
func (p *pool) detachLocked(s *slot) {
	delete(p.byToken, s.token)
	if set := p.byFn[s.fnName]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(p.byFn, s.fnName)
		}
	}
	p.total--
}

// removeIdleLocked drops s from its function's idle stack if present. Caller holds mu.
func (p *pool) removeIdleLocked(s *slot) {
	stack := p.idle[s.fnName]
	for i, cand := range stack {
		if cand == s {
			p.idle[s.fnName] = append(stack[:i], stack[i+1:]...)
			if len(p.idle[s.fnName]) == 0 {
				delete(p.idle, s.fnName)
			}
			return
		}
	}
}
