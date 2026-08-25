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
| `--addr` | `127.0.0.1:9000` | Public (AWS-shaped) API listen address. Accepts a `:0` port (e.g. `127.0.0.1:0`) to let the OS pick a free port; discover the resolved port via the READY line or `--port-file`. |
| `--runtime-addr` | `0.0.0.0:0` | Runtime API listen address. Bind `0.0.0.0` so containers can reach it; `:0` picks a free port. |
| `--data` | `~/.mini-lambda` | Directory for the SQLite state database. |
| `--max-concurrency` | `32` | Daemon-wide cap on concurrent slots (containers). |
| `--per-function-concurrency` | `4` | Per-function cap on concurrent slots. |
| `--idle-ttl` | `5m` | How long an idle warm slot survives before it's reaped. |
| `--port-file` | *(none)* | Path to atomically write the resolved listen addresses as JSON at readiness (see below). |
| `--shutdown-timeout` | `20s` | Max time to drain in-flight invocations on `SIGTERM`/`SIGINT` before containers are force-stopped. |
| `--reap-orphans` | `true` | On startup, remove managed containers whose owning daemon has died (pass `--reap-orphans=false` to disable). |

#### Readiness contract (`MINI_LAMBDA_READY`)

The daemon prints exactly one machine-parseable line to stdout when it is genuinely ready — migrations applied, both listeners bound and serving, and docker pinged:

```
MINI_LAMBDA_READY api=<host:port> runtime=<host:port> runtime_reachable=<host:port> pid=<n>
```

- `api` / `runtime` — the *resolved* listen addresses, so with `--addr 127.0.0.1:0` the `api=` field carries the OS-chosen port. `runtime` is the raw Runtime API bind address (e.g. `[::]:9001`).
- `runtime_reachable` — the host:port a **container** dials to reach the Runtime API (the value injected into `AWS_LAMBDA_RUNTIME_API`, e.g. `host.docker.internal:9001`). Use this rather than substituting the host into `runtime` yourself.
- `pid` — the daemon process id. Signal *this* pid to stop the daemon (see the sudo note below).

This line is a stable API: scripts can block on it and parse the fields out of it. It is emitted last, after the human-readable startup log, so seeing it means every readiness step is done.

#### `--port-file`

`--port-file <path>` writes the same resolved values as JSON at the readiness moment, so a supervisor doesn't have to scrape stdout:

```json
{"api":"127.0.0.1:53124","runtime":"[::]:53125","runtime_reachable":"host.docker.internal:53125","pid":8123}
```

It is written atomically (temp file + rename, so a reader never sees a partial document), created world-readable (`0644` — ports aren't secrets, so a cross-user harness can read a file a root/sudo daemon wrote), and best-effort removed on shutdown.

#### Shutdown contract (`MINI_LAMBDA_SHUTDOWN`)

On a clean exit (`SIGTERM`/`SIGINT`) the daemon prints exactly one machine-parseable line as the **last** thing before exiting `0`:

```
MINI_LAMBDA_SHUTDOWN complete   # in-flight invocations drained within --shutdown-timeout
MINI_LAMBDA_SHUTDOWN forced     # the --shutdown-timeout bound elapsed first; teardown was forced
```

A harness can block on this line to know teardown (containers stopped+removed) has actually finished, rather than racing the process exit.

#### `GET /healthz`

The public mux serves `GET /healthz`, returning `200` with a small JSON body once the daemon is serving:

```json
{"status":"ok","docker":"ok"}
```

`status` is the readiness signal CI wants ("the daemon is up") and is always `ok` while serving. `docker` is informational only (`ok` | `unreachable`) and never changes the status code — a daemon serving with docker down is still `200`.

#### Clean teardown and orphan reaping

On `SIGTERM`/`SIGINT` the daemon stops accepting new invocations, drains in-flight ones for up to `--shutdown-timeout`, then stops and removes every container it manages (labeled `mini-lambda.managed=true`), prints the `MINI_LAMBDA_SHUTDOWN` line, and exits `0`.

> **Signalling under `sudo`.** If you launch the daemon via `sudo`, send `SIGTERM` to the daemon process itself, not the `sudo` wrapper — `sudo` does not reliably relay signals to the child. The daemon's pid is in the readiness contract (`pid=` on the READY line and `"pid"` in the port file), so a harness can target it directly: `kill -TERM "$(jq -r .pid ports.json)"`.

That covers graceful exits. A daemon that is `SIGKILL`ed or crashes can't clean up after itself, so every container is also stamped with its owner's identity — a per-run instance id (`mini-lambda.instance`), the owner pid (`mini-lambda.owner-pid`), and the owner host (`mini-lambda.owner-host`). On startup the reaper (default on; `--reap-orphans=false` disables) lists managed containers and removes any whose owning daemon is dead, decided as:

- **own instance** → keep (never touch a container from this run).
- **different `owner-host`** → keep (pid liveness is only meaningful on the same host).
- **owner pid missing/invalid** → remove (unattributable orphan on this host).
- **owner pid still a live process** → keep (a peer daemon owns it — this is what lets parallel CI jobs share one docker host safely).
- **owner pid dead** → remove (its daemon died uncleanly).

Because the rule keys on pid liveness on the same host, two live daemons never reap each other's containers, while a dead daemon's containers are reclaimed by the next startup. Limitations: pid *reuse* can make a dead owner look alive, in which case its orphan is simply left for a later startup (we err toward leaking, never toward reaping a live peer's container); and a docker daemon shared across multiple hosts is out of scope (foreign-host containers are never reaped).

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

Install (or update) the `mini-lambda` binary onto your machine — builds with the repo toolchain and copies it into place:

```sh
./scripts/install.sh                                   # installs to $HOME/.local/bin
MINI_LAMBDA_INSTALL_DIR=/usr/local/bin ./scripts/install.sh   # or override the destination (also accepts a first-arg path)
```

Re-running updates the installed binary in place.

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
