// Package api implements the public, AWS-shaped HTTP API for mini-lambda.
//
// The wire-format request/response types live here and ONLY here so that the
// server (this package) and the CLI's HTTP client (internal/client) cannot
// drift: the client imports these same types.
package api

import (
	"time"

	"github.com/ajmcquilkin/mini-lambda/internal/model"
)

// Defaults applied when a create request omits sizing fields.
const (
	DefaultMemorySize = 512
	DefaultTimeout    = 30
)

// Code mirrors AWS Lambda's function code descriptor. mini-lambda is
// image-only, so only ImageUri is meaningful.
type Code struct {
	ImageUri string `json:"ImageUri,omitempty"`
}

// Environment mirrors AWS Lambda's environment block.
type Environment struct {
	Variables map[string]string `json:"Variables,omitempty"`
}

// CreateFunctionRequest is the body of POST /2015-03-31/functions.
type CreateFunctionRequest struct {
	FunctionName string       `json:"FunctionName"`
	Code         Code         `json:"Code"`
	Environment  *Environment `json:"Environment,omitempty"`
	MemorySize   int          `json:"MemorySize,omitempty"`
	Timeout      int          `json:"Timeout,omitempty"`
}

// UpdateFunctionConfigurationRequest is the body of
// PUT /2015-03-31/functions/{name}/configuration. Every field is optional; a
// nil pointer means "leave unchanged".
type UpdateFunctionConfigurationRequest struct {
	Code        *Code        `json:"Code,omitempty"`
	Environment *Environment `json:"Environment,omitempty"`
	MemorySize  *int         `json:"MemorySize,omitempty"`
	Timeout     *int         `json:"Timeout,omitempty"`
}

// FunctionConfiguration is the response body echoing a function's config plus
// timestamps. It is returned by create/get/update and nested in list.
type FunctionConfiguration struct {
	FunctionName  string       `json:"FunctionName"`
	FunctionArn   string       `json:"FunctionArn"`
	Code          Code         `json:"Code"`
	Environment   *Environment `json:"Environment,omitempty"`
	MemorySize    int          `json:"MemorySize"`
	Timeout       int          `json:"Timeout"`
	CreatedAt     time.Time    `json:"CreatedAt"`
	LastModified  time.Time    `json:"LastModified"`
	LastInvokedAt *time.Time   `json:"LastInvokedAt,omitempty"`
}

// ListFunctionsResponse is the body of GET /2015-03-31/functions.
type ListFunctionsResponse struct {
	Functions []FunctionConfiguration `json:"Functions"`
}

// NewFunctionConfiguration projects a stored model.Function onto the wire type.
func NewFunctionConfiguration(fn *model.Function) FunctionConfiguration {
	cfg := FunctionConfiguration{
		FunctionName:  fn.Name,
		FunctionArn:   model.FunctionARN(fn.Name),
		Code:          Code{ImageUri: fn.Image},
		MemorySize:    fn.MemoryMB,
		Timeout:       fn.TimeoutSec,
		CreatedAt:     fn.CreatedAt,
		LastModified:  fn.UpdatedAt,
		LastInvokedAt: fn.LastInvokedAt,
	}
	if len(fn.Env) > 0 {
		cfg.Environment = &Environment{Variables: fn.Env}
	}
	return cfg
}
