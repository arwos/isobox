#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  tests/host-info/run.sh [--smoke]

Collect host/runtime diagnostics. With --smoke, build isobox and run /usr/bin/true
inside a minimal network-off sandbox. Run as your regular user, not with sudo.
EOF
}

smoke=false
while (($#)); do
    case "$1" in
        --smoke) smoke=true ;;
        -h|--help) usage; exit 0 ;;
        *) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
results_root=${ISOBOX_TEST_RESULTS:-"${TMPDIR:-/tmp}/isobox-test-results"}
binary_path=${ISOBOX_TEST_BINARY:-"${TMPDIR:-/tmp}/isobox-test-bin/isobox"}
mkdir -p -- "$results_root"
result_dir=$(mktemp -d "$results_root/host-info.XXXXXX")
report="$result_dir/result.txt"

{
    printf 'scenario=host-info\n'
    printf 'date_utc=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'repository=%s\n' "$repo_root"
    printf 'commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD 2>&1 || true)"
    printf 'user='; id
    printf 'kernel='; uname -a
    if [[ -r /etc/os-release ]]; then
        . /etc/os-release
        printf 'distribution=%s\n' "${PRETTY_NAME:-unknown}"
    else
        printf 'distribution=not-found\n'
    fi
    printf 'architecture=%s\n' "$(getconf LONG_BIT 2>/dev/null || uname -m)"
    printf 'cgroup_version='
    if [[ -r /sys/fs/cgroup/cgroup.controllers ]]; then
        printf 'v2\n'
    elif [[ -r /proc/mounts ]] && grep -q ' cgroup ' /proc/mounts; then
        printf 'v1-or-hybrid\n'
    else
        printf 'unknown\n'
    fi
    printf 'user_namespace_limit='
    if [[ -r /proc/sys/user/max_user_namespaces ]]; then
        cat /proc/sys/user/max_user_namespaces
    else
        printf 'not-readable\n'
    fi

    printf '\n--- executables ---\n'
    for tool in go bwrap pasta slirp4netns nft ip systemd-run systemctl podman sing-box python3; do
        tool_path=$(command -v "$tool" 2>/dev/null || true)
        printf '%s_path=%s\n' "$tool" "${tool_path:-not-found}"
        [[ -n "$tool_path" ]] || continue
        printf '%s_version=' "$tool"
        if command -v timeout >/dev/null 2>&1; then
            timeout 8s "$tool_path" --version 2>&1 | head -n 2 || true
        else
            "$tool_path" --version 2>&1 | head -n 2 || true
        fi
    done

    printf '\n--- host facilities ---\n'
    if [[ -e /dev/net/tun ]]; then
        stat -c 'tun_device=%F mode=%a owner=%U:%G' /dev/net/tun 2>&1 || true
    else
        printf 'tun_device=not-found\n'
    fi
    if [[ -r /sys/fs/cgroup/cgroup.controllers ]]; then
        printf 'cgroup_controllers='
        tr -s ' ' ',' </sys/fs/cgroup/cgroup.controllers
    else
        printf 'cgroup_controllers=not-readable\n'
    fi
    if command -v ip >/dev/null 2>&1; then
        printf 'ipv6_default_route='
        ipv6_default_route=$(ip -6 route show default 2>/dev/null || true)
        if [[ -n "$ipv6_default_route" ]]; then printf 'present\n'; else printf 'absent\n'; fi
    else
        printf 'ipv6_default_route=ip-tool-not-found\n'
    fi

    printf '\n--- user service manager ---\n'
    if command -v systemctl >/dev/null 2>&1; then
        printf 'systemctl_user_is_system_running='
        if command -v timeout >/dev/null 2>&1; then
            timeout 8s systemctl --user is-system-running 2>&1 || true
        else
            systemctl --user is-system-running 2>&1 || true
        fi
    else
        printf 'systemctl_user_is_system_running=systemctl-not-found\n'
    fi
    if command -v podman >/dev/null 2>&1; then
        printf 'podman_rootless='
        if command -v timeout >/dev/null 2>&1; then
            timeout 12s podman info --format '{{.Host.Security.Rootless}}' 2>&1 || true
        else
            podman info --format '{{.Host.Security.Rootless}}' 2>&1 || true
        fi
    else
        printf 'podman_rootless=podman-not-found\n'
    fi

    printf '\n--- runtime smoke test ---\n'
    if [[ "$smoke" != true ]]; then
        printf 'status=not-requested\n'
    elif ! command -v go >/dev/null 2>&1; then
        printf 'status=blocked\nreason=go-not-found\n'
    else
        mkdir -p -- "$(dirname -- "$binary_path")"
        printf 'build_binary=%s\n' "$binary_path"
        if (cd -- "$repo_root" && go build -o "$binary_path" ./cmd/isobox) >"$result_dir/build.log" 2>&1; then
            printf 'build_status=passed\n'
            profile="$script_dir/fixtures/minimal-off.unic"
            printf 'command=%q run --profile=%q --app-id=host-report -- /usr/bin/true\n' "$binary_path" "$profile"
            if "$binary_path" run --profile="$profile" --app-id=host-report -- /usr/bin/true >"$result_dir/smoke.log" 2>&1; then
                printf 'status=passed\n'
            else
                smoke_status=$?
                printf 'status=failed\nexit_status=%s\n' "$smoke_status"
            fi
            printf '%s\n' '--- smoke output ---'
            cat "$result_dir/smoke.log"
        else
            build_status=$?
            printf 'build_status=failed\nexit_status=%s\n' "$build_status"
            printf '%s\n' '--- build output ---'
            cat "$result_dir/build.log"
        fi
    fi

    printf '\nresult_dir=%s\n' "$result_dir"
} | tee "$report"

if [[ "$smoke" == true ]] && ! grep -q '^status=passed$' "$report"; then
    exit 1
fi
