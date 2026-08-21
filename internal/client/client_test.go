package client_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
)

// fakeStore is an in-memory store.Store.
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

type fakeScheduler struct {
	invoke func(ctx context.Context, name string, payload []byte) (*model.InvokeResult, error)
}

func (f *fakeScheduler) Invoke(ctx context.Context, name string, payload []byte) (*model.InvokeResult, error) {
	return f.invoke(ctx, name, payload)
}

func (f *fakeScheduler) Shutdown(context.Context) error { return nil }

// newServer spins up the real api.New handler behind an httptest.Server and
// returns a client pointed at it.
func newServer(t *testing.T, sched *fakeScheduler) (*client.Client, *fakeStore) {
	t.Helper()
	st := newFakeStore()
	if sched == nil {
		sched = &fakeScheduler{invoke: func(context.Context, string, []byte) (*model.InvokeResult, error) {
			return &model.InvokeResult{Payload: []byte(`null`), StatusCode: 200}, nil
		}}
	}
	srv := httptest.NewServer(api.New(st, sched))
	t.Cleanup(srv.Close)
	return client.New(srv.URL), st
}

func TestClientCreateGetListRoundTrip(t *testing.T) {
	c, _ := newServer(t, nil)

	created, err := c.CreateFunction(api.CreateFunctionRequest{
		FunctionName: "hello",
		Code:         api.Code{ImageUri: "img:1"},
		Environment:  &api.Environment{Variables: map[string]string{"A": "1"}},
		MemorySize:   256,
		Timeout:      12,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.FunctionName != "hello" || created.Code.ImageUri != "img:1" || created.MemorySize != 256 {
		t.Fatalf("unexpected created: %+v", created)
	}

	got, err := c.GetFunction("hello")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Environment == nil || got.Environment.Variables["A"] != "1" {
		t.Fatalf("env not round-tripped: %+v", got.Environment)
	}

	list, err := c.ListFunctions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].FunctionName != "hello" {
		t.Fatalf("unexpected list: %+v", list)
	}
}

func TestClientCreateConflict(t *testing.T) {
	c, _ := newServer(t, nil)
	req := api.CreateFunctionRequest{FunctionName: "dup", Code: api.Code{ImageUri: "img"}}
	if _, err := c.CreateFunction(req); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := c.CreateFunction(req)
	var apiErr *apierror.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *apierror.Error, got %T: %v", err, err)
	}
	if apiErr.Code != apierror.CodeResourceConflict || apiErr.Status != 409 {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
}

func TestClientGetNotFound(t *testing.T) {
	c, _ := newServer(t, nil)
	_, err := c.GetFunction("ghost")
	var apiErr *apierror.Error
	if !errors.As(err, &apiErr) || apiErr.Code != apierror.CodeResourceNotFound {
		t.Fatalf("expected not-found envelope, got %v", err)
	}
}

func TestClientUpdateAndDelete(t *testing.T) {
	c, _ := newServer(t, nil)
	if _, err := c.CreateFunction(api.CreateFunctionRequest{FunctionName: "u", Code: api.Code{ImageUri: "img"}, MemorySize: 128, Timeout: 5}); err != nil {
		t.Fatalf("create: %v", err)
	}

	mem := 1024
	updated, err := c.UpdateFunctionConfiguration("u", api.UpdateFunctionConfigurationRequest{MemorySize: &mem})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.MemorySize != 1024 || updated.Timeout != 5 {
		t.Fatalf("unexpected update: %+v", updated)
	}

	if err := c.DeleteFunction("u"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := c.DeleteFunction("u"); err == nil {
		t.Fatalf("expected error deleting missing function")
	}
}

func TestClientInvokeSuccess(t *testing.T) {
	sched := &fakeScheduler{invoke: func(_ context.Context, name string, payload []byte) (*model.InvokeResult, error) {
		return &model.InvokeResult{Payload: append([]byte("out:"), payload...), StatusCode: 200}, nil
	}}
	c, _ := newServer(t, sched)

	out, err := c.Invoke("fn", []byte(`{"k":1}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if string(out.Payload) != `out:{"k":1}` {
		t.Fatalf("payload = %q", out.Payload)
	}
	if out.FunctionError != "" {
		t.Fatalf("unexpected function error: %q", out.FunctionError)
	}
}

func TestClientInvokeHandlerError(t *testing.T) {
	sched := &fakeScheduler{invoke: func(context.Context, string, []byte) (*model.InvokeResult, error) {
		return &model.InvokeResult{Payload: []byte(`{"errorMessage":"boom"}`), FunctionError: "boom", StatusCode: 200}, nil
	}}
	c, _ := newServer(t, sched)

	out, err := c.Invoke("fn", []byte(`{}`))
	if err != nil {
		t.Fatalf("invoke returned transport error: %v", err)
	}
	if out.FunctionError != "Unhandled" {
		t.Fatalf("FunctionError = %q, want Unhandled", out.FunctionError)
	}
	if string(out.Payload) != `{"errorMessage":"boom"}` {
		t.Fatalf("payload = %q", out.Payload)
	}
}

func TestClientInvokeThrottle(t *testing.T) {
	sched := &fakeScheduler{invoke: func(context.Context, string, []byte) (*model.InvokeResult, error) {
		return nil, apierror.Throttled("at capacity")
	}}
	c, _ := newServer(t, sched)

	_, err := c.Invoke("fn", []byte(`{}`))
	var apiErr *apierror.Error
	if !errors.As(err, &apiErr) || apiErr.Code != apierror.CodeTooManyRequests || apiErr.Status != 429 {
		t.Fatalf("expected throttle envelope, got %v", err)
	}
}

func TestClientDefaultBaseURL(t *testing.T) {
	if got := client.New(""); got == nil {
		t.Fatal("expected client")
	}
}
