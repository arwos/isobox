# Egress policy event logging

## Goal
Write failed DNS resolution and denied destination events from `egress` launches to `/var/log/isobox-<app-id>.log`.

## Tasks
- [x] Pass the app ID into the host-side TUN policy and open an owner-only append log safely; warn and continue if `/var/log` is not writable.
- [x] Log denied destinations and DNS resolution failures without logging packet contents or credentials.
- [x] Document log location, permissions, and fallback behavior; update F06 context and QA notes.
- [x] Run gofmt, build, and diff checks; leave unit and runtime checks unclaimed unless run.

## Done when
- [x] `egress` logs DNS failure details and denied IP/port/protocol events to its app-specific file when writable.
- [x] Failure to open the log is visible on stderr and does not change egress policy or open a network path.
