package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/runtime"
	"github.com/ajmcquilkin/mini-lambda/internal/runtimeapi"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

// Defaults for Config fields left at their zero value.
const (
	DefaultMaxConcurrency         = 32
	DefaultPerFunctionConcurrency = 4
	DefaultIdleTTL                = 5 * time.Minute
)

// destroyTimeout bounds the stop+remove of a container during teardown.
const destroyTimeout = 30 * time.Second

// runtimeAPIEnvVar is the environment variable the RIC reads to find the
// Runtime API. Its value is "<reachable-host>:<port>/<token>".
const runtimeAPIEnvVar = "AWS_LAMBDA_RUNTIME_API"

// Config configures an Engine.
type Config struct {
	// ReachableHost is the host:port a container uses to reach the daemon's
	// Runtime API listener (e.g. "host.docker.internal:9001"). The per-slot token
	// is appended as a trailing path segment.
	ReachableHost string

	// ExtraHosts are extra "hostname:IP" container /etc/hosts entries, e.g.
	// "host.docker.internal:host-gateway" so the container can reach ReachableHost.
	ExtraHosts []string

	// MaxConcurrency caps the total number of live slots (containers) daemon-wide.
	MaxConcurrency int

	// PerFunctionConcurrency caps the number of live slots for a single function.
	PerFunctionConcurrency int

	// IdleTTL is how long an idle slot survives before the reaper destroys it.
	IdleTTL time.Duration

	// ReapInterval is how often the idle reaper runs. Zero derives a value from
	// IdleTTL.
	ReapInterval time.Duration

	// now, newToken, and timeoutFor are injectable for tests; nil selects the
	// real implementations.
	now        func() time.Time
	newToken   func() (string, error)
	timeoutFor func(fn *model.Function) time.Duration
}

// Engine is the concrete slot-based scheduler.Scheduler. It is the imperative
// shell: it drives docker IO, timeouts, the reaper goroutine, and store
// side-effects, delegating all slot state to an internal pool. It also
// implements runtimeapi.Registry so the Runtime API listener can resolve a
// token to the slot mailbox the RIC should talk to.
type Engine struct {
	store store.Store
	rt    runtime.Runtime
	cfg   Config
	pool  *pool

	now        func() time.Time
	newToken   func() (string, error)
	timeoutFor func(fn *model.Function) time.Duration

	reaperStop chan struct{}
	reaperDone chan struct{}

	// touchWG tracks in-flight best-effort last_invoked_at updates so Shutdown
	// can drain them.
	touchWG sync.WaitGroup
}

var (
	_ Scheduler           = (*Engine)(nil)
	_ runtimeapi.Registry = (*Engine)(nil)
)

// New constructs an Engine and starts its idle reaper. Unset Config limits fall
// back to the package defaults.
func New(st store.Store, rt runtime.Runtime, cfg Config) *Engine {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = DefaultMaxConcurrency
	}
	if cfg.PerFunctionConcurrency <= 0 {
		cfg.PerFunctionConcurrency = DefaultPerFunctionConcurrency
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = DefaultIdleTTL
	}
	if cfg.ReapInterval <= 0 {
		cfg.ReapInterval = reapIntervalFor(cfg.IdleTTL)
	}

	e := &Engine{
		store:      st,
		rt:         rt,
		cfg:        cfg,
		pool:       newPool(cfg.MaxConcurrency, cfg.PerFunctionConcurrency),
		now:        cfg.now,
		newToken:   cfg.newToken,
		timeoutFor: cfg.timeoutFor,
		reaperStop: make(chan struct{}),
		reaperDone: make(chan struct{}),
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.newToken == nil {
		e.newToken = randomToken
	}
	if e.timeoutFor == nil {
		e.timeoutFor = func(fn *model.Function) time.Duration {
			return time.Duration(fn.TimeoutSec) * time.Second
		}
	}

	go e.reapLoop()
	return e
}

