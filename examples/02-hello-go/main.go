package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
)

// Request is the event shape the handler accepts.
type Request struct {
	Name string `json:"name"`
}

// Response is what the handler returns.
type Response struct {
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

func handle(ctx context.Context, req Request) (Response, error) {
	name := req.Name
	if name == "" {
		name = "world"
	}

	// The request id comes from the Lambda-Runtime-Aws-Request-Id header that
	// mini-lambda fabricates for each invocation and serves over the Runtime
	// API. aws-lambda-go reads it and exposes it via the invocation context.
	var requestID string
	if lc, ok := lambdacontext.FromContext(ctx); ok {
		requestID = lc.AwsRequestID
	}

	return Response{
		Message:   fmt.Sprintf("Hello, %s!", name),
		RequestID: requestID,
	}, nil
}

func main() {
	lambda.Start(handle)
}
