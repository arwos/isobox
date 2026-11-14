#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  tests/f04-sandbox/gui-grants/run.sh

Run as a regular, non-root user. Builds isobox and checks default-deny plus
independent Wayland, X11, PipeWire, PulseAudio, GPU, and session-bus grants.
EOF
}

if (($#)); then
    case "$1" in
        -h|--help) usage; exit 0 ;;
        *) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
results_root=${ISOBOX_TEST_RESULTS:-"${TMPDIR:-/tmp}/isobox-test-results"}
binary_path=${ISOBOX_TEST_BINARY:-"${TMPDIR:-/tmp}/isobox-test-bin/isobox"}
mkdir -p -- "$results_root" "$(dirname -- "$binary_path")"
result_dir=$(mktemp -d "$results_root/f04-gui-grants.XXXXXX")
runtime_dir=$(mktemp -d /tmp/isobox-f04-gui-grants.XXXXXX)
report="$result_dir/result.txt"
events="$result_dir/events.log"
build_log="$result_dir/build.log"
server_log="$result_dir/fake-sockets.log"
active_log=
server_pid=
x11_display=

cleanup() {
    if [[ -n "$server_pid" ]]; then
        kill -TERM "$server_pid" 2>/dev/null || true
        wait "$server_pid" 2>/dev/null || true
        server_pid=
    fi
    if [[ -f "$server_log" ]]; then
        printf '\n--- synthetic socket server ---\n' >>"$report"
        cat -- "$server_log" >>"$report"
    fi
    rm -rf -- "$runtime_dir"
}
trap cleanup EXIT

fail() {
    printf 'FAIL %s\n' "$*" | tee -a "$events" "$report" >&2
    if [[ -n "$active_log" && -f "$active_log" ]]; then
        cat -- "$active_log" | tee -a "$report" >&2
    fi
    exit 1
}

if ((EUID == 0)); then
    printf 'Run this acceptance check as a regular user, not root.\n' >&2
    exit 2
fi
for tool in go bwrap python3 timeout readlink; do
    command -v "$tool" >/dev/null 2>&1 || { printf 'required tool not found: %s\n' "$tool" >&2; exit 2; }
