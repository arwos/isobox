#!/usr/bin/env bash
set -euo pipefail
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
exec "$repo_root/tests/f05-network/run.sh" slirp4netns-direct \
    "$repo_root/ROADMAP/F05-network/source/slirp_acceptance.py" \
    --backend slirp "$@"
