# sing-box SOCKS5 test fixture

Run `./tests/f06-proxy/sing-box/run.sh` as your regular user. The script starts
the pinned official sing-box image in rootless Podman, checks the isobox SOCKS5
client against local TCP and UDP echo sockets, verifies wrong-password rejection,
stops the container, and confirms the SOCKS listener is no longer reachable.
The server listens on host loopback only. The test destinations are also host
loopback, so the fixture does not require external Internet access.

The first run may need Internet access to pull
`ghcr.io/sagernet/sing-box:v1.14.2`. Credentials in `config.json` are fixed,
non-secret test values and must not be reused. sing-box is only a test server;
it is not an isobox runtime dependency.

Requirements: Linux, Podman with working rootless storage, Go, and Python 3.
Do not run the script with `sudo`. If port 10808 is busy, pass another port:

```sh
./tests/f06-proxy/sing-box/run.sh --port 18088
```

The report path is printed at the end. Send `result.txt` from that directory;
it includes Podman diagnostics, test output, and sing-box logs. To choose a
different report directory, set `ISOBOX_TEST_RESULTS`.
