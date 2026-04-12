#!/usr/bin/env bash
# Build Go binary snapshots using GoReleaser with optional custom toolchain.
#
# Usage:
#   ./goreleaser_build_snapshot.sh              # Use default Go toolchain
#   ./goreleaser_build_snapshot.sh go1.24       # Specify toolchain as parameter
#   GOTOOLCHAIN_VERSION=go1.24 ./script.sh      # Specify via environment variable
#
# Requirements:
#   - goreleaser installed and available in PATH
#
# The GOTOOLCHAIN environment variable is temporarily set during execution
# and restored afterward, ensuring no side effects.

set -euo pipefail

# Resolve toolchain from parameter first, then environment variable, then default
toolchain="${1:-${GOTOOLCHAIN_VERSION:-}}"

# Resolve script and repo paths
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"

# Verify goreleaser is available
if ! command -v goreleaser >/dev/null 2>&1; then
  echo "ERROR: goreleaser not found in PATH" >&2
  exit 1
fi

# Preserve original GOTOOLCHAIN for restoration
previous_toolchain="${GOTOOLCHAIN:-}"

cleanup() {
  # Restore or unset GOTOOLCHAIN
  if [[ -z "${previous_toolchain}" ]]; then
    unset GOTOOLCHAIN || true
  else
    export GOTOOLCHAIN="${previous_toolchain}"
  fi
}

trap cleanup EXIT

cd "${repo_root}"

if [[ -n "${toolchain}" ]]; then
  echo "Using GOTOOLCHAIN=${toolchain}" | sed 's/^/[INFO] /'
  export GOTOOLCHAIN="${toolchain}"
  goreleaser build --snapshot --clean
else
  echo "Using default Go toolchain" | sed 's/^/[INFO] /'
  goreleaser build --snapshot --clean
fi
