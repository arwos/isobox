#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  tests/f06-proxy/sing-box/run.sh [--port PORT]

Start the test-only sing-box SOCKS5 server in rootless Podman, exercise TCP,
UDP and bad credentials with the isobox SOCKS5 client, stop the container, and
verify that its listener is gone. The server binds to host loopback only.

Set ISOBOX_TEST_RESULTS to choose the report directory and
ISOBOX_SINGBOX_IMAGE to override the pinned image reference.
EOF
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
port=${ISOBOX_SINGBOX_PORT:-10808}
image=${ISOBOX_SINGBOX_IMAGE:-ghcr.io/sagernet/sing-box:v1.14.2}

while (($#)); do
    case "$1" in
        --port)
            (($# >= 2)) || { usage >&2; exit 2; }
            port=$2
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            printf 'unknown option: %s\n' "$1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

[[ "$port" =~ ^[0-9]+$ ]] && ((port >= 1024 && port <= 65535)) || {
    printf 'port must be an integer between 1024 and 65535\n' >&2
    exit 2
}

results_root=${ISOBOX_TEST_RESULTS:-"${TMPDIR:-/tmp}/isobox-test-results"}
mkdir -p -- "$results_root"
result_dir=$(mktemp -d "$results_root/f06-sing-box.XXXXXX")
name="isobox-f06-sing-box-$$-${RANDOM}"
metadata="$result_dir/metadata.txt"
events="$result_dir/commands.log"
podman_info="$result_dir/podman-info.log"
container_log="$result_dir/sing-box.log"
test_log="$result_dir/test.log"
config="$result_dir/config.json"
result="$result_dir/result.txt"
container_started=false

cleanup() {
    exit_code=$?
    trap - EXIT
    if [[ "$container_started" == true ]]; then
        podman logs "$name" >>"$container_log" 2>&1 || true
        podman stop --time 2 "$name" >>"$events" 2>&1 || true
        podman rm -f "$name" >>"$events" 2>&1 || true
    fi
    {
        cat "$metadata"
        printf '\n--- podman info ---\n'
        cat "$podman_info" 2>/dev/null || true
        printf '\n--- commands ---\n'
        cat "$events" 2>/dev/null || true
        printf '\n--- Go integration test ---\n'
        cat "$test_log" 2>/dev/null || true
        printf '\n--- sing-box container log ---\n'
        cat "$container_log" 2>/dev/null || true
        printf '\nexit_status=%s\nresult_dir=%s\n' "$exit_code" "$result_dir"
    } >"$result"
    cat "$result"
    printf '\nShare this file: %s\n' "$result"
    exit "$exit_code"
}
trap cleanup EXIT

{
    printf 'scenario=f06-sing-box-podman\n'
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'repository=%s\n' "$repo_root"
    printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD 2>&1 || true)"
    printf 'worktree_status=\n'
    git -C "$repo_root" status --short 2>&1 || true
    printf 'user=%s\n' "$(id 2>&1)"
    printf 'kernel=%s\n' "$(uname -a 2>&1)"
    printf 'go=%s\n' "$(go version 2>&1 || true)"
    printf 'podman=%s\n' "$(podman --version 2>&1 || true)"
    printf 'image=%s\n' "$image"
    printf 'listen_address=127.0.0.1:%s\n' "$port"
    printf 'container=%s\n' "$name"
} >"$metadata"

for tool in podman go python3; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        printf 'missing required command: %s\n' "$tool" >>"$events"
        exit 127
    fi
done

if ! podman info >"$podman_info" 2>&1; then
    printf 'podman info failed; see the captured diagnostic above\n' >>"$events"
    exit 1
fi

if ! python3 - "$port" >>"$events" 2>&1 <<'PY'
import socket
import sys

port = int(sys.argv[1])
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        listener.bind(("127.0.0.1", port))
    except OSError as error:
        print(f"127.0.0.1:{port} is already in use: {error}")
        raise SystemExit(1)
print(f"127.0.0.1:{port} is available")
PY
then
    exit 1
fi

sed "s/\"listen_port\": 10808/\"listen_port\": $port/" "$script_dir/config.json" >"$config"
if ! python3 -m json.tool "$config" >/dev/null 2>>"$events"; then
    printf 'generated sing-box config is invalid JSON\n' >>"$events"
    exit 1
fi

if ! podman run --detach --rm \
    --name "$name" \
    --network host \
    --userns keep-id \
    --read-only \
    --tmpfs /tmp:rw,nosuid,nodev,noexec,size=8m \
    --cap-drop all \
    --security-opt no-new-privileges \
    --volume "$config:/etc/sing-box/config.json:ro,Z" \
    "$image" run -c /etc/sing-box/config.json >>"$events" 2>&1; then
    printf 'Podman could not start the sing-box container\n' >>"$events"
    exit 1
fi
container_started=true

if ! (cd -- "$repo_root" && ISOBOX_SINGBOX_FIXTURE="127.0.0.1:$port" go test ./internal/proxy -run '^TestSOCKS5PodmanFixture$' -count=1 -v) >"$test_log" 2>&1; then
    exit 1
fi

podman logs "$name" >>"$container_log" 2>&1 || true
if ! podman stop --time 5 "$name" >>"$events" 2>&1; then
    printf 'Podman failed to stop the sing-box container\n' >>"$events"
    exit 1
fi
container_started=false

if ! (cd -- "$repo_root" && ISOBOX_SINGBOX_FIXTURE="127.0.0.1:$port" ISOBOX_SINGBOX_EXPECT_DOWN=1 go test ./internal/proxy -run '^TestSOCKS5PodmanFixtureStopped$' -count=1 -v) >>"$test_log" 2>&1; then
    exit 1
fi

printf 'fixture_acceptance=passed\n' >>"$events"
