# Configure the sandbox user

## Goal
Allow a profile to select the account name and guest UID/GID used by the application, with the guest UID and primary GID mapped to the host user who launched isobox.

## Tasks
- [x] Add repeated positional `user` and `group` profile fields with strict parsing and validation → Invalid and malformed entries are rejected by the decode/validation paths; targeted tests were not run.
- [x] Apply the selected guest UID/GID to bwrap and generate matching private passwd/group files → Launch argv and account-file generation reviewed; full project build passed.
- [x] Update F02/F04 roadmap, README, CONTEXT, and QA with mapping and supplementary-group behavior → Documentation records that extra groups are not process supplementary GIDs.
- [x] Format changed Go code, build the project, and inspect the final diff → `gofmt`, `go build ./...`, and `git diff --check` passed; runtime acceptance was not run.

## Done When
- [x] The configured UID and first group GID identify the application inside the sandbox and map to the launching host user.
- [x] Additional group records are visible in `/etc/group`, while process supplementary groups are not claimed as active.
- [x] Invalid identity configuration fails before sandbox launch.
