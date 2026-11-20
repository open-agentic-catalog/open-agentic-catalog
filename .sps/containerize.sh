#!/usr/bin/env bash

set -euo pipefail

enter "${BASH_SOURCE[0]}"

BASE_DIR=$(dirname "$(dirname "$(realpath "${BASH_SOURCE[0]}")")")
cd "${BASE_DIR}"

# Central hardened base images (digest-pinned), maintained by the image-hardening team.
BASE_IMAGES_FILE="$(mktemp)"
trap 'rm -f "${BASE_IMAGES_FILE}"' EXIT
curl -fsS \
  -H "Accept: application/vnd.github.v3.raw" \
  -H "Authorization: token $(get_secret git-token)" \
  -LJ "https://raw.github.ibm.com/WatsonOrchestrate/wxo-security-image-hardening/refs/heads/develop/redhat/base-images.yaml" \
  -o "${BASE_IMAGES_FILE}"

CENTRAL_BASE_IMAGE="$(yq '.base_images.BASE_UBI9_GO_TOOLSET' "${BASE_IMAGES_FILE}")"
RUNTIME_BASE_IMAGE="$(yq '.base_images.BASE_UBI9_UBI_MINIMAL' "${BASE_IMAGES_FILE}")"
for image in "${CENTRAL_BASE_IMAGE}" "${RUNTIME_BASE_IMAGE}"; do
  if [[ -z "${image}" || "${image}" == "null" ]]; then
    die "Central base image is missing from base-images.yaml"
  fi
done

echo "Using build base image:   ${CENTRAL_BASE_IMAGE}"
echo "Using runtime base image: ${RUNTIME_BASE_IMAGE}"

# Build identity baked into the binaries and labels, as the Makefile does.
VERSION="$(tr -d '[:space:]' < VERSION)"
COMMIT="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
if [[ -n "$(git status --porcelain 2>/dev/null)" ]]; then
  SOURCE_STATE="modified"
else
  SOURCE_STATE="clean"
fi
echo "Build identity: version=${VERSION} commit=${COMMIT} state=${SOURCE_STATE}"

IMAGE_NAME="open-agentic-catalog"

login_docker_registry "$(get_env "wai-registry-development")"

# The central base images are served from Artifactory.
export ARTIFACTORY_USERNAME="WO.Pipelines@ibm.com"
ARTIFACTORY_PASSWORD="$(get_secret artifactory-password "")"  # pragma: allowlist secret
export ARTIFACTORY_PASSWORD
docker login na.artifactory.swg-devops.com -u "${ARTIFACTORY_USERNAME}" --password-stdin <<< "${ARTIFACTORY_PASSWORD}"

# Containerfile.ibm is the IBM variant; Containerfile stays for the GitHub workflows.
docker_build \
  --file "${BASE_DIR}/Containerfile.ibm" \
  --tag "${IMAGE_NAME}" \
  --build-arg CENTRAL_BASE_IMAGE="${CENTRAL_BASE_IMAGE}" \
  --build-arg RUNTIME_BASE_IMAGE="${RUNTIME_BASE_IMAGE}" \
  --build-arg VERSION="${VERSION}" \
  --build-arg COMMIT="${COMMIT}" \
  --build-arg SOURCE_STATE="${SOURCE_STATE}"

success "EXIT ${BASH_SOURCE[0]}"
