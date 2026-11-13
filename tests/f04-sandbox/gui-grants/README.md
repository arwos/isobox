# F04 GUI grant runtime check

Run `./tests/run.sh f04-gui-grants` as the regular user, without `sudo`. The
runner builds isobox, creates temporary synthetic Wayland, X11, PipeWire,
PulseAudio, and session-bus Unix sockets, then enables one profile grant at a
time and checks which resources and environment reach the app. It also checks
that default-deny exposes no GUI resources, that the session bus does not
include the system bus, and that unavailable or invalid grants fail before the
app marker is written. GPU access is checked against devices present on the
host; if none are available, the runner requires GPU-enabled launch to fail
closed. The synthetic echo services verify socket forwarding and access; they
do not emulate the Wayland, X11, audio, or D-Bus application protocols.

The runner needs Go, Python 3, bubblewrap, enabled user namespaces, and
`timeout`. It briefly creates a uniquely numbered socket under
`/tmp/.X11-unix` and removes only the socket it created. Each run saves
`result.txt` and logs under `${ISOBOX_TEST_RESULTS:-/tmp/isobox-test-results}`.
Send the printed result when it completes.
