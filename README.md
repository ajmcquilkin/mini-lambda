# mini-lambda

mini-lambda is a local AWS-Lambda emulator: it runs Lambda-compatible container images on your machine and speaks the same HTTP APIs as the real thing. A single Go binary is both the CLI and the daemon — the daemon mirrors Lambda's public data-plane API (`/2015-03-31/*`) for managing and invoking functions, and serves the exact Lambda Runtime API (`2018-06-01`) to your containers. Because the Runtime API is bit-for-bit AWS's, images built on the AWS base images or wired up with the Runtime Interface Client run unmodified. Function configuration is persisted durably in SQLite.

Requirements: Docker (the daemon drives it to pull images and run containers).

## Quickstart

Build the binary (either toolchain works):

```sh
bin/bazel build //cmd/mini-lambda
# or
bin/go build ./cmd/mini-lambda
```

Start the daemon in the background:

```sh
mini-lambda serve &
```

Build a Lambda image. Any image built on an AWS base image works; here's a minimal Python one:

```dockerfile
FROM public.ecr.aws/lambda/python:3.12
COPY app.py ${LAMBDA_TASK_ROOT}
CMD ["app.handler"]
```

```python
# app.py
def handler(event, context):
    return {"echo": event}
```

```sh
docker build -t hello:latest .
```

Register the function from that local image and invoke it:

```sh
mini-lambda function create hello --image hello:latest
mini-lambda function invoke hello --payload '{"name":"ada"}'
# => {"echo": {"name": "ada"}}
```

The invoke above is just sugar over the public HTTP API. The equivalent raw call:

```sh
curl -s -XPOST http://127.0.0.1:9000/2015-03-31/functions/hello/invocations -d '{"name":"ada"}'
```

## CLI reference

`mini-lambda serve` runs the daemon; `mini-lambda function <cmd>` manages functions against a running daemon.

### `serve`

| Flag | Default | Meaning |
|------|---------|---------|
| `--addr` | `127.0.0.1:9000` | Public (AWS-shaped) API listen address. |
| `--runtime-addr` | `0.0.0.0:0` | Runtime API listen address. Bind `0.0.0.0` so containers can reach it; `:0` picks a free port. |
| `--data` | `~/.mini-lambda` | Directory for the SQLite state database. |
| `--max-concurrency` | `32` | Daemon-wide cap on concurrent slots (containers). |
| `--per-function-concurrency` | `4` | Per-function cap on concurrent slots. |
| `--idle-ttl` | `5m` | How long an idle warm slot survives before it's reaped. |

### `function`

All subcommands target the daemon at `--host` (default `127.0.0.1:9000`, overridable via the `MINI_LAMBDA_HOST` env var). Read commands accept `--output table|json` (default `table`).

| Command | Aliases | Args / key flags |
|---------|---------|------------------|
| `create NAME` | | `--image` (required), `--env K=V` (repeatable), `--memory` (MB, default 512), `--timeout` (sec, default 30), `--output` |
| `ls` | `list` | `--output` |
| `get NAME` | | `--output` |
| `update NAME` | | `--image`, `--env K=V` (replaces the set), `--memory`, `--timeout`, `--output`. Only flags you pass are changed. |
| `rm NAME` | `delete` | — |
| `invoke NAME` | | `--payload '<json>'` (inline), `-f/--file <path>` (`-` for stdin); with neither, the payload is read from stdin |
| `logs NAME` | | `-f/--follow` to stream |

Payload sources for `invoke` are tried in order: `--payload`, then `--file` (`-` means stdin), then bare stdin when it isn't a terminal.

## HTTP API

The daemon serves the AWS Lambda data-plane API, so anything that speaks it (the AWS CLI/SDK pointed at `--addr`, or plain `curl`) works:

| Method & path | Action |
|---------------|--------|
| `POST /2015-03-31/functions` | CreateFunction |
| `GET /2015-03-31/functions` | ListFunctions |
| `GET /2015-03-31/functions/{name}` | GetFunction |
| `PUT /2015-03-31/functions/{name}/configuration` | UpdateFunctionConfiguration |
| `DELETE /2015-03-31/functions/{name}` | DeleteFunction |
| `POST /2015-03-31/functions/{name}/invocations` | Invoke |

Separately, the daemon serves the Lambda Runtime API (`2018-06-01`) to the function containers themselves — it is exactly AWS's contract (`invocation/next`, `invocation/{id}/response`, `invocation/{id}/error`, `init/error`), with no extensions or telemetry endpoints.

## Behavior notes

- Invocation is synchronous only — there's no async/event queue; the HTTP call blocks until the handler responds.
- Creating a function whose name already exists is an error (`ResourceConflictException`); use `update` to change one.
- Concurrency is capped, not queued: when the per-function or daemon-wide slot limit is hit, invokes are rejected immediately with `429 TooManyRequestsException` rather than waiting.
- Hitting the function timeout fails that invoke with a `Sandbox.Timedout` error and destroys the slot (its container is torn down).
- Warm containers are reused across invocations; slots that sit idle past `--idle-ttl` are reaped by a background loop.
- `function logs` live-streams stdout/stderr from currently-running slot containers only — there's no stored log history, and output is gone once a slot is destroyed.
- Invocations are ephemeral: only function configuration is durable (in SQLite). There's no record of past invocations or their results.
- No authentication or authorization — mini-lambda is a localhost developer tool, not a multi-tenant service.
- Images must be either locally built or pullable from a public registry; there's no private-registry credential handling.

## Development

This repo carries its own toolchain via [Hermit](https://cashapp.github.io/hermit/). The stubs in `bin/` are committed, so a **fresh clone needs zero preinstalled tools** — no hand-installing bazelisk/bazel/go on each CI or agent VM.

```sh
# Option A: run tools directly through the committed stubs
bin/bazel build //...
bin/bazel test //...
bin/bazel run //:gazelle   # regenerate BUILD files after changing Go sources
bin/go build ./...

# Option B: activate the env so the tools land on your PATH
. bin/activate-hermit
bazel build //...   # now `which bazel` / `which go` point into the Hermit env
```

### What's pinned

| Tool       | Hermit package     | Notes |
|------------|--------------------|-------|
| `bazelisk` | `bazelisk-1.29.0`  | Launcher, exposed as both `bin/bazelisk` and `bin/bazel`. It reads `.bazelversion` and fetches the pinned Bazel **7.4.1** on first use, so the Bazel version stays pinned in `.bazelversion`. |
| `go`       | `go-1.27.0`        | Matches the `go 1.27.0` line in `go.mod`. Bazel builds use rules_go's own SDK; this Go serves `go test`/`go vet`/`go build` and other non-Bazel workflows. |

Nothing else is installed — the Hermit env is intentionally minimal.

> `bin/bazel` is just an alias for bazelisk, which honors `.bazelversion`. Do not pin raw Bazel here; keep the Bazel version in `.bazelversion`.