// Invoke acquires a slot for fnName and runs payload through it, enforcing the
// function timeout. It returns *apierror.Error (429) when at capacity and
// passes store.ErrNotFound through for unknown functions.
func (e *Engine) Invoke(ctx context.Context, fnName string, payload []byte) (*model.InvokeResult, error) {
	fn, err := e.store.GetFunction(ctx, fnName)
	if err != nil {
		return nil, err
	}

	s, err := e.acquire(ctx, fn)
	if err != nil {
		return nil, err
	}

	ic := model.NewInvocationContext(fn)
	p := &pending{
		inv: &runtimeapi.Invocation{
			RequestID:          ic.RequestID,
			DeadlineMs:         ic.DeadlineMs,
			InvokedFunctionArn: ic.InvokedFunctionArn,
			TraceID:            ic.TraceID,
			Payload:            payload,
		},
		result: make(chan outcome, 1),
	}
	s.deliver(p)

	timeout := e.timeoutFor(fn)
	if timeout <= 0 {
		timeout = DefaultIdleTTL // defensive; a real function always has a timeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case o := <-p.result:
		return e.finish(s, fn, o), nil
	case <-timer.C:
		e.destroy(s)
		return timeoutResult(p.inv, timeout), nil
	case <-ctx.Done():
		e.destroy(s)
		return nil, ctx.Err()
	}
}

// finish maps a terminal outcome to an InvokeResult and either returns the slot
// to the idle pool (success/handler-error keep the sandbox warm) or destroys it
// (init failure).
func (e *Engine) finish(s *slot, fn *model.Function, o outcome) *model.InvokeResult {
	switch o.kind {
	case outcomeResponse:
		e.release(s)
		e.touchLastInvoked(fn.Name)
		return &model.InvokeResult{Payload: o.payload, StatusCode: 200}
	case outcomeError:
		e.release(s)
		e.touchLastInvoked(fn.Name)
		return &model.InvokeResult{Payload: o.payload, FunctionError: "Unhandled", StatusCode: 200}
	default: // outcomeInitError
		e.destroy(s)
		return &model.InvokeResult{Payload: o.payload, FunctionError: "Unhandled", StatusCode: 200}
	}
}

// acquire returns a warm idle slot from the pool, or reserves a new one and
// cold-starts its container. Pool errors are mapped to the API wire vocabulary.
func (e *Engine) acquire(ctx context.Context, fn *model.Function) (*slot, error) {
	s, warm, err := e.pool.acquire(fn.Name, e.newToken)
	if err != nil {
		return nil, mapAcquireError(fn.Name, err)
	}
	if warm {
		return s, nil
	}
	if err := e.coldStart(ctx, fn, s); err != nil {
		e.destroy(s)
		return nil, apierror.Internal("cold start " + fn.Name + ": " + err.Error())
	}
	return s, nil
}

// mapAcquireError translates a pool sentinel into the public API error. The pool
// stays free of HTTP semantics; this is the shell's job.
func mapAcquireError(fnName string, err error) error {
	switch {
	case errors.Is(err, errPoolClosed):
		return apierror.Internal("scheduler is shutting down")
	case errors.Is(err, errGlobalCap):
		return apierror.Throttled("daemon-wide max concurrency reached")
	case errors.Is(err, errFunctionCap):
		return apierror.Throttled("function concurrency limit reached for " + fnName)
	default:
		return apierror.Internal("allocate slot for " + fnName + ": " + err.Error())
	}
}

// coldStart pulls the image, creates the container with the function env plus
// the Runtime API env, and starts it. The slot's RIC will connect back to the
// Runtime API listener using the token embedded in AWS_LAMBDA_RUNTIME_API.
func (e *Engine) coldStart(ctx context.Context, fn *model.Function, s *slot) error {
	if err := e.rt.Pull(ctx, fn.Image); err != nil {
		return fmt.Errorf("pull image: %w", err)
	}

	spec := runtime.ContainerSpec{
		Image:    fn.Image,
		Env:      fn.Env,
		MemoryMB: fn.MemoryMB,
		RuntimeAPIEnv: map[string]string{
			runtimeAPIEnvVar: e.cfg.ReachableHost + "/" + s.token,
		},
		ExtraHosts: e.cfg.ExtraHosts,
	}

	id, err := e.rt.Create(ctx, spec)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	s.containerID = id

	if err := e.rt.Start(ctx, id); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// release returns a slot to the idle pool, stamped with the current time.
func (e *Engine) release(s *slot) {
	e.pool.markIdle(s, e.now())
}

// destroy removes a slot from the pool, unblocks its poll, and stops+removes its
// container. It is idempotent: only the caller that actually removed the slot
// tears the container down.
func (e *Engine) destroy(s *slot) {
	if !e.pool.remove(s) {
		return
	}
	s.close()
	e.stopContainer(s)
}

// stopContainer best-effort stops and removes a slot's container.
func (e *Engine) stopContainer(s *slot) {
	if s.containerID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), destroyTimeout)
	defer cancel()
	_ = e.rt.Stop(ctx, s.containerID)
	_ = e.rt.Remove(ctx, s.containerID)
}

