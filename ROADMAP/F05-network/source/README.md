# Live acceptance for direct networking

`slirp_acceptance.py` runs the current `isobox` binary with a temporary `PATH`.
By default it contains only `bwrap` and `slirp4netns`, forcing the fallback.
`--backend pasta` forces pasta; `--backend auto` exposes both installed
backends and exercises the normal pasta-first selection. The system
installation is not changed.

Build and run it as the ordinary user who will use isobox:

```sh
GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath \
  go build -o /tmp/isobox-live ./cmd/isobox
python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-live
python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-live --backend pasta
python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-live --backend auto
python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-live --backend auto --simulate-pasta-failure --local-only
python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-live --backend auto --simulate-pasta-failure --dns-only
```

Each direct-backend run checks both-protocol range forwarding, separate
TCP-only and UDP-only single-port rules (including refusal of the other
protocol), exact loopback host binding, and rejection of an unannounced port.
The default slirp run also checks `off` egress isolation and fail-closed startup
for `off` with ports when pasta is unavailable. Full runs check default and
profile-selected DNS, outbound IPv4 TCP/UDP, and external IPv6 when the host has
an IPv6 default route. External checks use DNS lookups and connection
handshakes only. Pass `--local-only` after the binary path to skip public DNS
and Internet probes.

`--simulate-pasta-failure` wraps the installed pasta binary so capability
probes still use the real binary while runtime startup fails. This checks that
isobox switches to a compatible slirp backend before opening the application
gate. `--dns-only` runs just the default/profile DNS and outbound connectivity
probes; combine it with simulated failure to check resolver fallback. Unit
tests cover the case where both runtime backends fail before the application
gate opens.

It uses only Python's standard library and writes profiles, settings, HOME
directories, and runtime state under a temporary directory.

## pasta off-mode host-forward acceptance

`pasta_off_acceptance.py` exercises `off` mode with one declared TCP+UDP
host port. It checks that the sandbox cannot reach a host listener or public
TCP/UDP DNS endpoint, that host TCP/UDP requests reach the declared app port
and receive replies, and that an unannounced guest TCP listener stays closed.

```sh
python3 ROADMAP/F05-network/source/pasta_off_acceptance.py /tmp/isobox-live
```

Run it as the ordinary isobox user on a host that permits rootless bwrap,
pasta namespace setup, and nftables inside the new network namespace. A failure
before the app reports `READY` is not a networking acceptance pass; inspect
the printed fail-closed error and host restrictions.
