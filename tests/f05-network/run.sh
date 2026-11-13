#!/usr/bin/env bash
set -Eeuo pipefail

if (($# < 2)); then
    printf 'usage: %s <scenario-name> <python-runner> [runner args...]\n' "$0" >&2
    exit 2
fi

scenario=$1
runner=$2
shift 2
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
results_root=${ISOBOX_TEST_RESULTS:-"${TMPDIR:-/tmp}/isobox-test-results"}
binary_path=${ISOBOX_TEST_BINARY:-"${TMPDIR:-/tmp}/isobox-test-bin/isobox"}
mkdir -p -- "$results_root" "$(dirname -- "$binary_path")"
result_dir=$(mktemp -d "$results_root/$scenario.XXXXXX")

{
    printf 'scenario=%s\n' "$scenario"
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'repository=%s\n' "$repo_root"
    printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD 2>&1)"
    printf 'user=%s\n' "$(id 2>&1)"
    printf 'kernel=%s\n' "$(uname -a 2>&1)"
    printf 'go=%s\n' "$(go version 2>&1)"
    for tool in bwrap pasta slirp4netns nft python3 systemd-run systemctl; do
        tool_path=$(command -v "$tool" 2>/dev/null || true)
        printf '%s_path=%s\n' "$tool" "${tool_path:-not-found}"
        if [[ -n "$tool_path" ]]; then
            printf '%s_version=' "$tool"
            "$tool_path" --version 2>&1 | head -n 1 || true
        fi
    done
    printf 'binary=%s\n' "$binary_path"
    printf 'runner=%s\n' "$runner"
    printf 'runner_args='
    printf '%q ' "$@"
    printf '\n'
} >"$result_dir/metadata.txt"

if ! (cd -- "$repo_root" && go build -o "$binary_path" ./cmd/isobox) >"$result_dir/build.log" 2>&1; then
    exit_code=1
    {
        cat "$result_dir/metadata.txt"
        printf '\n--- build.log ---\n'
        cat "$result_dir/build.log"
        printf '\nexit_status=%s\n' "$exit_code"
        printf 'result_dir=%s\n' "$result_dir"
    } >"$result_dir/result.txt"
    cat "$result_dir/result.txt"
    exit "$exit_code"
fi

if python3 "$runner" "$binary_path" "$@" >"$result_dir/output.txt" 2>&1; then
    exit_code=0
else
    exit_code=$?
fi

{
    cat "$result_dir/metadata.txt"
    printf '\n--- build.log ---\n'
    cat "$result_dir/build.log"
    printf '\n--- output.txt ---\n'
    cat "$result_dir/output.txt"
    printf '\nexit_status=%s\n' "$exit_code"
    printf 'result_dir=%s\n' "$result_dir"
} >"$result_dir/result.txt"

cat "$result_dir/result.txt"
exit "$exit_code"
