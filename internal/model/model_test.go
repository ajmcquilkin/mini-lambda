package model

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFunctionARN(t *testing.T) {
	assert.Equal(t,
		"arn:aws:lambda:local:000000000000:function:my-fn",
		FunctionARN("my-fn"),
	)
}

func TestNewInvocationContextARN(t *testing.T) {
	fn := &Function{Name: "hello", TimeoutSec: 3}
	ctx := NewInvocationContext(fn)
	assert.Equal(t,
		"arn:aws:lambda:local:000000000000:function:hello",
		ctx.InvokedFunctionArn,
	)
}

func TestNewInvocationContextDeadline(t *testing.T) {
	const timeoutSec = 30
	fn := &Function{Name: "hello", TimeoutSec: timeoutSec}

	before := time.Now()
	ctx := NewInvocationContext(fn)
	after := time.Now()

	// DeadlineMs must land in [before+timeout, after+timeout]; the surrounding
	// timestamps bound the now() the constructor observed.
	lo := before.Add(timeoutSec * time.Second).UnixMilli()
	hi := after.Add(timeoutSec * time.Second).UnixMilli()
	assert.GreaterOrEqual(t, ctx.DeadlineMs, lo)
	assert.LessOrEqual(t, ctx.DeadlineMs, hi)
}

func TestNewInvocationContextZeroTimeout(t *testing.T) {
	fn := &Function{Name: "hello", TimeoutSec: 0}

	before := time.Now()
	ctx := NewInvocationContext(fn)
	after := time.Now()

	// A zero timeout deadlines at "now".
	assert.GreaterOrEqual(t, ctx.DeadlineMs, before.UnixMilli())
	assert.LessOrEqual(t, ctx.DeadlineMs, after.UnixMilli())
}

func TestDeadlineMs(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name       string
		timeoutSec int
		want       int64
	}{
		{"zero timeout deadlines now", 0, base.UnixMilli()},
		{"positive timeout adds whole seconds", 3, base.Add(3 * time.Second).UnixMilli()},
		{"large timeout", 900, base.Add(900 * time.Second).UnixMilli()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeadlineMs(base, tt.timeoutSec))
		})
	}
}

func TestNewInvocationContextAtDeadlineIsExact(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	fn := &Function{Name: "hello", TimeoutSec: 30}

	ctx := NewInvocationContextAt(base, fn)

	// With an injected clock the deadline is exact: no tolerance window.
	assert.Equal(t, base.Add(30*time.Second).UnixMilli(), ctx.DeadlineMs)
	assert.Equal(t, FunctionARN("hello"), ctx.InvokedFunctionArn)
	assert.Empty(t, ctx.TraceID)
	_, err := uuid.Parse(ctx.RequestID)
	require.NoError(t, err)
}

func TestNewInvocationContextRequestIDIsUniqueUUID(t *testing.T) {
	fn := &Function{Name: "hello", TimeoutSec: 3}

	c1 := NewInvocationContext(fn)
	c2 := NewInvocationContext(fn)

	id1, err := uuid.Parse(c1.RequestID)
	require.NoError(t, err)
	id2, err := uuid.Parse(c2.RequestID)
	require.NoError(t, err)

	assert.NotEqual(t, id1, id2, "each invocation gets a fresh RequestID")
}

func TestNewInvocationContextTraceIDEmpty(t *testing.T) {
	fn := &Function{Name: "hello", TimeoutSec: 3}
	// TraceID is not populated by the constructor (X-Ray is a no-op here).
	assert.Empty(t, NewInvocationContext(fn).TraceID)
}
