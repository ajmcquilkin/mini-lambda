# Examples

Each subdirectory is a self-contained example lambda — handler source, a `Dockerfile`, and a `README.md` with copy-pasteable commands — that runs on mini-lambda unmodified because mini-lambda speaks the same AWS APIs as the real thing. Work through them in order; they climb in complexity from a trivial echo to a worker that exercises the error, timeout, and throttling semantics. None of this directory is part of the repo's Go module or Bazel build (`examples/` is excluded from Gazelle), so the example modules stay independent of the daemon's own dependencies.

## Progression

- [`01-echo-python`](01-echo-python/) — the minimal case. A one-file Python handler on the AWS Lambda Python base image that echoes the event back. Shows build, create, invoke.
- [`02-hello-go`](02-hello-go/) — a compiled handler. Go with `aws-lambda-go` in its own standalone module, built into a small image on the AWS-provided custom-runtime base. Returns a greeting plus the invocation request id pulled from the Lambda context, showing the fabricated `Lambda-Runtime-*` headers flowing through.
- [`03-flaky-worker`](03-flaky-worker/) — the interesting runtime semantics. A Python handler whose env vars drive normal returns, handler errors (`X-Amz-Function-Error`), and timeouts (`Sandbox.Timedout` plus slot destruction), with a concurrency loop that shows `429` throttling past the per-function cap.
