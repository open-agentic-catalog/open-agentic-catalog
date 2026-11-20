#!/usr/bin/env bash

set -euo pipefail

enter "${BASH_SOURCE[0]}"

BASE_DIR=$(dirname "$(dirname "$(realpath "${BASH_SOURCE[0]}")")")
cd "${BASE_DIR}"

# Same environment the GitHub quality/extended workflows use.
export CATALOG_ENV_FILE=/dev/null
export DEV_PYTHON=python3.12
export PYTHONDONTWRITEBYTECODE=1
export GOTELEMETRY=off
export ASTRO_TELEMETRY_DISABLED=1
export NPM_CONFIG_AUDIT=false

EVIDENCE_DIR="${BASE_DIR}/output/ibm-pipeline"
rm -rf "${EVIDENCE_DIR}"
mkdir -p "${EVIDENCE_DIR}"

# ---------------------------------------------------------------------------
# System prerequisites. The pinned Go, Python/uv, Node, ShellCheck, hadolint,
# actionlint, markdownlint and Trivy are installed below by test/ci/tools.py.
# ---------------------------------------------------------------------------
install_system_package() {
  if command -v dnf >/dev/null 2>&1; then
    dnf -y install "$@"
  elif command -v microdnf >/dev/null 2>&1; then
    microdnf -y install "$@"
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get update && DEBIAN_FRONTEND=noninteractive apt-get -y install "$@"
  else
    die "No supported package manager to install: $*"
  fi
}

info "Checking system prerequisites."
for tool in git openssl tar curl make; do
  command -v "${tool}" >/dev/null 2>&1 || install_system_package "${tool}"
done
command -v cc >/dev/null 2>&1 || install_system_package gcc
command -v python3.12 >/dev/null 2>&1 || install_system_package python3.12
command -v docker >/dev/null 2>&1 || die "docker is required (set dind: true on the test stage)."
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is required by 'make check' (compose-check)."

# The Makefile needs GNU Make 4 or later.
make_major="$(make --version | head -n 1 | sed -E 's/^[^0-9]*([0-9]+).*/\1/')"
if [[ "${make_major}" -lt 4 ]]; then
  die "GNU Make 4 or later is required, found: $(make --version | head -n 1)"
fi

# ---------------------------------------------------------------------------
# Pinned tools. tools.py publishes new PATH entries to $GITHUB_PATH; collect
# them and prepend them here, because a child process cannot change our PATH.
# These are the installers behind 'tools.py install --profile quality', plus
# Trivy for 'make security'. install() is not called as a whole because it also
# installs the Docker Compose plugin, which tools.py restricts to GitHub Actions.
# ---------------------------------------------------------------------------
GITHUB_PATH="$(mktemp)"
export GITHUB_PATH
info "Installing pinned tools into .tools/ci."
python3.12 -B - <<'PY'
import sys
sys.path.insert(0, "test/ci")
import tools

tools.preflight("quality")
tools.OWNED.mkdir(parents=True, exist_ok=True)
(tools.OWNED / "bin").mkdir(exist_ok=True)
tools.publish_path(tools.OWNED / "bin")
tools.go_install()
tools.python_install("quality")
tools.node_install(tools.NODE_PROJECT["quality"])
tools.binary_install("shellcheck")
tools.markdown_install()
tools.binary_install("hadolint")
tools.actionlint_install()
tools.binary_install("trivy")
PY
while IFS= read -r tool_path; do
  PATH="${tool_path}:${PATH}"
done < "${GITHUB_PATH}"
export PATH
rm -f "${GITHUB_PATH}"
unset GITHUB_PATH

# tools.py installed exactly the Go version in go.mod; never fetch another one.
export GOTOOLCHAIN=local
go version
make --version | head -n 1

info "Installing locked Python and API validation tools."
make dev-tools-install
make validation-tools-install
trivy image --download-db-only

# ---------------------------------------------------------------------------
# Run every gate even when an earlier one fails, so the evidence is complete,
# then fail the pipeline if any gate failed.
# ---------------------------------------------------------------------------
declare -a RESULTS=()
FAILED=0

run_gate() {
  local name="$1"
  shift
  local log="${EVIDENCE_DIR}/${name}.log"
  info "------------------ ${name}: $* ------------------"
  local status=0
  "$@" 2>&1 | tee "${log}" || status="${PIPESTATUS[0]}"
  if [[ "${status}" -eq 0 ]]; then
    RESULTS+=("| ${name} | passed |")
    success "${name} passed"
  else
    RESULTS+=("| ${name} | FAILED (exit ${status}) |")
    FAILED=1
    warn "${name} FAILED (exit ${status})"
  fi
}

info "------------------ Test Script Started -------------------"
run_gate lint make lint
run_gate check make check
run_gate test-race make test-race
run_gate coverage-check make coverage-check
run_gate coverage-core-check make coverage-core-check
run_gate security make security
info "------------------ Test Script Finished -------------------"

{
  printf '# Open Agentic Catalog IBM pipeline test results\n\n'
  printf 'Commit: %s\n\n' "$(git rev-parse HEAD 2>/dev/null || echo unknown)"
  printf '| Gate | Result |\n| --- | --- |\n'
  printf '%s\n' "${RESULTS[@]}"
} > "${EVIDENCE_DIR}/summary.md"
cat "${EVIDENCE_DIR}/summary.md"

# One archive holds the logs, the summary and the coverage profiles.
ARCHIVE="${EVIDENCE_DIR}/evidence.tgz"
EVIDENCE_FILES=(output/ibm-pipeline/summary.md output/ibm-pipeline/*.log)
for profile in coverage/default.out coverage/core.out; do
  [[ -f "${profile}" ]] && EVIDENCE_FILES+=("${profile}")
done
tar -czf "${ARCHIVE}" "${EVIDENCE_FILES[@]}"

# Fail before collecting evidence: collect_evidence has no verified failure
# status here, so a failed run must not be recorded as passing evidence.
if [[ "${FAILED}" -ne 0 ]]; then
  die "One or more gates failed; see ${EVIDENCE_DIR}/summary.md"
fi

# Documentation: https://cloud.ibm.com/docs/devsecops?topic=devsecops-devsecops-collect-evidence
collect_evidence --tool-type "misc" --attachment "${ARCHIVE}"

success "EXIT ${BASH_SOURCE[0]}"
