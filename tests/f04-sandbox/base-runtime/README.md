# F04 base bwrap runtime check

Run `./tests/run.sh f04-base-runtime` as a regular user, without `sudo`. The
scenario builds isobox and checks the isolated root, `/proc`, `/dev`, `/tmp`,
`/run`, `/var/tmp`, clean environment, read-only system paths, exact app argv
and exit status, SIGTERM forwarding, and fail-closed behavior when bwrap reports
a required `/usr` bind failure. The last case uses a temporary bwrap shim and
does not modify host system files.

The host needs Go, Python 3, bubblewrap, enabled user namespaces, and `timeout`.
Each run saves `result.txt`, logs, and events under
`${ISOBOX_TEST_RESULTS:-/tmp/isobox-test-results}`. Send the printed result if
the scenario fails or completes.
