# Live acceptance for slirp4netns

`slirp_acceptance.py` runs the current `isobox` binary with a temporary `PATH`
that contains only `bwrap` and `slirp4netns`. This forces direct mode to use the
slirp fallback without changing the system installation.

Build and run it as the ordinary user who will use isobox:

```sh
GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath \
  go build -o /tmp/isobox-live ./cmd/isobox
python3 ROADMAP/F05-network/source/slirp_acceptance.py /tmp/isobox-live
```

The full run checks `off` egress isolation, fail-closed startup for `off` with
ports when pasta is unavailable, direct TCP/UDP host forwarding for a range,
loopback-only host binding when a routed host IPv4 address is available,
rejection of an unannounced port, default and profile-selected DNS, and outbound
IPv4 TCP/UDP. External checks use DNS lookups and connection handshakes only.
The script skips external IPv6 only when the host has no IPv6 default route.
Pass `--local-only` after the binary path to skip all public DNS and Internet
probes.

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
