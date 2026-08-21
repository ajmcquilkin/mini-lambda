package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/runtime"
	"github.com/ajmcquilkin/mini-lambda/internal/runtime/runtimemock"
	"github.com/ajmcquilkin/mini-lambda/internal/runtimeapi"
	"github.com/ajmcquilkin/mini-lambda/internal/store/storemock"
)

// ricBehavior simulates the in-container Runtime Interface Client for one
// invocation: it is called with the slot and the delivered event and decides
// how the "handler" responds.
type ricBehavior func(slot runtimeapi.Slot, inv *runtimeapi.Invocation)

// harness wires an Engine on top of mock store/runtime plus a fake RIC that the
// mocked Create() spawns per cold-started slot.
type harness struct {
	t     *testing.T
	eng   *Engine
	store *storemock.MockStore
	rt    *runtimemock.MockRuntime

	mu          sync.Mutex
	behavior    ricBehavior
	nextID      int
	createCount int
}

func newHarness(t *testing.T, fn model.Function, cfg Config) *harness {
	t.Helper()
	ctrl := gomock.NewController(t)
	h := &harness{
		t:     t,
		store: storemock.NewMockStore(ctrl),
		rt:    runtimemock.NewMockRuntime(ctrl),
	}

	// GetFunction returns a fresh copy so the async last_invoked_at updater and
	// the invoke path never share a *model.Function.
	h.store.EXPECT().GetFunction(gomock.Any(), fn.Name).
		DoAndReturn(func(_ context.Context, _ string) (*model.Function, error) {
			cp := fn
			return &cp, nil
		}).AnyTimes()
	h.store.EXPECT().UpdateFunctionConfiguration(gomock.Any(), gomock.Any()).
		Return(nil).AnyTimes()

	if cfg.now == nil {
		cfg.now = time.Now
	}
	h.eng = New(h.store, h.rt, cfg)

	// A cold start spawns a fake RIC keyed off the token embedded in the spec.
	h.rt.EXPECT().Pull(gomock.Any(), fn.Image).Return(nil).AnyTimes()
	h.rt.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, spec runtime.ContainerSpec) (string, error) {
			token := tokenFromSpec(spec)
			h.mu.Lock()
			h.nextID++
			h.createCount++
			id := "container-" + itoa(h.nextID)
			behavior := h.behavior
			h.mu.Unlock()
			h.spawnRIC(token, behavior)
			return id, nil
		}).AnyTimes()
	h.rt.EXPECT().Start(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	t.Cleanup(func() {
		_ = h.eng.Shutdown(context.Background())
	})
	return h
}

// setBehavior sets the RIC behavior used for subsequently cold-started slots.
func (h *harness) setBehavior(b ricBehavior) {
	h.mu.Lock()
	h.behavior = b
	h.mu.Unlock()
}

// creates returns how many containers were cold-started.
func (h *harness) creates() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.createCount
}

// spawnRIC runs a fake RIC loop for a slot: poll next, run the behavior, repeat
// until the slot is destroyed.
func (h *harness) spawnRIC(token string, behavior ricBehavior) {
	slot, ok := h.eng.LookupSlot(token)
	if !ok {
		return
	}
	go func() {
		ctx := context.Background()
		for {
			inv, err := slot.Next(ctx)
			if err != nil {
				return // slot destroyed
			}
			behavior(slot, inv)
		}
	}()
}

