package client_test

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
	"github.com/ajmcquilkin/mini-lambda/internal/client"
	"github.com/ajmcquilkin/mini-lambda/internal/model"
	"github.com/ajmcquilkin/mini-lambda/internal/scheduler/schedulermock"
	"github.com/ajmcquilkin/mini-lambda/internal/store"
	"github.com/ajmcquilkin/mini-lambda/internal/store/storemock"
)

// newServer runs the real api.New handler (backed by mocks) behind an
// httptest.Server and returns a client pointed at it.
func newServer(t *testing.T) (*client.Client, *storemock.MockStore, *schedulermock.MockScheduler) {
	t.Helper()
	ctrl := gomock.NewController(t)
	st := storemock.NewMockStore(ctrl)
	sched := schedulermock.NewMockScheduler(ctrl)
	srv := httptest.NewServer(api.New(st, sched))
	t.Cleanup(srv.Close)
	return client.New(srv.URL), st, sched
}

func TestClientCreateGetList(t *testing.T) {
	c, st, _ := newServer(t)
	st.EXPECT().CreateFunction(gomock.Any(), gomock.Any()).Return(nil)
	st.EXPECT().GetFunction(gomock.Any(), "hello").
		Return(&model.Function{Name: "hello", Image: "img:1", Env: map[string]string{"A": "1"}, MemoryMB: 256}, nil)
	st.EXPECT().ListFunctions(gomock.Any()).
		Return([]*model.Function{{Name: "hello", Image: "img:1"}}, nil)

	created, err := c.CreateFunction(api.CreateFunctionRequest{
		FunctionName: "hello",
		Code:         api.Code{ImageUri: "img:1"},
		Environment:  &api.Environment{Variables: map[string]string{"A": "1"}},
		MemorySize:   256,
		Timeout:      12,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", created.FunctionName)
	assert.Equal(t, "img:1", created.Code.ImageUri)
	assert.Equal(t, 256, created.MemorySize)

	got, err := c.GetFunction("hello")
	require.NoError(t, err)
	require.NotNil(t, got.Environment)
	assert.Equal(t, "1", got.Environment.Variables["A"])

	list, err := c.ListFunctions()
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "hello", list[0].FunctionName)
}

func TestClientCreateConflict(t *testing.T) {
	c, st, _ := newServer(t)
	st.EXPECT().CreateFunction(gomock.Any(), gomock.Any()).Return(store.ErrConflict)

	_, err := c.CreateFunction(api.CreateFunctionRequest{FunctionName: "dup", Code: api.Code{ImageUri: "img"}})
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.CodeResourceConflict, apiErr.Code)
	assert.Equal(t, 409, apiErr.Status)
}

func TestClientGetNotFound(t *testing.T) {
	c, st, _ := newServer(t)
	st.EXPECT().GetFunction(gomock.Any(), "ghost").Return(nil, store.ErrNotFound)

	_, err := c.GetFunction("ghost")
	var apiErr *apierror.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, apierror.CodeResourceNotFound, apiErr.Code)
}

func TestClientUpdateAndDelete(t *testing.T) {
	c, st, _ := newServer(t)
	gomock.InOrder(
		st.EXPECT().GetFunction(gomock.Any(), "u").
			Return(&model.Function{Name: "u", Image: "img", MemoryMB: 128, TimeoutSec: 5}, nil),
		st.EXPECT().UpdateFunctionConfiguration(gomock.Any(), gomock.Any()).Return(nil),
	)
	st.EXPECT().DeleteFunction(gomock.Any(), "u").Return(nil)

	mem := 1024
	updated, err := c.UpdateFunctionConfiguration("u", api.UpdateFunctionConfigurationRequest{MemorySize: &mem})
	require.NoError(t, err)
	assert.Equal(t, 1024, updated.MemorySize)
	assert.Equal(t, 5, updated.Timeout)

	require.NoError(t, c.DeleteFunction("u"))
}

func TestClientInvoke(t *testing.T) {
	tests := []struct {
		name        string
		ret         *model.InvokeResult
		retErr      error
		wantErr     bool
		wantPayload string
		wantFnErr   string
		wantCode    string
	}{
		{
			name:        "success",
			ret:         &model.InvokeResult{Payload: []byte(`{"ok":true}`), StatusCode: 200},
			wantPayload: `{"ok":true}`,
		},
		{
			name:        "handler error",
			ret:         &model.InvokeResult{Payload: []byte(`{"errorMessage":"boom"}`), FunctionError: "boom", StatusCode: 200},
			wantPayload: `{"errorMessage":"boom"}`,
			wantFnErr:   "Unhandled",
		},
		{
			name:     "throttle",
			retErr:   apierror.Throttled("at capacity"),
			wantErr:  true,
			wantCode: apierror.CodeTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _, sched := newServer(t)
			sched.EXPECT().Invoke(gomock.Any(), "fn", gomock.Any()).Return(tt.ret, tt.retErr)

			out, err := c.Invoke("fn", []byte(`{}`))
			if tt.wantErr {
				var apiErr *apierror.Error
				require.ErrorAs(t, err, &apiErr)
				assert.Equal(t, tt.wantCode, apiErr.Code)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPayload, string(out.Payload))
			assert.Equal(t, tt.wantFnErr, out.FunctionError)
		})
	}
}

func TestClientDefaultBaseURL(t *testing.T) {
	assert.NotNil(t, client.New(""))
}
