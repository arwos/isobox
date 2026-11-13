# F06 SOCKS5 client checks

Run from the repository root:

```sh
./tests/f06-proxy/client/run.sh
```

The script runs the package-level SOCKS5 checks and saves `result.txt` plus the
raw Go test output in a new folder under `/tmp/isobox-test-results/`. Send me
the `result.txt` file; include `test.log` if the report shows a failure.

To also run Go's race detector:

```sh
./tests/f06-proxy/client/run.sh --race
```

These tests use an in-process fake SOCKS5 server and a fake UDP socket, so they
do not need root, Podman, a live proxy, or external network access. They cover
SOCKS5 negotiation, authentication, TCP CONNECT, UDP ASSOCIATE, IPv4/domain
addresses, failure handling, cancellation, and the absence of a direct retry.
They do not yet verify the application's real TUN path; that requires the
separate F06 runtime integration work.
