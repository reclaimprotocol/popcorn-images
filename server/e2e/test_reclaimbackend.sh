#!/usr/bin/env bash
#
# Test the reclaimbackend client and the in-image bootstrap path.
#
# Runs everything inside the golang:1.26 Docker toolchain with the shared
# popcorn-gomod / popcorn-gobuild volumes (the project's build convention), so
# you don't need a local Go install.
#
# Usage:
#   ./test_reclaimbackend.sh                 # unit tests only (no network)
#   ./test_reclaimbackend.sh -s <SESSION_ID> # unit tests + live bootstrap check
#   ./test_reclaimbackend.sh -s <ID> -a      # also build+vet the whole module
#
# Flags:
#   -s <id>   live, non-terminal session id -> runs the bootstrap-check command
#             (getSession -> getProvider -> featureFlag) against the real backend
#   -b <url>  backend base URL for the live check (default: prod)
#   -a        run a full module build + vet + api tests too
#   -v        verbose test output (go test -v)
#
# Requires: docker.
set -uo pipefail

SESSION_ID=""
BACKEND_URL=""
FULL=0
VERBOSE=""

while getopts "s:b:av" opt; do
  case "$opt" in
    s) SESSION_ID="$OPTARG" ;;
    b) BACKEND_URL="$OPTARG" ;;
    a) FULL=1 ;;
    v) VERBOSE="-v" ;;
    *) echo "see header for usage" >&2; exit 2 ;;
  esac
done

# Resolve the server/ directory (this script lives in server/e2e/).
SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

DOCKER_RUN=(docker run --rm
  -v "$SERVER_DIR":/src
  -v popcorn-gomod:/go/pkg/mod
  -v popcorn-gobuild:/root/.cache/go-build
  -w /src
  golang:1.26)

run() {
  echo
  echo "==> $*"
  "${DOCKER_RUN[@]}" sh -c "$*"
}

set -e

# 1. Unit tests (stubbed backend, no network).
run "go vet ./lib/reclaimbackend/... && go test $VERBOSE ./lib/reclaimbackend/..."

# 2. Optional: full module build + vet + api tests.
if [[ "$FULL" == "1" ]]; then
  run "go build ./... && go vet ./... && go test $VERBOSE ./cmd/api/api/..."
fi

# 3. Optional: live bootstrap check against the real backend.
if [[ -n "$SESSION_ID" ]]; then
  ARGS="-session $SESSION_ID"
  [[ -n "$BACKEND_URL" ]] && ARGS="$ARGS -backend $BACKEND_URL"
  run "go run ./lib/reclaimbackend/cmd/bootstrap-check $ARGS"
fi

echo
echo "All checks passed."
