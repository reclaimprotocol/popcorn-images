#!/usr/bin/env bash
set -e -o pipefail

# Move to the script's directory so relative paths work regardless of the caller CWD
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
cd "$SCRIPT_DIR"
source ../../shared/ensure-common-build-run-vars.sh chromium-headful

source ../../shared/start-buildkit.sh

REPO_ROOT=$(cd "$SCRIPT_DIR/../../.." && pwd)
PLATFORM="${PLATFORM:-linux/amd64}"
eval "$("$REPO_ROOT/scripts/chromium-lock-env.sh" "$PLATFORM")"

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$SCRIPT_DIR/../.." log -1 --pretty=%ct)}"
ARTIFACT_MIRROR_PREFIX="${ARTIFACT_MIRROR_PREFIX:-}"

ARTIFACT_LAYOUT_DIR="$(mktemp -d)"
cleanup() {
    rm -rf "$ARTIFACT_LAYOUT_DIR"
}
trap cleanup EXIT

ARTIFACT_MIRROR_PREFIX="$ARTIFACT_MIRROR_PREFIX" \
    "$REPO_ROOT/scripts/prepare-chromium-artifacts.sh" "$ARTIFACT_LAYOUT_DIR" "$TARGET_PLATFORM"

(cd "$SCRIPT_DIR/../.." && docker buildx build \
    --platform "$TARGET_PLATFORM" \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    --build-arg "UBUNTU_SNAPSHOT=$UBUNTU_SNAPSHOT" \
    --build-arg "ARTIFACT_MIRROR_IMAGE=artifact-mirror" \
    --build-context "artifact-mirror=$ARTIFACT_LAYOUT_DIR" \
    -f images/chromium-headful/Dockerfile \
    -t "$IMAGE" \
    --load \
    .)
echo "Built image: $IMAGE (SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH)"
