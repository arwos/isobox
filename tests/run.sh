#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
    cat <<'EOF'
Usage:
  ./tests/run.sh --list
  ./tests/run.sh <scenario> [scenario options]

Scenarios:
  host-info                 Collect host diagnostics only
  host-smoke                Build isobox and run /usr/bin/true with networking off
  f04-base-runtime          Check the minimal bwrap root and process lifecycle
  f04-mount-home            Check explicit mounts and persistent HOME
  profile-validation        Validate the checked-in UNIC network profiles
  pasta                     Run the pasta network acceptance scenario
  slirp4netns               Run the slirp4netns network acceptance scenario
  auto-fallback             Check fallback from pasta to slirp4netns
  off-published             Check host-to-sandbox ports with egress blocked
  socks5-client             Run local SOCKS5 client tests
  socks5-client-race        Run SOCKS5 client tests with the race detector
  limits                    Run resource limit acceptance checks

Every scenario prints the path to its saved result.txt. Run as your regular
user, not with sudo. Read tests/README.md for prerequisites and scenario details.
EOF
}

if (($# == 0)); then
    usage >&2
    exit 2
fi

scenario=$1
shift

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)

case "$scenario" in
    -h|--help)
        usage
        exit 0
        ;;
    -l|--list)
        sed -n '/^Scenarios:/,/^$/p' < <(usage)
        exit 0
        ;;
    host-info)
        script=tests/host-info/run.sh
        ;;
    host-smoke)
        script=tests/host-info/run.sh
        set -- --smoke "$@"
        ;;
    f04-base-runtime)
        script=tests/f04-sandbox/base-runtime/run.sh
        ;;
    f04-mount-home)
        script=tests/f04-sandbox/run.sh
        ;;
    profile-validation)
        script=tests/f05-network/profile-validation/run.sh
        ;;
    pasta)
        script=tests/f05-network/pasta/run.sh
        ;;
    slirp4netns)
        script=tests/f05-network/slirp4netns/run.sh
        ;;
    auto-fallback)
        script=tests/f05-network/auto-fallback/run.sh
        ;;
    off-published)
        script=tests/f05-network/off-published/run.sh
        ;;
    socks5-client)
        script=tests/f06-proxy/client/run.sh
        ;;
    socks5-client-race)
        script=tests/f06-proxy/client/run.sh
        set -- --race "$@"
        ;;
    limits)
        script=tests/f07-limits/run.sh
        ;;
    *)
        printf 'unknown test scenario: %s\n\n' "$scenario" >&2
        usage >&2
        exit 2
        ;;
esac

exec bash "$repo_root/$script" "$@"
