#!/usr/bin/env bash
set -e -o pipefail

# Move to the script's directory so relative paths work regardless of the caller CWD
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
cd "$SCRIPT_DIR"
source ../../shared/ensure-common-build-run-vars.sh chromium-headful

source ../../shared/start-buildkit.sh

# SOURCE_DATE_EPOCH pins the timestamp BuildKit applies to file mtimes and the
# image config's `created` field. Combined with `rewrite-timestamp=true` on the
# output, this makes the image bit-reproducible across rebuilds from the same
# commit. We use the commit timestamp so the value is deterministic per commit.
REPO_ROOT=$(cd "$SCRIPT_DIR/../.." && pwd)
export SOURCE_DATE_EPOCH=$(git -C "$REPO_ROOT" log -1 --pretty=%ct)

# Ensure a docker-container builder exists (the default `docker` driver does
# not support `rewrite-timestamp` or OCI tar output).
if ! docker buildx inspect repro >/dev/null 2>&1; then
    docker buildx create --name repro --driver docker-container --bootstrap >/dev/null
fi

# Build to a docker-format tar with rewrite-timestamp, then load into the local
# docker daemon so the resulting image is tagged as $IMAGE for downstream use.
# Provenance and SBOM attestations are disabled because they intentionally
# include build-time nonces that would defeat reproducibility.
OUT_TAR="${REPRO_OUT_TAR:-/tmp/${IMAGE//[\/:]/_}.tar}"
(cd "$REPO_ROOT" && docker buildx build \
    --builder repro \
    --build-arg SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" \
    --provenance=false --sbom=false \
    --output "type=docker,name=$IMAGE,dest=$OUT_TAR,rewrite-timestamp=true" \
    -f images/chromium-headful/Dockerfile .)
docker load -i "$OUT_TAR"
echo "Built reproducible image: $IMAGE (tar at $OUT_TAR, SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH)"
