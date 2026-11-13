#!/usr/bin/env bash
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
exec "$repo_root/tests/f05-network/run.sh" \
    "f07-limits" \
    "$repo_root/ROADMAP/F07-limits/source/live_acceptance.py" \
    "$@"
