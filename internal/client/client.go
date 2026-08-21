// Package client is the HTTP client the mini-lambda CLI uses to talk to the
// serve daemon's public API. It reuses the wire types defined in internal/api
// so the client and server can never drift, and it decodes AWS-style error
// envelopes back into typed *apierror.Error values.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ajmcquilkin/mini-lambda/internal/api"
	"github.com/ajmcquilkin/mini-lambda/internal/apierror"
)

// DefaultBaseURL is the daemon address assumed when none is supplied.
const DefaultBaseURL = "http://127.0.0.1:9000"

const apiPrefix = "/2015-03-31/functions"

// Client is a thin HTTP client for the mini-lambda public API.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client targeting baseURL. An empty baseURL falls back to
// DefaultBaseURL. A missing scheme is assumed to be http://.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    http.DefaultClient,
	}
}

// InvokeOutput is the result of an Invoke call. FunctionError is non-empty when
// the handler reported an error (the raw error payload is still in Payload).
type InvokeOutput struct {
	Payload       []byte
	FunctionError string
	StatusCode    int
}

// CreateFunction creates a function and returns its configuration.
func (c *Client) CreateFunction(req api.CreateFunctionRequest) (*api.FunctionConfiguration, error) {
	return c.doConfig(http.MethodPost, apiPrefix, req)
}

// ListFunctions returns all functions' configurations.
func (c *Client) ListFunctions() ([]api.FunctionConfiguration, error) {
	resp, err := c.do(http.MethodGet, apiPrefix, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := errorFor(resp); err != nil {
		return nil, err
	}
	var out api.ListFunctionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out.Functions, nil
}

// GetFunction returns a single function's configuration.
func (c *Client) GetFunction(name string) (*api.FunctionConfiguration, error) {
	return c.doConfig(http.MethodGet, apiPrefix+"/"+name, nil)
}

// UpdateFunctionConfiguration applies partial configuration changes.
func (c *Client) UpdateFunctionConfiguration(name string, req api.UpdateFunctionConfigurationRequest) (*api.FunctionConfiguration, error) {
	return c.doConfig(http.MethodPut, apiPrefix+"/"+name+"/configuration", req)
}

// DeleteFunction removes a function.
func (c *Client) DeleteFunction(name string) error {
	resp, err := c.do(http.MethodDelete, apiPrefix+"/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return errorFor(resp)
}

// Invoke invokes a function with the raw event payload. A handler error is not
// returned as an error: it surfaces via InvokeOutput.FunctionError so callers
// can still read the raw error payload. Transport/API errors (e.g. unknown
// function, throttle) are returned as typed errors.
func (c *Client) Invoke(name string, payload []byte) (*InvokeOutput, error) {
	resp, err := c.doRaw(http.MethodPost, apiPrefix+"/"+name+"/invocations", payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := errorFor(resp); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read invocation response: %w", err)
	}
	return &InvokeOutput{
		Payload:       body,
		FunctionError: resp.Header.Get("X-Amz-Function-Error"),
		StatusCode:    resp.StatusCode,
	}, nil
}

// doConfig performs a request whose success body is a FunctionConfiguration.
func (c *Client) doConfig(method, path string, body any) (*api.FunctionConfiguration, error) {
	resp, err := c.do(method, path, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := errorFor(resp); err != nil {
		return nil, err
	}
	var cfg api.FunctionConfiguration
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &cfg, nil
}

// do issues a request with an optional JSON-encoded body.
func (c *Client) do(method, path string, body any) (*http.Response, error) {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		raw = b
	}
	return c.doRaw(method, path, raw)
}

// doRaw issues a request with a raw (already-encoded) body.
func (c *Client) doRaw(method, path string, raw []byte) (*http.Response, error) {
	var rdr io.Reader
	if raw != nil {
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// errorFor returns a typed *apierror.Error for non-2xx responses, decoding the
// AWS-style envelope, and nil otherwise. The response body is consumed on error.
func errorFor(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(resp.Body)

	var apiErr apierror.Error
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Code != "" {
		apiErr.Status = resp.StatusCode
		return &apiErr
	}
	return &apierror.Error{
		Code:    apierror.CodeService,
		Message: strings.TrimSpace(string(body)),
		Status:  resp.StatusCode,
	}
}
