# Live acceptance for resource limits

`live_acceptance.py` runs the real isobox binary as the ordinary user who will
use it. The host needs `bwrap`, `systemd-run`, a running user systemd manager,
and writable delegated cgroup v2 controllers for the requested limits.

Build and run:

```sh
GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath \
  go build -o /tmp/isobox-f07-live/isobox ./cmd/isobox
python3 ROADMAP/F07-limits/source/live_acceptance.py /tmp/isobox-f07-live/isobox
```

The script checks `RLIMIT_NOFILE` on its own and combined with cgroup limits;
reads `cpu.max`, `memory.max`, `pids.max`, and `pids.current` while the app is
held open; confirms the requested application task budget remains available
after accounting for bwrap supervisors; confirms an extra thread is denied;
and checks transient scope cleanup. It also removes `systemd-run` from `PATH`
and verifies a requested CPU limit fails closed before the payload starts.

The script uses Python's standard library and writes all profiles, state, and
markers under a temporary directory. It refuses to run as root.
