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
