#!/usr/bin/env bash
set -euo pipefail

# Resolve the repo root from this script's own location so it works from any cwd.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

BAZEL="$REPO_ROOT/bin/bazel"
TARGET="//cmd/mini-lambda"

# Resolve the install destination (precedence: env var > first arg > default) while
# still in the caller's cwd, so a relative path argument resolves as the user expects.
INSTALL_DIR="${MINI_LAMBDA_INSTALL_DIR:-${1:-$HOME/.local/bin}}"
mkdir -p "$INSTALL_DIR"
INSTALL_DIR="$(cd -- "$INSTALL_DIR" && pwd)"

# Bazel must run from within the workspace, regardless of the caller's cwd.
cd "$REPO_ROOT"

echo "==> Building $TARGET with the repo toolchain"
if ! "$BAZEL" build "$TARGET"; then
	echo "error: bazel build of $TARGET failed" >&2
	exit 1
fi

# Locate the built binary without hardcoding platform-specific bazel-bin paths.
BINARY="$("$BAZEL" cquery --output=files "$TARGET" 2>/dev/null | head -n 1)"
if [[ -z "${BINARY:-}" || ! -f "$BINARY" ]]; then
	# Fallback: derive from `bazel info bazel-bin` + the known target path.
	BAZEL_BIN="$("$BAZEL" info bazel-bin 2>/dev/null)"
	BINARY="$BAZEL_BIN/cmd/mini-lambda/mini-lambda_/mini-lambda"
fi
if [[ ! -f "$BINARY" ]]; then
	echo "error: could not locate the built mini-lambda binary" >&2
	exit 1
fi

DEST="$INSTALL_DIR/mini-lambda"

# Copy atomically (temp file in the same dir + mv) so an in-use binary updates cleanly.
TMP="$(mktemp "$INSTALL_DIR/.mini-lambda.XXXXXX")"
trap 'rm -f "$TMP"' EXIT
cp "$BINARY" "$TMP"
chmod +x "$TMP"
mv -f "$TMP" "$DEST"
trap - EXIT

echo "==> Installed mini-lambda to $DEST"

# mini-lambda has no --version flag; report the path and a success line instead.
echo "==> mini-lambda installed successfully"

# Warn (not fail) if the destination dir isn't on PATH.
case ":${PATH}:" in
	*":$INSTALL_DIR:"*) ;;
	*) echo "warning: $INSTALL_DIR is not on your \$PATH; add it to run 'mini-lambda' directly" >&2 ;;
esac
