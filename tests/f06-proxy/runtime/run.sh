#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  tests/f06-proxy/runtime/run.sh [--socks-port PORT] [--host-port PORT]

Run the complete proxy TUN and published-port check with a local sing-box
SOCKS5 server in rootless Podman. Run as a regular user, not with sudo.
EOF
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
image=${ISOBOX_SINGBOX_IMAGE:-ghcr.io/sagernet/sing-box:v1.14.2}
socks_port=${ISOBOX_SINGBOX_PORT:-10809}
host_port=${ISOBOX_PROXY_INGRESS_PORT:-0}

while (($#)); do
    case "$1" in
        --socks-port)
            (($# >= 2)) || { usage >&2; exit 2; }
            socks_port=$2
            shift 2
            ;;
        --host-port)
            (($# >= 2)) || { usage >&2; exit 2; }
            host_port=$2
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

[[ "$socks_port" =~ ^[0-9]+$ ]] && ((socks_port >= 1024 && socks_port <= 65535)) || {
    printf 'SOCKS port must be an integer between 1024 and 65535\n' >&2
    exit 2
}
[[ "$host_port" =~ ^[0-9]+$ ]] && ((host_port == 0 || (host_port >= 1024 && host_port <= 65535))) || {
    printf 'host port must be 0 or an integer between 1024 and 65535\n' >&2
    exit 2
}

results_root=${ISOBOX_TEST_RESULTS:-"${TMPDIR:-/tmp}/isobox-test-results"}
mkdir -p -- "$results_root"
result_dir=$(mktemp -d "$results_root/f06-proxy-runtime.XXXXXX")
name="isobox-f06-proxy-runtime-$$-${RANDOM}"
metadata="$result_dir/metadata.txt"
events="$result_dir/commands.log"
podman_info="$result_dir/podman-info.log"
container_log="$result_dir/sing-box.log"
build_log="$result_dir/build.log"
proxy_log="$result_dir/proxy-add.log"
app_log="$result_dir/isobox-run.log"
echo_log="$result_dir/echo.log"
config="$result_dir/sing-box.json"
profile="$result_dir/profile.unic"
binary="$result_dir/isobox"
result="$result_dir/result.txt"
container_started=false
echo_pid=
sandbox_pid=
host_address=
tcp_echo_port=
udp_echo_port=

cleanup() {
    exit_code=$?
    trap - EXIT
    if [[ -n "$sandbox_pid" ]] && kill -0 "$sandbox_pid" 2>/dev/null; then
        kill -TERM "$sandbox_pid" 2>/dev/null || true
        wait "$sandbox_pid" 2>/dev/null || true
    fi
    if [[ -n "$echo_pid" ]] && kill -0 "$echo_pid" 2>/dev/null; then
        kill -TERM "$echo_pid" 2>/dev/null || true
        wait "$echo_pid" 2>/dev/null || true
    fi
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
        printf '\n--- build ---\n'
        cat "$build_log" 2>/dev/null || true
        printf '\n--- proxy configuration ---\n'
        cat "$proxy_log" 2>/dev/null || true
        printf '\n--- isobox run ---\n'
        cat "$app_log" 2>/dev/null || true
        printf '\n--- host echo service ---\n'
        cat "$echo_log" 2>/dev/null || true
        printf '\n--- sing-box container ---\n'
        cat "$container_log" 2>/dev/null || true
        printf '\nexit_status=%s\nresult_dir=%s\n' "$exit_code" "$result_dir"
    } >"$result"
    cat "$result"
    printf '\nShare this file: %s\n' "$result"
    exit "$exit_code"
}
trap cleanup EXIT

{
    printf 'scenario=f06-proxy-runtime-ingress\n'
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
    printf 'socks_port=%s\n' "$socks_port"
    printf 'requested_host_port=%s\n' "$host_port"
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

host_address=$(python3 - <<'PY'
import fcntl
import socket
import struct
import sys

default_interfaces = []
try:
    with open("/proc/net/route", encoding="ascii") as routes:
        next(routes)
        for line in routes:
            fields = line.split()
            if len(fields) > 3 and fields[1] == "00000000" and int(fields[3], 16) & 1:
                default_interfaces.append(fields[0])
except OSError:
    pass

names = default_interfaces + [name for _, name in socket.if_nameindex() if name not in default_interfaces]
seen = set()
probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
for name in names:
    if name in seen:
        continue
    seen.add(name)
    try:
        request = struct.pack("256s", name.encode("ascii")[:15])
        response = fcntl.ioctl(probe.fileno(), 0x8915, request)
        address = socket.inet_ntoa(response[20:24])
    except (OSError, UnicodeEncodeError):
        continue
    if address.startswith("127.") or address.startswith("10.89.0.") or address.startswith("10.89.1.") or address == "0.0.0.0":
        continue
    print(address)
    break
else:
    print("no non-loopback host IPv4 address outside the isobox test subnets", file=sys.stderr)
    raise SystemExit(1)
PY
) || {
    printf 'cannot choose a local IPv4 address for the SOCKS echo target\n' >>"$events"
    exit 1
}

host_dns=$(python3 - <<'PY'
import ipaddress
import sys

try:
    with open("/etc/resolv.conf", encoding="ascii") as resolvers:
        for line in resolvers:
            fields = line.split()
            if len(fields) == 2 and fields[0] == "nameserver":
                try:
                    address = ipaddress.ip_address(fields[1])
                except ValueError:
                    continue
                if address.version == 4:
                    print(address)
                    raise SystemExit(0)
except OSError:
    pass
print("unavailable", file=sys.stderr)
raise SystemExit(1)
PY
) || host_dns=unavailable

if [[ "$host_port" == 0 ]]; then
    host_port=$(python3 - <<'PY'
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)
fi
tcp_echo_port=$(python3 - "$host_address" <<'PY'
import socket
import sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.bind((sys.argv[1], 0))
print(s.getsockname()[1])
s.close()
PY
)
udp_echo_port=$(python3 - "$host_address" <<'PY'
import socket
import sys
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.bind((sys.argv[1], 0))
print(s.getsockname()[1])
s.close()
PY
)
printf 'host_echo_address=%s\ntcp_echo_port=%s\nudp_echo_port=%s\nhost_port=%s\n' \
    "$host_address" "$tcp_echo_port" "$udp_echo_port" "$host_port" >>"$metadata"
printf 'host_first_ipv4_dns=%s\n' "$host_dns" >>"$metadata"

python3 "$script_dir/echo.py" "$host_address" "$tcp_echo_port" "$udp_echo_port" >"$echo_log" 2>&1 &
echo_pid=$!
for _ in $(seq 1 50); do
    if grep -q '^ECHO_READY$' "$echo_log"; then
        break
    fi
    if ! kill -0 "$echo_pid" 2>/dev/null; then
        printf 'host echo server exited before readiness\n' >>"$events"
        exit 1
    fi
    sleep 0.1
done
if ! grep -q '^ECHO_READY$' "$echo_log"; then
    printf 'host echo server did not become ready\n' >>"$events"
    exit 1
fi

cat >"$config" <<EOF
{
  "log": {"level": "info", "timestamp": true},
  "inbounds": [{
    "type": "socks",
    "tag": "isobox-runtime-test",
    "listen": "127.0.0.1",
    "listen_port": $socks_port,
    "users": []
  }],
  "outbounds": [{"type": "direct", "tag": "direct"}],
  "route": {"final": "direct"}
}
EOF
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

for _ in $(seq 1 50); do
    if python3 - "$socks_port" <<'PY' >/dev/null 2>&1
import socket
import sys
with socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=0.2):
    pass
PY
    then
        break
    fi
    sleep 0.1
done
if ! python3 - "$socks_port" <<'PY' >>"$events" 2>&1
import socket
import sys
with socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=1):
    print("SOCKS listener is accepting connections")
PY
then
    printf 'sing-box SOCKS listener did not become ready\n' >>"$events"
    exit 1
fi

mkdir -m 700 -- "$result_dir/config" "$result_dir/home"
export XDG_CONFIG_HOME="$result_dir/config"
if ! (cd -- "$repo_root" && go build -o "$binary" ./cmd/isobox) >"$build_log" 2>&1; then
    printf 'isobox build failed; see build.log in the result directory\n' >>"$events"
    exit 1
fi
if ! "$binary" proxy add --alias=f06-runtime --server="127.0.0.1:$socks_port" --auth=none >"$proxy_log" 2>&1; then
    printf 'cannot configure the temporary no-auth SOCKS5 alias\n' >>"$events"
    exit 1
fi

cat >"$profile" <<EOF
version 1;
home_path '$result_dir/home';
mount {
  source '$script_dir/probe_app.py';
  target '/app/probe_app.py';
  read_only true;
}
network {
  mode proxy;
  proxy_alias 'f06-runtime';
  port 127.0.0.1 $host_port tcp;
}
EOF

"$binary" run --profile="$profile" -- \
    /usr/bin/python3 -u /app/probe_app.py "$host_address" "$tcp_echo_port" "$udp_echo_port" "$host_port" \
    >"$app_log" 2>&1 &
sandbox_pid=$!

app_ready=false
for _ in $(seq 1 120); do
    if grep -q '^APP_READY$' "$app_log"; then
        app_ready=true
        break
    fi
    if ! kill -0 "$sandbox_pid" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
if [[ "$app_ready" != true ]]; then
    printf 'proxy application did not reach its TCP listen state\n' >>"$events"
    exit 1
fi
if ! grep -q '^OUTBOUND_TCP_OK$' "$app_log" || ! grep -q '^OUTBOUND_UDP_OK$' "$app_log"; then
    printf 'proxy TCP/UDP echo checks did not both pass before the app listen state\n' >>"$events"
    exit 1
fi

if ! python3 - "$host_port" >>"$events" 2>&1 <<'PY'
import socket
import sys

with socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=8) as connection:
    connection.settimeout(8)
    connection.sendall(b"isobox-proxy-ingress")
    response = connection.recv(64)
if response != b"isobox-proxy-ingress-ok":
    raise SystemExit(f"unexpected host-forward response: {response!r}")
print("PUBLISHED_TCP_INGRESS_OK")
PY
then
    printf 'host-to-sandbox published TCP request failed\n' >>"$events"
    exit 1
fi

if wait "$sandbox_pid"; then
    app_status=0
else
    app_status=$?
fi
sandbox_pid=
printf 'isobox_run_exit_status=%s\n' "$app_status" >>"$events"
if ((app_status != 0)); then
    exit "$app_status"
fi
for marker in DNS_QUERY_OK OUTBOUND_TCP_OK OUTBOUND_UDP_OK APP_READY INGRESS_RESPONSE_OK; do
    if ! grep -q "^$marker$" "$app_log"; then
        printf 'application log lacks checkpoint %s\n' "$marker" >>"$events"
        exit 1
    fi
done

printf 'runtime_acceptance=passed\n' >>"$events"
