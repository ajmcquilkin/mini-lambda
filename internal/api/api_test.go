package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

// fakeStore is an in-memory store.Store honoring the ErrConflict/ErrNotFound
// sentinels.
type fakeStore struct {
	fns map[string]*model.Function
}

func newFakeStore() *fakeStore { return &fakeStore{fns: map[string]*model.Function{}} }

func (f *fakeStore) Migrate(context.Context) error { return nil }

func (f *fakeStore) CreateFunction(_ context.Context, fn *model.Function) error {
	if _, ok := f.fns[fn.Name]; ok {
		return store.ErrConflict
	}
	cp := *fn
	f.fns[fn.Name] = &cp
	return nil
}

func (f *fakeStore) GetFunction(_ context.Context, name string) (*model.Function, error) {
	fn, ok := f.fns[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *fn
	return &cp, nil
}

func (f *fakeStore) ListFunctions(context.Context) ([]*model.Function, error) {
	out := make([]*model.Function, 0, len(f.fns))
	for _, fn := range f.fns {
		cp := *fn
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeStore) UpdateFunctionConfiguration(_ context.Context, fn *model.Function) error {
	if _, ok := f.fns[fn.Name]; !ok {
		return store.ErrNotFound
	}
	cp := *fn
	f.fns[fn.Name] = &cp
	return nil
}

func (f *fakeStore) DeleteFunction(_ context.Context, name string) error {
	if _, ok := f.fns[name]; !ok {
		return store.ErrNotFound
	}
	delete(f.fns, name)
	return nil
}

func (f *fakeStore) Close() error { return nil }

// fakeScheduler dispatches Invoke to a configurable function.
type fakeScheduler struct {
	invoke func(ctx context.Context, name string, payload []byte) (*model.InvokeResult, error)
}

func (f *fakeScheduler) Invoke(ctx context.Context, name string, payload []byte) (*model.InvokeResult, error) {
	return f.invoke(ctx, name, payload)
}

func (f *fakeScheduler) Shutdown(context.Context) error { return nil }

func do(t *testing.T, h http.Handler, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateFunction(t *testing.T) {
	h := New(newFakeStore(), &fakeScheduler{})
	rec := do(t, h, http.MethodPost, routeFunctions, `{"FunctionName":"hello","Code":{"ImageUri":"img:latest"},"Environment":{"Variables":{"A":"1"}},"MemorySize":256,"Timeout":15}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var cfg FunctionConfiguration
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.FunctionName != "hello" || cfg.Code.ImageUri != "img:latest" || cfg.MemorySize != 256 || cfg.Timeout != 15 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Environment == nil || cfg.Environment.Variables["A"] != "1" {
		t.Fatalf("environment not echoed: %+v", cfg.Environment)
	}
	if cfg.FunctionArn == "" {
		t.Fatalf("expected FunctionArn to be populated")
	}
}

func TestCreateFunctionDefaults(t *testing.T) {
	h := New(newFakeStore(), &fakeScheduler{})
	rec := do(t, h, http.MethodPost, routeFunctions, `{"FunctionName":"h","Code":{"ImageUri":"img"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var cfg FunctionConfiguration
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.MemorySize != DefaultMemorySize || cfg.Timeout != DefaultTimeout {
		t.Fatalf("defaults not applied: mem=%d timeout=%d", cfg.MemorySize, cfg.Timeout)
	}
}

func TestCreateFunctionValidation(t *testing.T) {
	h := New(newFakeStore(), &fakeScheduler{})
	rec := do(t, h, http.MethodPost, routeFunctions, `{"Code":{"ImageUri":"img"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertErrorCode(t, rec, apierror.CodeInvalidParameterValue)
}

func TestCreateFunctionConflict(t *testing.T) {
	st := newFakeStore()
	h := New(st, &fakeScheduler{})
	body := `{"FunctionName":"dup","Code":{"ImageUri":"img"}}`
	if rec := do(t, h, http.MethodPost, routeFunctions, body); rec.Code != http.StatusCreated {
		t.Fatalf("setup create status = %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, routeFunctions, body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	assertErrorCode(t, rec, apierror.CodeResourceConflict)
}

func TestGetFunctionNotFound(t *testing.T) {
	h := New(newFakeStore(), &fakeScheduler{})
	rec := do(t, h, http.MethodGet, routeFunctions+"/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, apierror.CodeResourceNotFound)
}

func TestListFunctions(t *testing.T) {
	st := newFakeStore()
	h := New(st, &fakeScheduler{})
	do(t, h, http.MethodPost, routeFunctions, `{"FunctionName":"a","Code":{"ImageUri":"img"}}`)
	do(t, h, http.MethodPost, routeFunctions, `{"FunctionName":"b","Code":{"ImageUri":"img"}}`)

	rec := do(t, h, http.MethodGet, routeFunctions, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp ListFunctionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Functions) != 2 {
		t.Fatalf("got %d functions, want 2", len(resp.Functions))
	}
}

func TestUpdateFunctionConfiguration(t *testing.T) {
	st := newFakeStore()
	h := New(st, &fakeScheduler{})
	do(t, h, http.MethodPost, routeFunctions, `{"FunctionName":"c","Code":{"ImageUri":"img"},"MemorySize":128,"Timeout":5}`)

	rec := do(t, h, http.MethodPut, routeFunctions+"/c/configuration", `{"MemorySize":1024,"Environment":{"Variables":{"K":"V"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var cfg FunctionConfiguration
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.MemorySize != 1024 {
		t.Fatalf("memory not updated: %d", cfg.MemorySize)
	}
	if cfg.Timeout != 5 {
		t.Fatalf("timeout should be unchanged: %d", cfg.Timeout)
	}
	if cfg.Environment == nil || cfg.Environment.Variables["K"] != "V" {
		t.Fatalf("env not updated: %+v", cfg.Environment)
	}
}

func TestUpdateFunctionNotFound(t *testing.T) {
	h := New(newFakeStore(), &fakeScheduler{})
	rec := do(t, h, http.MethodPut, routeFunctions+"/nope/configuration", `{"MemorySize":10}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteFunction(t *testing.T) {
	st := newFakeStore()
	h := New(st, &fakeScheduler{})
	do(t, h, http.MethodPost, routeFunctions, `{"FunctionName":"d","Code":{"ImageUri":"img"}}`)

	rec := do(t, h, http.MethodDelete, routeFunctions+"/d", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", rec.Body.String())
	}
	// second delete -> 404
	if rec := do(t, h, http.MethodDelete, routeFunctions+"/d", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rec.Code)
	}
}

func TestInvokeSuccess(t *testing.T) {
	sched := &fakeScheduler{invoke: func(_ context.Context, name string, payload []byte) (*model.InvokeResult, error) {
		if name != "fn" {
			t.Fatalf("unexpected name %q", name)
		}
		return &model.InvokeResult{Payload: append([]byte("echo:"), payload...), StatusCode: 200}, nil
	}}
	h := New(newFakeStore(), sched)

	rec := do(t, h, http.MethodPost, routeFunctions+"/fn/invocations", `{"x":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `echo:{"x":1}` {
		t.Fatalf("payload = %q", got)
	}
	if rec.Header().Get("X-Amz-Function-Error") != "" {
		t.Fatalf("unexpected function-error header")
	}
}

func TestInvokeHandlerError(t *testing.T) {
	sched := &fakeScheduler{invoke: func(context.Context, string, []byte) (*model.InvokeResult, error) {
		return &model.InvokeResult{
			Payload:       []byte(`{"errorMessage":"boom"}`),
			FunctionError: "boom",
			StatusCode:    200,
		}, nil
	}}
	h := New(newFakeStore(), sched)

	rec := do(t, h, http.MethodPost, routeFunctions+"/fn/invocations", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Amz-Function-Error"); got != "Unhandled" {
		t.Fatalf("X-Amz-Function-Error = %q, want Unhandled", got)
	}
	if got := rec.Body.String(); got != `{"errorMessage":"boom"}` {
		t.Fatalf("payload = %q", got)
	}
}

func TestInvokeThrottle(t *testing.T) {
	sched := &fakeScheduler{invoke: func(context.Context, string, []byte) (*model.InvokeResult, error) {
		return nil, apierror.Throttled("at capacity")
	}}
	h := New(newFakeStore(), sched)

	rec := do(t, h, http.MethodPost, routeFunctions+"/fn/invocations", `{}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	assertErrorCode(t, rec, apierror.CodeTooManyRequests)
}

func TestInvokeUnknownFunction(t *testing.T) {
	sched := &fakeScheduler{invoke: func(context.Context, string, []byte) (*model.InvokeResult, error) {
		return nil, store.ErrNotFound
	}}
	h := New(newFakeStore(), sched)

	rec := do(t, h, http.MethodPost, routeFunctions+"/ghost/invocations", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, apierror.CodeResourceNotFound)
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var e apierror.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error envelope: %v (body=%s)", err, rec.Body.String())
	}
	if e.Code != code {
		t.Fatalf("error code = %q, want %q", e.Code, code)
	}
	if rec.Header().Get("X-Amzn-Errortype") != code {
		t.Fatalf("X-Amzn-Errortype = %q, want %q", rec.Header().Get("X-Amzn-Errortype"), code)
	}
}
