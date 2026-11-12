# Live acceptance for desktop integration

`desktop_acceptance.py` checks optional XDG defaults through real `isobox run`
and launches a generated desktop file through the active desktop session. Run
it as the ordinary user who owns that graphical session. The host needs
`bwrap`, `gio`, and `desktop-file-validate`.

Build and run:

```sh
GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath \
  go build -o /tmp/isobox-f08-live-isobox ./cmd/isobox
python3 ROADMAP/F08-system-integration/source/desktop_acceptance.py /tmp/isobox-f08-live-isobox
```

The script verifies direct CLI use without installed launchers or settings;
selection of the first existing system XDG config; partial user settings and
the user network override; and launch of an installed mode-0600 desktop file
via `gio`. Its profile path, XDG data path, and application argument include
spaces and reserved characters. The payload is `/usr/bin/touch` inside the
sandbox. Removing the launcher must leave the profile and persistent app HOME
in place.

The harness uses Python's standard library and a temporary directory. It
requires a working X11 or Wayland desktop session and refuses to run as root.
