# 03-flaky-worker

This example exercises the interesting runtime semantics. It is a single Python handler whose behavior is controlled by environment variables passed at `create` time:

- `MODE=ok` (default) returns a normal JSON result.
- `MODE=fail` raises, which surfaces as a handler error (`X-Amz-Function-Error: Unhandled`).
- `SLEEP_SECONDS=N` sleeps for `N` seconds before returning. Set it above the function's `--timeout` to trigger a timeout, which fails the invoke with `Sandbox.Timedout` and destroys the slot.

It also shows per-function concurrency throttling: fire more concurrent invokes than the per-function cap (default 4) and the excess are rejected immediately with `429 TooManyRequestsException` rather than queued.

One thing to know up front: mini-lambda bakes a function's config (env vars, timeout) into the sandbox at cold start, and warm sandboxes are keyed by function name and reused across invokes. A warm slot even survives deleting and recreating the function under the same name (delete removes the stored config, not the pooled container, which lingers until it is idle-reaped). So the demos below each use a distinct function name to guarantee a cold start with exactly the config that demo needs.

## Files

- `app.py` — reads `MODE` and `SLEEP_SECONDS` from the environment and acts accordingly.
- `Dockerfile` — `public.ecr.aws/lambda/python:3.12` base, same shape as example 01.

## Build

```sh
docker build -t flaky-worker:latest .
```

Start the daemon if it is not already running:

```sh
mini-lambda serve &
```

## Demo 1: normal return (MODE=ok)

```sh
mini-lambda function create flaky-ok --image flaky-worker:latest --env MODE=ok --memory 256 --timeout 10
mini-lambda function invoke flaky-ok --payload '{"hello":"world"}'
```

Expected output (the `requestId` differs per invocation):

```json
{"mode": "ok", "sleptSeconds": 0.0, "requestId": "79bb1b2b-...", "event": {"hello": "world"}}
```

## Demo 2: handler error (MODE=fail)

```sh
mini-lambda function create flaky-fail --image flaky-worker:latest --env MODE=fail --memory 256 --timeout 10
```

Invoking via the CLI prints the raw error payload to stderr and exits non-zero, mirroring the AWS CLI:

```sh
mini-lambda function invoke flaky-fail --payload '{}'
# stderr:
# {"errorMessage": "flaky worker asked to fail (MODE=fail)", "errorType": "RuntimeError", "requestId": "...", "stackTrace": ["  File \"/var/task/app.py\", line 15, in handler\n    raise RuntimeError(\"flaky worker asked to fail (MODE=fail)\")\n"]}
# function error: Unhandled
# exit status: 1
```

To see the wire-level detail — HTTP 200 with the `X-Amz-Function-Error` header set — hit the raw API with `curl -i`:

```sh
curl -i -s -XPOST http://127.0.0.1:9000/2015-03-31/functions/flaky-fail/invocations -d '{}'
# HTTP/1.1 200 OK
# Content-Type: application/json
# X-Amz-Function-Error: Unhandled
# ...
# {"errorMessage": "flaky worker asked to fail (MODE=fail)", "errorType": "RuntimeError", ...}
```

## Demo 3: timeout (SLEEP_SECONDS > timeout)

Create a function with a 5s timeout but a handler that sleeps 10s:

```sh
mini-lambda function create flaky-slow --image flaky-worker:latest --env MODE=ok --env SLEEP_SECONDS=10 --memory 256 --timeout 5
```

The invoke blocks until the timeout fires at ~5s (the call takes a few seconds longer while the timed-out slot is torn down), then fails with a timeout error:

```sh
curl -i -s -XPOST http://127.0.0.1:9000/2015-03-31/functions/flaky-slow/invocations -d '{}'
# HTTP/1.1 200 OK
# X-Amz-Function-Error: Unhandled
# ...
# {"errorMessage":"<request-id> Task timed out after 5.00 seconds","errorType":"Sandbox.Timedout"}
```

When a function times out, mini-lambda destroys the slot (tears the container down), so the next invoke cold-starts a fresh sandbox — there is no half-finished handler left running.

## Demo 4: concurrency throttling (429)

The per-function concurrency cap is 4 by default. Create a function whose handler sleeps long enough to hold its slot, then fire 8 invokes at once:

```sh
mini-lambda function create flaky-throttle --image flaky-worker:latest --env MODE=ok --env SLEEP_SECONDS=5 --memory 256 --timeout 30

for i in $(seq 1 8); do
  curl -s -o /dev/null -w "%{http_code}\n" \
    -XPOST http://127.0.0.1:9000/2015-03-31/functions/flaky-throttle/invocations -d '{}' &
done
wait
```

Four requests grab slots and return `200`; the other four exceed the per-function cap and are rejected immediately (order varies):

```
429
429
429
429
200
200
200
200
```

The `429` bodies are AWS-shaped `TooManyRequestsException` envelopes:

```json
{"__type":"TooManyRequestsException","message":"function concurrency limit reached for flaky-throttle","Reason":"ReservedFunctionConcurrentInvocationLimitExceeded"}
```

Note that mini-lambda rejects rather than queues: excess invokes fail fast instead of waiting for a slot.

## Clean up

```sh
mini-lambda function rm flaky-ok
mini-lambda function rm flaky-fail
mini-lambda function rm flaky-slow
mini-lambda function rm flaky-throttle
```