// touchLastInvoked best-effort stamps the function's last_invoked_at. It runs
// asynchronously and swallows errors: invocation success does not depend on it.
func (e *Engine) touchLastInvoked(name string) {
	e.touchWG.Add(1)
	go func() {
		defer e.touchWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fn, err := e.store.GetFunction(ctx, name)
		if err != nil {
			return
		}
		now := e.now().UTC()
		fn.LastInvokedAt = &now
		_ = e.store.UpdateFunctionConfiguration(ctx, fn)
	}()
}

// FunctionContainerIDs returns the container IDs of the live slots (idle+busy)
// for name, in no particular order. Used by the daemon's log endpoint.
func (e *Engine) FunctionContainerIDs(name string) []string {
	return e.pool.containerIDs(name)
}

// LookupSlot resolves a token to its slot mailbox for the Runtime API.
func (e *Engine) LookupSlot(token string) (runtimeapi.Slot, bool) {
	s, ok := e.pool.lookup(token)
	if !ok {
		return nil, false
	}
	return s, true
}

// Shutdown stops the reaper and destroys every managed container. It is safe to
// call more than once; only the first call does work.
func (e *Engine) Shutdown(ctx context.Context) error {
	all := e.pool.drain()
	if all == nil {
		return nil // already shut down
	}

	close(e.reaperStop)
	<-e.reaperDone
	e.touchWG.Wait()

	// Tear slots down concurrently so per-container stop grace periods don't
	// serialize into a total that blows the caller's shutdown budget.
	var wg sync.WaitGroup
	for _, s := range all {
		s.close()
		if s.containerID == "" {
			continue
		}
		wg.Add(1)
		go func(s *slot) {
			defer wg.Done()
			_ = e.rt.Stop(ctx, s.containerID)
			_ = e.rt.Remove(ctx, s.containerID)
		}(s)
	}
	wg.Wait()
	return nil
}

// reapLoop periodically destroys idle slots that have exceeded IdleTTL.
func (e *Engine) reapLoop() {
	defer close(e.reaperDone)
	ticker := time.NewTicker(e.cfg.ReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.reaperStop:
			return
		case <-ticker.C:
			for _, s := range e.pool.expired(e.now(), e.cfg.IdleTTL) {
				s.close()
				e.stopContainer(s)
			}
		}
	}
}

// timeoutResult fabricates the Lambda-style error payload for a timed-out
// invocation.
func timeoutResult(inv *runtimeapi.Invocation, timeout time.Duration) *model.InvokeResult {
	msg := fmt.Sprintf("%s Task timed out after %.2f seconds", inv.RequestID, timeout.Seconds())
	payload := fmt.Sprintf(`{"errorMessage":%q,"errorType":"Sandbox.Timedout"}`, msg)
	return &model.InvokeResult{
		Payload:       []byte(payload),
		FunctionError: "Unhandled",
		StatusCode:    200,
	}
}

// reapIntervalFor derives a reaper tick from the idle TTL: often enough to be
// responsive, capped so very long TTLs don't idle-spin.
func reapIntervalFor(ttl time.Duration) time.Duration {
	iv := ttl / 4
	if iv < time.Second {
		iv = time.Second
	}
	if iv > time.Minute {
		iv = time.Minute
	}
	return iv
}

// randomToken returns a 128-bit crypto-random hex token.
func randomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
