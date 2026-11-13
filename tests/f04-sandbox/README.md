# F04 mount and HOME runtime check

Run `./tests/f04-sandbox/run.sh` as the regular user, without `sudo`. The
scenario builds isobox and runs isolated network-off applications through
bubblewrap.

It checks that an explicit HOME is mounted at `/home/isobox` and persists at
the configured host path, profile read-only and writable mounts enforce their
permissions, and an unmounted host path is not visible. It also checks that
managed HOME persists for the same profile and `--app-id`, differs for another
profile or app ID, and has owner-only mode `0700`. A profile with an unavailable
mount must fail before its application starts.
The runner removes only the managed-HOME leaf directories it creates for its
temporary profiles; the shared `/tmp/isobox-<uid>` root is left intact.

The runner needs Go, Python 3, `bwrap`, user namespaces, and a real sticky
`/tmp`. Each run saves `result.txt`, the build log, generated runtime logs, and
events under `${ISOBOX_TEST_RESULTS:-/tmp/isobox-test-results}`. Send the printed
`result.txt` if the scenario fails or completes.
