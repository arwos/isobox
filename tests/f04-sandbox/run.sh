#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  tests/f04-sandbox/run.sh

Run as a regular, non-root user. Builds isobox and checks explicit read-only
and writable mounts, explicit HOME persistence, app-id/profile HOME isolation,
private managed-HOME permissions, and fail-closed behavior for an unavailable
profile mount.
EOF
}

if (($#)); then
    case "$1" in
        -h|--help) usage; exit 0 ;;
        *) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
results_root=${ISOBOX_TEST_RESULTS:-"${TMPDIR:-/tmp}/isobox-test-results"}
binary_path=${ISOBOX_TEST_BINARY:-"${TMPDIR:-/tmp}/isobox-test-bin/isobox"}
mkdir -p -- "$results_root" "$(dirname -- "$binary_path")"
result_dir=$(mktemp -d "$results_root/f04-mount-home.XXXXXX")
runtime_dir=$(mktemp -d /tmp/isobox-f04-mount-home.XXXXXX)
report="$result_dir/result.txt"
events="$result_dir/events.log"
build_log="$result_dir/build.log"
active_log=
managed_home_paths=()

managed_home_for() {
    python3 - "$1" <<'PY'
import os
import sys
print(os.path.join(os.path.expanduser("~"), ".isobox", "home", sys.argv[1]))
PY
}