done
host_bwrap=$(command -v bwrap)
python_binary=$(readlink -f "$(command -v python3)")
case "$python_binary" in
    /usr/*|/bin/*|/sbin/*) ;;
    *) printf 'Python 3 must resolve inside a mounted system path (/usr, /bin, or /sbin): %s\n' "$python_binary" >&2; exit 2 ;;
esac

{
    printf 'scenario=f04-gui-grants\n'
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'repository=%s\n' "$repo_root"
    printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD 2>&1 || true)"
    printf 'user='; id
    printf 'kernel='; uname -a
    printf 'bwrap='; bwrap --version 2>&1 | head -n 1
} | tee "$report"

if ! (cd -- "$repo_root" && GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build -o "$binary_path" ./cmd/isobox) >"$build_log" 2>&1; then
    printf 'isobox build failed; see build.log in the result directory\n' | tee -a "$events" "$report"
    cat -- "$build_log" | tee -a "$report"
    exit 1
fi
printf 'build_status=passed\n' | tee -a "$report"
cat -- "$build_log" | tee -a "$report"

home_path="$runtime_dir/app-home"
mkdir -m 700 -- "$home_path"
wayland_runtime="$runtime_dir/wayland-runtime"
pipewire_runtime="$runtime_dir/pipewire-runtime"
pulse_runtime="$runtime_dir/pulse-runtime"
desktop_runtime="$runtime_dir/desktop-runtime"
mkdir -m 700 -- "$wayland_runtime" "$pipewire_runtime" "$pulse_runtime" "$desktop_runtime"
mkdir -m 700 -- "$pulse_runtime/pulse"
wayland_socket="$wayland_runtime/wayland-test"
pipewire_socket="$pipewire_runtime/pipewire-0"
pulse_socket="$pulse_runtime/pulse/native"
desktop_socket="$desktop_runtime/bus"
pulse_cookie="$runtime_dir/pulse.cookie"
xauthority="$runtime_dir/Xauthority"
printf 'synthetic PulseAudio cookie\n' >"$pulse_cookie"
printf 'synthetic Xauthority data\n' >"$xauthority"
chmod 600 "$pulse_cookie" "$xauthority"
ready_file="$runtime_dir/socket-server-ready"
fake_x11_dir=/tmp/.X11-unix

python3 "$script_dir/fake_socket_server.py" --ready-file "$ready_file" --x11-dir "$fake_x11_dir" \
    --socket "$wayland_socket" --socket "$pipewire_socket" --socket "$pulse_socket" --socket "$desktop_socket" \
    >"$server_log" 2>&1 &
server_pid=$!
for ((attempt=0; attempt<150; attempt++)); do
    if [[ -s "$ready_file" ]]; then
        break
    fi
    if ! kill -0 "$server_pid" 2>/dev/null; then
        active_log=$server_log
        fail "synthetic Unix socket server failed to start"
    fi
    sleep 0.1
done
if [[ ! -s "$ready_file" ]]; then
    active_log=$server_log
    fail "synthetic Unix socket server did not become ready"
fi
x11_display=$(cat -- "$ready_file")
x11_socket="$fake_x11_dir/X${x11_display}"

profile_path="$runtime_dir/gui-profile.unic"
write_profile() {
    local gui_fields=$1
    {
        printf "version 1;\nhome_path '%s';\n" "$home_path"
        if [[ -n "$gui_fields" ]]; then
            printf 'gui {\n%s\n}\n' "$gui_fields"
        fi
        printf 'network {\n  mode off;\n}\n'
    } >"$profile_path"
}

run_probe() {
    local name=$1
    local mode=$2
    local gui_fields=$3
    shift 3
    # probe.py replaces punctuation in marker names with underscores.
    local marker="${name}_started"
    local log="$result_dir/$name.log"
    local -a environment=("ISOBOX_GUI_TEST_SECRET=must-not-leak" "$@")
    write_profile "$gui_fields"
    active_log=$log
    if ! cat -- "$script_dir/probe.py" | env "ISOBOX_GUI_TEST_MODE=$mode" \
        "${environment[@]}" timeout 40s "$binary_path" run "--profile=$profile_path" -- "$python_binary" - "$mode" "$marker" >"$log" 2>&1; then
        fail "$name sandbox probe failed"
    fi
    if ! grep -qx "PASS GUI grant $mode" "$log" || [[ ! -f "$home_path/$marker" ]]; then
        fail "$name probe missed its expected checkpoint"
    fi
    printf 'PASS %s\n' "$name" | tee -a "$events" "$report"
    cat -- "$log" | tee -a "$report"
    active_log=
}

run_failure() {
    local name=$1
    local gui_fields=$2
    local expected_error=$3
    shift 3
    local marker="$name.started"
    local log="$result_dir/$name.log"
    local launch_code="from pathlib import Path; Path('/home/isobox/$marker').write_text('unexpected')"
    local -a environment=("ISOBOX_GUI_TEST_SECRET=must-not-leak" "$@")
    write_profile "$gui_fields"
    active_log=$log
    if env "${environment[@]}" timeout 40s "$binary_path" run "--profile=$profile_path" -- "$python_binary" -c "$launch_code" >"$log" 2>&1; then
        fail "$name unexpectedly launched the app"
    fi
    if ! grep -Fq "$expected_error" "$log" || [[ -e "$home_path/$marker" ]]; then
        fail "$name did not fail closed before the app marker"
    fi
    printf 'PASS %s failed closed before app launch\n' "$name" | tee -a "$events" "$report"
    cat -- "$log" | tee -a "$report"
    active_log=
}

all_host_surfaces=(
    "XDG_RUNTIME_DIR=$wayland_runtime"
    WAYLAND_DISPLAY=wayland-test
    "DISPLAY=:$x11_display"
    "XAUTHORITY=$xauthority"
    "PIPEWIRE_RUNTIME_DIR=$pipewire_runtime"
    PIPEWIRE_REMOTE=pipewire-0
    "PULSE_SERVER=unix:path=$pulse_socket"
    "PULSE_COOKIE=$pulse_cookie"
    "DBUS_SESSION_BUS_ADDRESS=unix:path=$desktop_socket"
    DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket
    XDG_CURRENT_DESKTOP=isobox-test
)
run_probe default-deny default '' "${all_host_surfaces[@]}"
run_probe wayland-only wayland '  wayland true;' "${all_host_surfaces[@]}"
run_probe x11-only "x11:$x11_display" '  x11 true;' "${all_host_surfaces[@]}"

run_probe pipewire-only pipewire '  audio true;' \
    "XDG_RUNTIME_DIR=$pipewire_runtime" "PIPEWIRE_RUNTIME_DIR=$pipewire_runtime" PIPEWIRE_REMOTE=pipewire-0 \
    "PULSE_SERVER=unix:path=$runtime_dir/no-pulse-socket" "PULSE_COOKIE=$pulse_cookie"
run_probe pulse-only pulse '  audio true;' \
    "XDG_RUNTIME_DIR=$pulse_runtime" "PIPEWIRE_RUNTIME_DIR=$pulse_runtime" PIPEWIRE_REMOTE=missing-pipewire \
    "PULSE_SERVER=unix:path=$pulse_socket" "PULSE_COOKIE=$pulse_cookie"
run_probe desktop-only desktop '  desktop true;' \
    "XDG_RUNTIME_DIR=$desktop_runtime" "DBUS_SESSION_BUS_ADDRESS=unix:path=$desktop_socket" XDG_CURRENT_DESKTOP=isobox-test

run_failure wayland-invalid '  wayland true;' 'WAYLAND_DISPLAY must name a socket inside XDG_RUNTIME_DIR' \
    "XDG_RUNTIME_DIR=$wayland_runtime" WAYLAND_DISPLAY=../host-file
run_failure x11-rejects-tcp '  x11 true;' 'only a local Unix DISPLAY is supported' DISPLAY=example.invalid:8
run_failure audio-rejects-remote '  audio true;' 'only a local Unix PULSE_SERVER is supported' \
    "PIPEWIRE_RUNTIME_DIR=$pipewire_runtime" PIPEWIRE_REMOTE=missing-pipewire PULSE_SERVER=tcp:127.0.0.1:4713
run_failure desktop-missing-bus '  desktop true;' 'no usable filesystem session-bus socket' \
    "XDG_RUNTIME_DIR=$desktop_runtime" "DBUS_SESSION_BUS_ADDRESS=unix:path=$runtime_dir/no-session-bus"

kill -TERM "$server_pid" 2>/dev/null || true
if wait "$server_pid"; then
    server_exit=0
else
    server_exit=$?
fi
server_pid=
if ((server_exit != 0)) || [[ -e "$x11_socket" || -e "$wayland_socket" || -e "$pipewire_socket" || -e "$pulse_socket" || -e "$desktop_socket" ]]; then
    active_log=$server_log
    fail "synthetic GUI sockets did not shut down and clean up"
fi
printf 'PASS synthetic GUI socket cleanup\n' | tee -a "$events" "$report"

gpu_profile_fields='  gpu true;'
write_profile "$gpu_profile_fields"
gpu_log="$result_dir/gpu-only.log"
gpu_marker=gpu-only_started
active_log=$gpu_log
if cat -- "$script_dir/probe.py" | env "ISOBOX_GUI_TEST_SECRET=must-not-leak" ISOBOX_GUI_TEST_MODE=gpu \
    "${all_host_surfaces[@]}" timeout 40s "$binary_path" run "--profile=$profile_path" -- \
    "$python_binary" - gpu "$gpu_marker" >"$gpu_log" 2>&1; then
    if ! grep -qx 'PASS GUI grant gpu' "$gpu_log" || [[ ! -f "$home_path/$gpu_marker" ]]; then
        fail "GPU grant did not expose its device independently"
    fi
    printf 'PASS GPU grant exposed host devices without unrelated GUI resources\n' | tee -a "$events" "$report"
    cat -- "$gpu_log" | tee -a "$report"
else
    gpu_exit=$?
    if ! grep -Fq 'profile.gui.gpu: no supported GPU devices found' "$gpu_log" || [[ -e "$home_path/$gpu_marker" ]]; then
        fail "GPU grant failed for a reason other than missing host GPU devices (exit $gpu_exit)"
    fi
    printf 'PASS GPU grant failed closed because no supported host GPU device is available\n' | tee -a "$events" "$report"
    cat -- "$gpu_log" | tee -a "$report"
fi
active_log=

printf 'runtime_acceptance=passed\nresult_dir=%s\n' "$result_dir" | tee -a "$report"
printf 'result_dir=%s\n' "$result_dir"
