# Egress allowlist port sets

## Goal
Allow one egress rule to grant a list of ports and inclusive port ranges.

## Tasks
- [x] Decode exact ports from `port` lists and ranges from repeated positional `port_range START END;` entries; preserve old single-port syntax.
- [x] Validate each value and enforce union matching in the TUN policy.
- [x] Update `isobox config`, README, and F06 task/context/QA notes.
- [x] Run gofmt, build, and diff checks; do not claim unrun unit/runtime checks.

## Done when
- Existing `port 443;` and new exact/range forms work with the same fail-closed IP/protocol rules.
- Invalid zero, reversed, or out-of-range values are rejected before launch.
