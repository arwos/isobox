# F06 proxy runtime check

Run `./tests/f06-proxy/runtime/run.sh` as the regular user, without `sudo`.
The script starts a loopback-only, no-auth sing-box SOCKS5 fixture in rootless
Podman, builds isobox, and launches a small TCP server in proxy mode.

The scenario checks three observable paths:

- sandbox TCP and UDP connections reach an echo service on a host IPv4 address
  through SOCKS5;
- a DNS query for a reserved `.invalid` name receives a negative DNS answer
  through the host's first IPv4 resolver and SOCKS5;
- a host connection to the profile's published TCP port reaches the sandbox
  and gets a response through pasta's marked return route;
- the isobox command exits successfully and its log contains all checkpoints.

The echo destination is selected from the host's IPv4 interfaces, so the
scenario needs no external HTTP service. The host's DNS resolver must return a
negative answer for the reserved `.invalid` query. It requires Go, Python 3,
Podman, `bwrap`, `pasta`, `nft`, user namespaces, and a usable `/dev/net/tun`.

Every run creates an isolated result directory below
`${ISOBOX_TEST_RESULTS:-/tmp/isobox-test-results}`. Share the printed
`result.txt`; it contains the environment, generated profile, and captured
build, proxy, app, echo-server, and Podman logs. The Podman container and helper
processes are stopped by the runner on exit.
