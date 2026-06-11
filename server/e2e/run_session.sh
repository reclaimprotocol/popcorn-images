#!/usr/bin/env bash
#
# End-to-end: drive a full browser-events session from just a session_id,
# bootstrapping the provider config from the Reclaim backend.
#
# Does everything, in order:
#   1. (optional) reclaimbackend unit tests in the golang:1.26 toolchain
#   2. (optional) backend preview  — getSession -> getProvider -> featureFlag
#      (the bootstrap-check command; shows what config will be resolved)
#   3. subscribe to /session/events, then POST /session/start {session_id}
#      against the running popcorn image (this is what navigates the browser)
#   4. poll /session/status until all proofs finish (or timeout)
#   5. print /session/claim, then close the session
#
# Usage:
#   ./run_session.sh <SESSION_ID>
#   ./run_session.sh -s <SESSION_ID> -u http://127.0.0.1:444
#   ./run_session.sh -s <ID> --skip-tests --skip-preview
#
# Flags / env:
#   -s <id>            session id (required; or pass as the first positional arg)
#   -u <url>           popcorn image base URL   (env BASE_URL, default http://127.0.0.1:444)
#   -b <url>           backend base URL for the preview (env RECLAIM_BACKEND_URL, default prod)
#   -t <seconds>       max time to wait for proofs (env TIMEOUT, default 180)
#   --skip-tests       skip the docker unit tests
#   --skip-preview     skip the docker backend preview
#   --skip-proxy-check skip the post-proof egress IP check
#   --keep             do NOT close the session on exit
#   PROXY_CHECK_URL    IP-echo for the egress check (env, default geo.brdtest.com/mygeo.json)
#
# Requires: curl, jq. (docker only for the test/preview steps.)
set -uo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:444}"
BACKEND_URL="${RECLAIM_BACKEND_URL:-}"
TIMEOUT="${TIMEOUT:-180}"
# IP-echo used by the proxy egress check. Defaults to BrightData's geo endpoint
# (returns country/city/asn); any JSON IP-echo works.
PROXY_CHECK_URL="${PROXY_CHECK_URL:-https://geo.brdtest.com/mygeo.json}"
SESSION_ID=""
SKIP_TESTS=0
SKIP_PREVIEW=0
SKIP_PROXY_CHECK=0
KEEP=0

# --- arg parsing (supports long flags + a bare positional session id) --------
ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    -s) SESSION_ID="$2"; shift 2 ;;
    -u) BASE_URL="$2"; shift 2 ;;
    -b) BACKEND_URL="$2"; shift 2 ;;
    -t) TIMEOUT="$2"; shift 2 ;;
    --skip-tests) SKIP_TESTS=1; shift ;;
    --skip-preview) SKIP_PREVIEW=1; shift ;;
    --skip-proxy-check) SKIP_PROXY_CHECK=1; shift ;;
    --keep) KEEP=1; shift ;;
    -h|--help) sed -n '2,28p' "$0"; exit 0 ;;
    *) ARGS+=("$1"); shift ;;
  esac
