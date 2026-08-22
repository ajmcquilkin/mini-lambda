# 02-hello-go

A compiled handler. This is a Go program built with [`aws-lambda-go`](https://github.com/aws/aws-lambda-go), shipped as a small image on the AWS-provided custom-runtime base. It takes `{"name": "..."}` and returns a greeting plus the invocation's request id, which it pulls from the Lambda context. The request id is proof that the fabricated `Lambda-Runtime-*` headers mini-lambda serves over the Runtime API are flowing all the way through to your handler.

This example has its own `go.mod` and is deliberately not part of the repo's Go module or Bazel graph (`examples/` is excluded from Gazelle).

## Files

- `main.go` — the handler. Reads `name`, returns `{"message": "...", "requestId": "..."}`; the request id comes from `lambdacontext.FromContext`.
- `go.mod` / `go.sum` — a standalone module depending only on `aws-lambda-go`.
- `Dockerfile` — multi-stage: build a static binary with `golang:1.23`, then copy it onto `public.ecr.aws/lambda/provided:al2023`. `lambda.Start` implements the Runtime API client, so the provided base needs nothing else.

## Run it

Build the image (from this directory):

```sh
docker build -t hello-go:latest .
```

Start the daemon if it is not already running:

```sh
mini-lambda serve &
```

Register and invoke:

```sh
mini-lambda function create hello-go --image hello-go:latest --memory 256 --timeout 10
mini-lambda function invoke hello-go --payload '{"name":"ada"}'
```

Expected output (the `requestId` is a fresh UUID per invocation, so yours will differ):

```json
{"message":"Hello, ada!","requestId":"3f9c1e7a-2b6d-4f0a-9c3e-1d2b3a4c5d6e"}
```

Omit `name` and it defaults to `world`:

```sh
mini-lambda function invoke hello-go --payload '{}'
# => {"message":"Hello, world!","requestId":"..."}
```

## Clean up

```sh
mini-lambda function rm hello-go
```
