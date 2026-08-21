# mini-lambda

## Toolchain

This repo carries its own toolchain via [Hermit](https://cashapp.github.io/hermit/).
The stubs in `bin/` are committed, so a **fresh clone needs zero preinstalled
tools** — no hand-installing bazelisk/bazel/go on each CI or agent VM.

### Fresh-clone quickstart

```sh
# Option A: run tools directly through the committed stubs
bin/bazel build //...
bin/bazel run //:gazelle
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

> `bin/bazel` is just an alias for bazelisk, which honors `.bazelversion`. Do not
> pin raw Bazel here; keep the Bazel version in `.bazelversion`.