remove_managed_homes() {
    if ((${#managed_home_paths[@]} == 0)); then
        return 0
    fi
    local path
    local expected_root="$HOME/.isobox/home/"
    local app_id
    for path in "${managed_home_paths[@]}"; do
        [[ "$path" == "$expected_root"* ]] || return 1
        app_id=${path#"$expected_root"}
        [[ "$app_id" =~ ^f04-[A-Za-z0-9._-]+$ ]] || return 1
        rm -rf -- "$path" || return 1
        [[ ! -e "$path" && ! -L "$path" ]] || return 1
    done
}

cleanup() {
    remove_managed_homes || true
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
for tool in go bwrap python3 timeout; do
    command -v "$tool" >/dev/null 2>&1 || { printf 'required tool not found: %s\n' "$tool" >&2; exit 2; }
done

{
    printf 'scenario=f04-mount-home\n'
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

readonly_source="$runtime_dir/readonly-source"
writable_source="$runtime_dir/writable-source"
explicit_home="$runtime_dir/explicit-home"
launch_source="$runtime_dir/launch-source"
missing_source="$runtime_dir/missing-source"
mkdir -m 700 -- "$readonly_source" "$writable_source" "$explicit_home" "$launch_source"
printf 'read-only mount\n' >"$readonly_source/visible.txt"
cp -- "$script_dir/probe.py" "$readonly_source/probe.py"
printf 'host-only sentinel\n' >"$runtime_dir/host-secret.txt"
host_secret="$runtime_dir/host-secret.txt"

explicit_profile="$runtime_dir/explicit-home.unic"
cat >"$explicit_profile" <<EOF
version 1;
home_path '$explicit_home';
mount {
  source '$readonly_source';
  target '/mnt/readonly';
  read_only true;
}
mount {
  source '$writable_source';
  target '/mnt/writable';
  read_only false;
}
network {
  mode off;
}
EOF

managed_profile="$runtime_dir/managed-home.unic"
cat >"$managed_profile" <<EOF
version 1;
mount {
  source '$readonly_source';
  target '/mnt/readonly';
  read_only true;
}
network {
  mode off;
}
EOF
managed_other_profile="$runtime_dir/managed-other-profile.unic"
cp -- "$managed_profile" "$managed_other_profile"
test_id="f04-${BASHPID}-$(date +%s)"
app_a="$test_id-a"
app_b="$test_id-b"
managed_home_paths=(
    "$(managed_home_for "$app_a")"
    "$(managed_home_for "$app_b")"
)

run_probe() {
    local name=$1
    local profile=$2
    local app_id=$3
    local phase=$4
    local log="$result_dir/$name.log"
    local -a command=("$binary_path" run "--profile=$profile")
    if [[ -n "$app_id" ]]; then
        command+=("--app-id=$app_id")
    fi
    command+=(-- /usr/bin/python3 -u /mnt/readonly/probe.py "$phase" "$host_secret")
    active_log=$log
    if ! timeout 40s "${command[@]}" >"$log" 2>&1; then
        fail "$name did not complete successfully"
    fi
    if ! grep -qx "PASS $phase" "$log"; then
        fail "$name did not report its expected checkpoint"
    fi
    printf 'PASS %s\n' "$name" | tee -a "$events" "$report"
    cat -- "$log" | tee -a "$report"
    active_log=
}

run_probe explicit-home-first "$explicit_profile" "" explicit-first
run_probe explicit-home-repeat "$explicit_profile" "" explicit-repeat
if [[ ! -f "$explicit_home/persisted.txt" || ! -f "$writable_source/persisted.txt" || -e "$readonly_source/write-probe.txt" ]]; then
    fail "explicit HOME or mount writes did not land at the expected host paths"
fi
printf 'PASS explicit HOME path, read-only mount, and writable mount\n' | tee -a "$events" "$report"

run_probe managed-home-first "$managed_profile" "$app_a" managed-first
run_probe managed-home-repeat "$managed_profile" "$app_a" managed-repeat
run_probe different-app-id "$managed_profile" "$app_b" managed-isolation
run_probe same-app-id-other-profile "$managed_other_profile" "$app_a" managed-shared

verify_managed_home() {
    local profile=$1
    local app_id=$2
    local path
    path=$(managed_home_for "$app_id")
    python3 - "$path" <<'PY' || return 1
import os
import stat
import sys
path = sys.argv[1]
for directory in (os.path.dirname(path), path):
    info = os.lstat(directory)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o700:
        raise SystemExit(f"managed HOME directory is not owner-only mode 0700: {directory}")
PY
    printf '%s\n' "$path"
}

home_a=$(verify_managed_home "$managed_profile" "$app_a") || fail "managed HOME A is not private"
home_b=$(verify_managed_home "$managed_profile" "$app_b") || fail "managed HOME B is not private"
home_other_profile=$(verify_managed_home "$managed_other_profile" "$app_a") || fail "managed HOME for the other profile is not private"
if [[ "$home_a" == "$home_b" || "$home_a" != "$home_other_profile" ]]; then
    fail "managed HOME does not follow app-id identity"
fi
printf 'PASS managed HOME persistence, app-id identity, ownership, and mode 0700\n' | tee -a "$events" "$report"

missing_profile="$runtime_dir/missing-mount.unic"
cat >"$missing_profile" <<EOF
version 1;
home_path '$explicit_home';
mount {
  source '$launch_source';
  target '/mnt/launch';
  read_only false;
}
mount {
  source '$missing_source';
  target '/mnt/missing';
  read_only true;
}
network {
  mode off;
}
EOF
missing_log="$result_dir/missing-mount.log"
active_log=$missing_log
if timeout 40s "$binary_path" run "--profile=$missing_profile" -- \
    /usr/bin/python3 -c "from pathlib import Path; Path('/mnt/launch/started').write_text('unexpected')" \
    >"$missing_log" 2>&1; then
    fail "isobox accepted a profile mount whose source does not exist"
fi
if [[ -e "$launch_source/started" ]] || ! grep -Fq 'profile.mount[1].source' "$missing_log"; then
    fail "unsupported mount did not block the application before launch"
fi
printf 'PASS unsupported mount blocked app before launch\n' | tee -a "$events" "$report"
cat -- "$missing_log" | tee -a "$report"
active_log=

if ! remove_managed_homes; then
    fail "temporary managed HOME fixtures were not removed"
fi
printf 'PASS managed HOME fixtures cleaned\n' | tee -a "$events" "$report"

printf 'runtime_acceptance=passed\nresult_dir=%s\n' "$result_dir" | tee -a "$report"
printf 'result_dir=%s\n' "$result_dir"
