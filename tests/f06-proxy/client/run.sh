#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  tests/f06-proxy/client/run.sh [--race]

Run the SOCKS5 client checks for F06 and save a shareable report under
${ISOBOX_TEST_RESULTS:-${TMPDIR:-/tmp}/isobox-test-results}.
Run as your regular user; root, Podman, and external network access are not needed.
EOF
}

race=false
while (($#)); do
    case "$1" in
        --race) race=true ;;
        -h|--help) usage; exit 0 ;;
        *) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
results_root=${ISOBOX_TEST_RESULTS:-"${TMPDIR:-/tmp}/isobox-test-results"}
mkdir -p -- "$results_root"
result_dir=$(mktemp -d "$results_root/f06-proxy-client.XXXXXX")
metadata="$result_dir/metadata.txt"
test_log="$result_dir/test.log"
result="$result_dir/result.txt"

{
    printf 'scenario=f06-socks5-client\n'
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'repository=%s\n' "$repo_root"
    printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD 2>&1 || true)"
    printf 'worktree_status=\n'
    git -C "$repo_root" status --short 2>&1 || true
    printf 'user=%s\n' "$(id 2>&1)"
    printf 'kernel=%s\n' "$(uname -a 2>&1)"
    printf 'go=%s\n' "$(go version 2>&1 || true)"
    printf 'goos=%s\n' "$(go env GOOS 2>&1 || true)"
    printf 'goarch=%s\n' "$(go env GOARCH 2>&1 || true)"
    printf 'race=%s\n' "$race"
    printf 'package=./internal/proxy\n'
} >"$metadata"

test_args=(test ./internal/proxy -count=1)
if [[ "$race" == true ]]; then
    test_args+=( -race )
fi

if (cd -- "$repo_root" && go "${test_args[@]}") >"$test_log" 2>&1; then
    test_status=0
else
    test_status=$?
fi

{
    cat "$metadata"
    printf '\n--- go test output ---\n'
    cat "$test_log"
    printf '\ngo_test_status=%s\n' "$([[ $test_status -eq 0 ]] && printf passed || printf failed)"
    printf 'exit_status=%s\n' "$test_status"
    printf 'result_dir=%s\n' "$result_dir"
} >"$result"

cat "$result"
exit "$test_status"