// expectStopRemove asserts a container is stopped and removed exactly once.
func (h *harness) expectStopRemove() {
	h.rt.EXPECT().Stop(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	h.rt.EXPECT().Remove(gomock.Any(), gomock.Any()).Return(nil).Times(1)
}

func tokenFromSpec(spec runtime.ContainerSpec) string {
	v := spec.RuntimeAPIEnv[runtimeAPIEnvVar]
	if i := strings.LastIndex(v, "/"); i >= 0 {
		return v[i+1:]
	}
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func testFunction() model.Function {
	return model.Function{Name: "hello", Image: "img:latest", MemoryMB: 128, TimeoutSec: 3}
}

// respondEcho echoes the event payload back as the handler response.
func respondEcho(slot runtimeapi.Slot, inv *runtimeapi.Invocation) {
	_ = slot.Respond(inv.RequestID, inv.Payload)
}

func TestInvokeSuccessAndWarmReuse(t *testing.T) {
	h := newHarness(t, testFunction(), Config{})
	h.setBehavior(respondEcho)
	h.expectStopRemove() // one warm container, torn down on Shutdown

	res1, err := h.eng.Invoke(t.Context(), "hello", []byte(`{"n":1}`))
	require.NoError(t, err)
	assert.Equal(t, `{"n":1}`, string(res1.Payload))
	assert.Empty(t, res1.FunctionError)
	assert.Equal(t, 200, res1.StatusCode)

	res2, err := h.eng.Invoke(t.Context(), "hello", []byte(`{"n":2}`))
	require.NoError(t, err)
	assert.Equal(t, `{"n":2}`, string(res2.Payload))

	// The second invoke reused the warm slot rather than cold-starting.
	assert.Equal(t, 1, h.creates())
}

func TestInvokeHandlerErrorKeepsSlotWarm(t *testing.T) {
	h := newHarness(t, testFunction(), Config{})
	errBehavior := func(slot runtimeapi.Slot, inv *runtimeapi.Invocation) {
		_ = slot.InvocationError(inv.RequestID, []byte(`{"errorMessage":"boom"}`))
	}
	h.setBehavior(errBehavior)
	h.expectStopRemove()

	for i := 0; i < 2; i++ {
		res, err := h.eng.Invoke(t.Context(), "hello", []byte(`{}`))
		require.NoError(t, err)
		assert.Equal(t, "Unhandled", res.FunctionError)
		assert.Equal(t, `{"errorMessage":"boom"}`, string(res.Payload))
	}
	assert.Equal(t, 1, h.creates()) // handler error keeps the slot warm
}

func TestInvokeInitErrorDestroysSlot(t *testing.T) {
	h := newHarness(t, testFunction(), Config{})
	initBehavior := func(slot runtimeapi.Slot, inv *runtimeapi.Invocation) {
		_ = slot.InitError([]byte(`{"errorMessage":"cannot init"}`))
	}
	h.setBehavior(initBehavior)
	// The slot is destroyed on init error: its container is stopped+removed.
	h.expectStopRemove()

	res, err := h.eng.Invoke(t.Context(), "hello", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, "Unhandled", res.FunctionError)
	assert.Contains(t, string(res.Payload), "cannot init")
}

func TestTimeoutFailsAndDestroysSlot(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	cfg := Config{
		timeoutFor: func(*model.Function) time.Duration { return 40 * time.Millisecond },
	}
	h := newHarness(t, testFunction(), cfg)
	// RIC receives the event but never responds until released (after timeout).
	h.setBehavior(func(runtimeapi.Slot, *runtimeapi.Invocation) { <-release })
	h.expectStopRemove() // timeout destroys the slot

	start := time.Now()
	res, err := h.eng.Invoke(t.Context(), "hello", []byte(`{}`))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond)
	assert.Equal(t, "Unhandled", res.FunctionError)
	assert.Contains(t, string(res.Payload), "Task timed out")
}

func TestPerFunctionThrottle(t *testing.T) {
	started := make(chan struct{}, 8)
	release, closeRelease := onceCloser()
	t.Cleanup(closeRelease)

	cfg := Config{PerFunctionConcurrency: 2, MaxConcurrency: 32}
	h := newHarness(t, testFunction(), cfg)
	h.setBehavior(func(slot runtimeapi.Slot, inv *runtimeapi.Invocation) {
		started <- struct{}{}
		<-release
		_ = slot.Respond(inv.RequestID, inv.Payload)
	})
	// Two slots get created and busied; both are torn down on Shutdown.
	h.rt.EXPECT().Stop(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	h.rt.EXPECT().Remove(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = h.eng.Invoke(t.Context(), "hello", []byte(`{}`)) }()
	}
	// Wait until both slots are busy in their handler.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for slots to become busy")
		}
	}

	// A third invocation is over the per-function cap -> immediate throttle.
	_, err := h.eng.Invoke(t.Context(), "hello", []byte(`{}`))
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.CodeTooManyRequests, apiErr.Code)
	assert.Equal(t, 429, apiErr.Status)

	closeRelease()
	wg.Wait()
}

func TestGlobalMaxConcurrencyThrottle(t *testing.T) {
	started := make(chan struct{}, 8)
	release, closeRelease := onceCloser()
	t.Cleanup(closeRelease)

	cfg := Config{PerFunctionConcurrency: 4, MaxConcurrency: 1}
	h := newHarness(t, testFunction(), cfg)
	h.setBehavior(func(slot runtimeapi.Slot, inv *runtimeapi.Invocation) {
		started <- struct{}{}
		<-release
		_ = slot.Respond(inv.RequestID, inv.Payload)
	})
	h.rt.EXPECT().Stop(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	h.rt.EXPECT().Remove(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	done := make(chan struct{})
	go func() { defer close(done); _, _ = h.eng.Invoke(t.Context(), "hello", []byte(`{}`)) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first slot")
	}

	_, err := h.eng.Invoke(t.Context(), "hello", []byte(`{}`))
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.CodeTooManyRequests, apiErr.Code)

	closeRelease()
	<-done
}

// onceCloser returns a channel and a func that closes it at most once.
func onceCloser() (chan struct{}, func()) {
	ch := make(chan struct{})
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }
}

func TestInvokeUnknownFunction(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := storemock.NewMockStore(ctrl)
	rt := runtimemock.NewMockRuntime(ctrl)
	st.EXPECT().GetFunction(gomock.Any(), "ghost").Return(nil, errNotFound)
	eng := New(st, rt, Config{})
	t.Cleanup(func() { _ = eng.Shutdown(context.Background()) })

	_, err := eng.Invoke(t.Context(), "ghost", []byte(`{}`))
	require.ErrorIs(t, err, errNotFound)
}

func TestColdStartCreateFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	st := storemock.NewMockStore(ctrl)
	rt := runtimemock.NewMockRuntime(ctrl)
	fn := testFunction()
	st.EXPECT().GetFunction(gomock.Any(), fn.Name).
		DoAndReturn(func(_ context.Context, _ string) (*model.Function, error) {
			cp := fn
			return &cp, nil
		}).AnyTimes()
	rt.EXPECT().Pull(gomock.Any(), fn.Image).Return(nil)
	rt.EXPECT().Create(gomock.Any(), gomock.Any()).Return("", errBoom)

	eng := New(st, rt, Config{})
	t.Cleanup(func() { _ = eng.Shutdown(context.Background()) })

	_, err := eng.Invoke(t.Context(), "hello", []byte(`{}`))
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 500, apiErr.Status)

	// The failed reservation was released: a subsequent invoke can try again.
	assert.Equal(t, 0, eng.liveSlots())
}

var (
	errNotFound = errors.New("store: function not found")
	errBoom     = errors.New("boom")
)
