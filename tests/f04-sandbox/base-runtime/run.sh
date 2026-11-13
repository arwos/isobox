#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  tests/f04-sandbox/base-runtime/run.sh

Run as a regular, non-root user. Builds isobox and checks the minimal bwrap
root, runtime directories, sanitized environment, readonly system paths,
argv/exit-status preservation, signal forwarding, and fail-closed mount errors.
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
result_dir=$(mktemp -d "$results_root/f04-base-runtime.XXXXXX")
runtime_dir=$(mktemp -d /tmp/isobox-f04-base-runtime.XXXXXX)
report="$result_dir/result.txt"
events="$result_dir/events.log"
build_log="$result_dir/build.log"
active_log=
signal_pid=

cleanup() {
    if [[ -n "$signal_pid" ]] && kill -0 "$signal_pid" 2>/dev/null; then
        kill -KILL "$signal_pid" 2>/dev/null || true
        wait "$signal_pid" 2>/dev/null || true
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
    printf 'scenario=f04-base-runtime\n'
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
host_secret="$runtime_dir/host-only.txt"
printf 'must remain outside sandbox\n' >"$host_secret"
profile_path="$runtime_dir/base-runtime.unic"
cat >"$profile_path" <<EOF
version 1;
home_path '$home_path';
network {
  mode off;
}
EOF

expected_args=(simple '' 'two words' '--profile=not-an-isobox-flag' "quote'\"; dollar\$literal" 'unicode-Привет' $'line one\nline two')
expected_json=$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "${expected_args[@]}")
probe_log="$result_dir/base-probe.log"
active_log=$probe_log
if ! python3 -c 'import ast,sys; ast.parse(open(sys.argv[1], encoding="utf-8").read())' "$script_dir/probe.py"; then
    fail "host-side Python probe is invalid"
fi
if ! cat -- "$script_dir/probe.py" | env ISOBOX_T01_HOST_SECRET=must-not-leak "$binary_path" run "--profile=$profile_path" -- \
    "$python_binary" - "$host_secret" "$expected_json" "${expected_args[@]}" >"$probe_log" 2>&1; then
    fail "base sandbox probe did not complete successfully"
fi
if ! grep -qx 'PASS bwrap root, runtime paths, clean environment, readonly system paths, and argv' "$probe_log"; then
    fail "base sandbox probe missed its expected checkpoint"
fi
printf 'PASS minimal root, runtime paths, environment, system mounts, and argv\n' | tee -a "$events" "$report"
cat -- "$probe_log" | tee -a "$report"
active_log=

exit_log="$result_dir/exit-status.log"
active_log=$exit_log
if timeout 40s "$binary_path" run "--profile=$profile_path" -- "$python_binary" -c 'raise SystemExit(37)' >"$exit_log" 2>&1; then
    app_exit=0
else
    app_exit=$?
fi
if ((app_exit != 37)); then
    fail "application exit status changed (expected 37, got $app_exit)"
fi
printf 'PASS exact application exit status: %s\n' "$app_exit" | tee -a "$events" "$report"
cat -- "$exit_log" | tee -a "$report"
active_log=

signal_log="$result_dir/signal.log"
signal_program=$'import signal,time\ndef handle(signum, frame):\n print("APP_TERM_RECEIVED", flush=True)\n raise SystemExit(0)\nsignal.signal(signal.SIGTERM, handle)\nprint("APP_READY", flush=True)\nwhile True: time.sleep(0.1)'
env ISOBOX_T01_HOST_SECRET=must-not-leak "$binary_path" run "--profile=$profile_path" -- "$python_binary" -u -c "$signal_program" >"$signal_log" 2>&1 &
signal_pid=$!
ready=false
for ((attempt=0; attempt<150; attempt++)); do
    if grep -qx 'APP_READY' "$signal_log" 2>/dev/null; then
        ready=true
        break
    fi
    if ! kill -0 "$signal_pid" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
active_log=$signal_log
if [[ "$ready" != true ]]; then
    fail "application did not become ready for signal forwarding"
fi
kill -TERM "$signal_pid" || fail "could not signal the isobox supervisor"
signal_handled=false
for ((attempt=0; attempt<50; attempt++)); do
    if grep -qx 'APP_TERM_RECEIVED' "$signal_log" 2>/dev/null; then
        signal_handled=true
        break
    fi
    if ! kill -0 "$signal_pid" 2>/dev/null; then
        break
    fi
    sleep 0.1
done
if [[ "$signal_handled" != true ]]; then
    kill -KILL "$signal_pid" 2>/dev/null || true
    wait "$signal_pid" 2>/dev/null || true
    signal_pid=
    fail "SIGTERM did not reach the sandboxed application before timeout"
fi
if wait "$signal_pid"; then
    signal_exit=0
else
    signal_exit=$?
fi
signal_pid=
if ((signal_exit != 0)) || ! grep -qx 'APP_TERM_RECEIVED' "$signal_log"; then
    fail "SIGTERM did not reach the sandboxed application cleanly (exit $signal_exit)"
fi
printf 'PASS SIGTERM reached the sandboxed app; exit=%s\n' "$signal_exit" | tee -a "$events" "$report"
cat -- "$signal_log" | tee -a "$report"
active_log=

shim_dir="$runtime_dir/bwrap-shim"
mkdir -m 700 -- "$shim_dir"
cat >"$shim_dir/bwrap" <<'SHIM'
#!/usr/bin/env bash
set -Eeuo pipefail
real=${ISOBOX_T01_REAL_BWRAP:?}
args=("$@")
for ((index=0; index + 2 < ${#args[@]}; index++)); do
    if [[ "${args[index]}" == --ro-bind && "${args[index+1]}" == /usr && "${args[index+2]}" == /usr ]]; then
        printf 'simulated required /usr mount failure\n' >&2
        printf 'reached\n' >"${ISOBOX_T01_MOUNT_FAILURE_MARKER:?}"
        exit 1
    fi
done
exec "$real" "$@"
SHIM
chmod 700 "$shim_dir/bwrap"
failure_marker="$runtime_dir/mount-failure-intercepted"
launch_marker="$home_path/mount-failure-started"
mount_failure_log="$result_dir/mount-failure.log"
active_log=$mount_failure_log
if PATH="$shim_dir:$PATH" ISOBOX_T01_REAL_BWRAP="$host_bwrap" ISOBOX_T01_MOUNT_FAILURE_MARKER="$failure_marker" \
    "$binary_path" run "--profile=$profile_path" -- "$python_binary" -c \
    "from pathlib import Path; Path('$launch_marker').write_text('unexpected')" >"$mount_failure_log" 2>&1; then
    mount_failure_exit=0
else
    mount_failure_exit=$?
fi
if ((mount_failure_exit == 0)) || [[ ! -f "$failure_marker" || -e "$launch_marker" ]] || ! grep -Fq 'simulated required /usr mount failure' "$mount_failure_log"; then
    fail "required /usr bind failure did not block the application before payload"
fi
printf 'PASS simulated required /usr bind failure blocked app before payload; exit=%s\n' "$mount_failure_exit" | tee -a "$events" "$report"
cat -- "$mount_failure_log" | tee -a "$report"
active_log=

printf 'runtime_acceptance=passed\nresult_dir=%s\n' "$result_dir" | tee -a "$report"
printf 'result_dir=%s\n' "$result_dir"
