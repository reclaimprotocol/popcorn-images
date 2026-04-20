#!/usr/bin/env bash
set -e -o pipefail

# Move to the script's directory so relative paths work regardless of the caller CWD
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
cd "$SCRIPT_DIR"
source ../../shared/ensure-common-build-run-vars.sh chromium-headful

source ../../shared/start-buildkit.sh

REPO_ROOT=$(cd "$SCRIPT_DIR/../../.." && pwd)
eval "$("$REPO_ROOT/scripts/chromium-lock-env.sh" "${PLATFORM:-}")"

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$SCRIPT_DIR/../.." log -1 --pretty=%ct)}"
ARTIFACT_MIRROR_IMAGE="${ARTIFACT_MIRROR_IMAGE:-chromium-base-artifacts:${ARTIFACT_MIRROR_TAG}}"
ARTIFACT_MIRROR_PREFIX="${ARTIFACT_MIRROR_PREFIX:-}"

if [[ -z "$ARTIFACT_MIRROR_PREFIX" && -n "${GITHUB_ARTIFACT_MIRROR_REPO:-}" ]]; then
    ARTIFACT_MIRROR_PREFIX="https://github.com/${GITHUB_ARTIFACT_MIRROR_REPO}/releases/download/${ARTIFACT_MIRROR_TAG}"
fi

(cd "$SCRIPT_DIR/../.." && docker build \
    --build-arg "SOURCE_DATE_EPOCH=0" \
    --build-arg "ARTIFACT_MIRROR_PREFIX=$ARTIFACT_MIRROR_PREFIX" \
    -f images/chromium-headful/artifact-mirror.Dockerfile \
    -t "$ARTIFACT_MIRROR_IMAGE" \
    .)

(cd "$SCRIPT_DIR/../.." && docker build \
    --build-arg "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH" \
    --build-arg "UBUNTU_SNAPSHOT=$UBUNTU_SNAPSHOT" \
    --build-arg "ARTIFACT_MIRROR_IMAGE=$ARTIFACT_MIRROR_IMAGE" \
    -f images/chromium-headful/Dockerfile \
    -t "$IMAGE" \
    .)