done
[[ -z "$SESSION_ID" && ${#ARGS[@]} -gt 0 ]] && SESSION_ID="${ARGS[0]}"

command -v curl >/dev/null 2>&1 || { echo "error: curl is required"; exit 1; }
command -v jq   >/dev/null 2>&1 || { echo "error: jq is required"; exit 1; }
[[ -n "$SESSION_ID" ]] || { echo "error: session id is required (pass -s <id> or as arg 1)"; exit 2; }

SERVER_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOCKER_RUN=(docker run --rm
  -v "$SERVER_DIR":/src
  -v popcorn-gomod:/go/pkg/mod
  -v popcorn-gobuild:/root/.cache/go-build
  -w /src golang:1.26)

hr() { printf '\n=== %s ===\n' "$*"; }

# proxy_egress_check drives the LIVE session to an IP-echo URL and prints the
# exit IP/geo the browser actually used, next to the host's direct IP. If they
# differ, the session is proxied; if identical, it egressed direct (proxy off,
# or useProxy=true but the proxy failed to apply — check the container logs for
# "egress proxy" / "Fetch enabled for proxy auth"). Provider-agnostic ground
# truth — does not trust cf-ray (cached) or any header.
proxy_egress_check() {
  local url="$1"
  hr "4.5/5 proxy egress check ($url)"
  local body_file; body_file="$(mktemp)"
  # Capture the network-response body for the echo URL off a short-lived stream.
  ( curl -N --max-time 25 "$BASE_URL/session/events" 2>/dev/null \
    | while IFS= read -r line; do
        [[ "$line" == data:* ]] || continue
        b=$(printf '%s' "${line#data: }" \
          | jq -r --arg u "$url" 'select(.event=="network-response" and (.data.url|startswith($u))) | .data.response_body // empty' 2>/dev/null)
        [[ -n "$b" ]] && { printf '%s' "$b" > "$body_file"; break; }
      done ) &
  local cap=$!
  sleep 1
  curl -sS -m 30 -X POST "$BASE_URL/session/action" -H 'Content-Type: application/json' \
    -d "{\"type\":\"navigate\",\"url\":\"$url\"}" >/dev/null 2>&1
  for _ in $(seq 1 20); do [[ -s "$body_file" ]] && break; sleep 1; done
  kill "$cap" 2>/dev/null; wait "$cap" 2>/dev/null || true

  local summarize='{country: (.country // .country_code), city: (.geo.city // .city), asn: (.asn.org_name // .org // .asn), ip: (.ip // .ip_address // .origin)}'
  if [[ -s "$body_file" ]]; then
    echo "  session egress (through the browser):"
    jq -c "$summarize" "$body_file" 2>/dev/null | sed 's/^/    /' || sed 's/^/    (raw) /' "$body_file"
  else
    echo "  (no response captured for $url within timeout — is the session still open?)"
  fi
  rm -f "$body_file"

  local host_body; host_body="$(curl -s -m 10 "$url" 2>/dev/null)"
  if [[ -n "$host_body" ]]; then
    echo "  host egress (direct, for comparison):"
    printf '%s' "$host_body" | jq -c "$summarize" 2>/dev/null | sed 's/^/    /'
  fi
  echo "  → differ ⇒ proxied; identical ⇒ direct."
}

# --- 1. unit tests -----------------------------------------------------------
if [[ "$SKIP_TESTS" == "0" ]]; then
  if command -v docker >/dev/null 2>&1; then
    hr "1/5 reclaimbackend unit tests"
    "${DOCKER_RUN[@]}" sh -c 'go test ./lib/reclaimbackend/...' || { echo "unit tests failed"; exit 1; }
  else
    echo "(docker not found — skipping unit tests)"
  fi
fi

# --- 2. backend preview (bootstrap-check) ------------------------------------
if [[ "$SKIP_PREVIEW" == "0" ]]; then
  if command -v docker >/dev/null 2>&1; then
    hr "2/5 backend preview (getSession -> getProvider -> featureFlag)"
    PREVIEW_ARGS="-session $SESSION_ID"
    [[ -n "$BACKEND_URL" ]] && PREVIEW_ARGS="$PREVIEW_ARGS -backend $BACKEND_URL"
    "${DOCKER_RUN[@]}" sh -c "go run ./lib/reclaimbackend/cmd/bootstrap-check $PREVIEW_ARGS" \
      || echo "(preview reported a problem — continuing to the live start anyway)"
  else
    echo "(docker not found — skipping backend preview)"
  fi
fi

# --- check the image is up ---------------------------------------------------
hr "3/5 live session against $BASE_URL"
if ! curl -fsS -m 5 "$BASE_URL/session/status" >/dev/null 2>&1; then
  # /session/status returns 404 (no active session) when healthy — that's fine.
  code=$(curl -sS -m 5 -o /dev/null -w '%{http_code}' "$BASE_URL/session/status" 2>/dev/null || echo 000)
  if [[ "$code" == "000" ]]; then
    echo "error: nothing responding at $BASE_URL — is the popcorn image running? (run-docker.sh)"
    exit 1
  fi
fi

# --- close on exit unless --keep ---------------------------------------------
finish() {
  hr "5/5 final claims"
  curl -fsS -m 10 "$BASE_URL/session/claim" 2>/dev/null \
    | jq '{session_id, proof_count: (.proofs | length)}' 2>/dev/null \
    || echo "(no active session / no claims)"
  if [[ "$KEEP" == "0" ]]; then
    echo "==> POST /session/close"
    curl -fsS -m 10 -X POST "$BASE_URL/session/close" -H 'Content-Type: application/json' -d '{}' 2>/dev/null | jq -c . || true
  else
    echo "(--keep set: leaving session open)"
  fi
}
trap finish EXIT

# --- subscribe to events BEFORE starting -------------------------------------
(
  curl -N --max-time "$TIMEOUT" "$BASE_URL/session/events" 2>/dev/null \
  | while IFS= read -r line; do
      [[ "$line" == data:* ]] || continue
      payload="${line#data: }"
      ev=$(printf '%s' "$payload" | jq -r '.event // "?"' 2>/dev/null)
      [[ "$ev" == "heartbeat" ]] && continue
      printf '[event] %s\n' "$(printf '%s' "$payload" | jq -c . 2>/dev/null || printf '%s' "$payload")"
    done
) &
STREAM_PID=$!
sleep 1

# --- start the session -------------------------------------------------------
echo "==> POST /session/start {session_id: $SESSION_ID}"
START_RESP=$(curl -sS -m 30 -X POST "$BASE_URL/session/start" \
  -H 'Content-Type: application/json' -d "{\"session_id\":\"$SESSION_ID\"}")
echo "$START_RESP" | jq . 2>/dev/null || echo "$START_RESP"
if ! echo "$START_RESP" | jq -e '.session_id' >/dev/null 2>&1; then
  echo "error: /session/start failed (a session may already be active — close it and retry)"
  kill "$STREAM_PID" 2>/dev/null
  exit 1
fi

# --- 4. poll status until proofs finish --------------------------------------
hr "4/5 waiting for proofs (timeout ${TIMEOUT}s)"
deadline=$(( $(date +%s) + TIMEOUT ))
last=""
while [[ $(date +%s) -lt $deadline ]]; do
  st=$(curl -fsS -m 10 "$BASE_URL/session/status" 2>/dev/null)
  [[ -z "$st" ]] && { sleep 2; continue; }
  line=$(echo "$st" | jq -rc '{url: .current_url, login: .login.indicator, p: .proofs}' 2>/dev/null)
  [[ "$line" != "$last" ]] && { echo "[status] $line"; last="$line"; }
  read -r exp succ fail prog < <(echo "$st" | jq -r '[.proofs.expected,.proofs.succeeded,.proofs.failed,.proofs.in_progress] | @tsv' 2>/dev/null)
  if [[ -n "$exp" && "$exp" -gt 0 && $(( succ + fail )) -ge "$exp" && "$prog" -eq 0 ]]; then
    echo "==> all $exp matchers settled: $succ succeeded, $fail failed"
    break
  fi
  sleep 2
done

kill "$STREAM_PID" 2>/dev/null
wait "$STREAM_PID" 2>/dev/null || true

# --- 4.5 proxy egress check (ground-truth the exit IP) -----------------------
if [[ "$SKIP_PROXY_CHECK" == "0" ]]; then
  proxy_egress_check "$PROXY_CHECK_URL"
fi

# finish() runs on EXIT (prints claims + closes).
