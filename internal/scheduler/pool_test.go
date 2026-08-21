package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenMinter returns a deterministic mkToken that hands out "tok-1", "tok-2", …
// so tests can address slots by their minted token.
func tokenMinter() func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		return "tok-" + itoa(n), nil
	}
}

// coldAcquire reserves a fresh slot for fn and asserts it was a cold reserve.
func coldAcquire(t *testing.T, p *pool, fn string, mk func() (string, error)) *slot {
	t.Helper()
	s, warm, err := p.acquire(fn, mk)
	require.NoError(t, err)
	require.False(t, warm, "expected a cold reserve, got a warm slot")
	require.NotNil(t, s)
	return s
}

func TestPoolAcquireWarmReturnsLIFOIdleSlot(t *testing.T) {
	p := newPool(10, 10)
	mk := tokenMinter()

	s1 := coldAcquire(t, p, "fn", mk)
	s2 := coldAcquire(t, p, "fn", mk)

	// Push both onto the idle stack in order s1, then s2.
	now := time.Now()
	p.markIdle(s1, now)
	p.markIdle(s2, now)

	// LIFO: the most recently idled slot comes back first.
	got, warm, err := p.acquire("fn", mk)
	require.NoError(t, err)
	assert.True(t, warm)
	assert.Same(t, s2, got)

	got, warm, err = p.acquire("fn", mk)
	require.NoError(t, err)
	assert.True(t, warm)
	assert.Same(t, s1, got)

	// Idle stack is now empty; the entry is deleted, not left as an empty slice.
	assert.NotContains(t, p.idle, "fn")
}

func TestPoolAcquireFunctionCap(t *testing.T) {
	p := newPool(100, 2)
	mk := tokenMinter()

	coldAcquire(t, p, "fn", mk)
	coldAcquire(t, p, "fn", mk)

	// Third live slot for the same fn breaches the per-function cap.
	s, warm, err := p.acquire("fn", mk)
	assert.Nil(t, s)
	assert.False(t, warm)
	assert.ErrorIs(t, err, errFunctionCap)

	// A different function is unaffected by another fn's cap.
	other := coldAcquire(t, p, "other", mk)
	assert.NotNil(t, other)
}

func TestPoolAcquireGlobalCap(t *testing.T) {
	// Global cap of 2 is tighter than the per-fn cap, and is checked first.
	p := newPool(2, 100)
	mk := tokenMinter()

	coldAcquire(t, p, "a", mk)
	coldAcquire(t, p, "b", mk)

	s, warm, err := p.acquire("c", mk)
	assert.Nil(t, s)
	assert.False(t, warm)
	assert.ErrorIs(t, err, errGlobalCap)
}

func TestPoolAcquireClosed(t *testing.T) {
	p := newPool(10, 10)
	p.drain()

	s, warm, err := p.acquire("fn", tokenMinter())
	assert.Nil(t, s)
	assert.False(t, warm)
	assert.ErrorIs(t, err, errPoolClosed)
}

func TestPoolAcquireMkTokenError(t *testing.T) {
	p := newPool(10, 10)
	boom := errors.New("mint failed")

	s, warm, err := p.acquire("fn", func() (string, error) { return "", boom })
	assert.Nil(t, s)
	assert.False(t, warm)
	assert.ErrorIs(t, err, boom)

	// A failed mint reserves nothing: no leaked count or index entry.
	assert.Equal(t, 0, p.liveTotal())
}

func TestPoolExpiredBoundary(t *testing.T) {
	p := newPool(10, 10)
	mk := tokenMinter()
	s := coldAcquire(t, p, "fn", mk)

	base := time.Now()
	p.markIdle(s, base)

	const ttl = time.Minute

	// Just under ttl: kept.
	got := p.expired(base.Add(ttl-time.Nanosecond), ttl)
	assert.Empty(t, got)
	assert.Equal(t, 1, p.liveTotal())

	// Exactly ttl: expired (boundary is inclusive, now.Sub(lastUsed) >= ttl).
	got = p.expired(base.Add(ttl), ttl)
	require.Len(t, got, 1)
	assert.Same(t, s, got[0])

	// The expired slot is fully detached, not just popped off the idle stack.
	assert.Equal(t, 0, p.liveTotal())
	_, ok := p.lookup(s.token)
	assert.False(t, ok)
	assert.NotContains(t, p.idle, "fn")
}

func TestPoolRemoveIdempotentAndConsistent(t *testing.T) {
	p := newPool(10, 10)
	mk := tokenMinter()
	s1 := coldAcquire(t, p, "fn", mk)
	s2 := coldAcquire(t, p, "fn", mk)
	require.Equal(t, 2, p.liveTotal())

	assert.True(t, p.remove(s1), "first remove reports it was present")
	assert.False(t, p.remove(s1), "second remove is a no-op")

	// total and the per-function set both drop by exactly one.
	assert.Equal(t, 1, p.liveTotal())
	assert.Len(t, p.byFn["fn"], 1)
	_, ok := p.lookup(s1.token)
	assert.False(t, ok)
	_, ok = p.lookup(s2.token)
	assert.True(t, ok)

	// Removing the last slot drops the empty per-function set entirely.
	assert.True(t, p.remove(s2))
	assert.Equal(t, 0, p.liveTotal())
	assert.NotContains(t, p.byFn, "fn")
}

func TestPoolRemoveClearsIdleEntry(t *testing.T) {
	p := newPool(10, 10)
	mk := tokenMinter()
	s := coldAcquire(t, p, "fn", mk)
	p.markIdle(s, time.Now())

	assert.True(t, p.remove(s))
	// A removed slot must not linger in the idle stack.
	assert.NotContains(t, p.idle, "fn")
	assert.Equal(t, 0, p.liveTotal())
}

func TestPoolDrainReturnsAllOnce(t *testing.T) {
	p := newPool(10, 10)
	mk := tokenMinter()
	a := coldAcquire(t, p, "fn-a", mk)
	b := coldAcquire(t, p, "fn-b", mk)
	c := coldAcquire(t, p, "fn-b", mk)

	all := p.drain()
	assert.ElementsMatch(t, []*slot{a, b, c}, all)

	// State is fully cleared and the pool is closed.
	assert.Equal(t, 0, p.liveTotal())

	// A second drain returns nil so one-shot teardown runs exactly once.
	assert.Nil(t, p.drain())
}

func TestPoolLookup(t *testing.T) {
	p := newPool(10, 10)
	mk := tokenMinter()
	s := coldAcquire(t, p, "fn", mk)

	got, ok := p.lookup(s.token)
	assert.True(t, ok)
	assert.Same(t, s, got)

	got, ok = p.lookup("nope")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestPoolContainerIDs(t *testing.T) {
	p := newPool(10, 10)
	mk := tokenMinter()
	s1 := coldAcquire(t, p, "fn", mk)
	s2 := coldAcquire(t, p, "fn", mk)
	s3 := coldAcquire(t, p, "fn", mk)
	other := coldAcquire(t, p, "other", mk)

	s1.containerID = "cid-1"
	s2.containerID = "cid-2"
	// s3 has no container yet (empty id) and must be filtered out.
	other.containerID = "cid-other"
	_ = s3

	ids := p.containerIDs("fn")
	assert.ElementsMatch(t, []string{"cid-1", "cid-2"}, ids)

	// A function with no live slots yields an empty (non-nil) slice.
	assert.Empty(t, p.containerIDs("ghost"))
}
