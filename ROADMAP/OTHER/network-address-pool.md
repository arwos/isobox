# Configurable container network address

## Goal
Allow `direct`, `proxy`, and `egress` profiles to name the guest interface and optionally allocate a random IPv4 address from a profile CIDR, while leaving defaults unchanged.

## Tasks
- [x] Add positional network attributes, validate interface names/CIDRs, and reject custom networking in `off` → Verified in profile validation and mode resolution.
- [x] Build one per-launch address plan and pass it consistently to pasta and TUN, preserving fixed proxy DNS/gateway endpoints → Custom direct requires compatible pasta; slirp cannot honor the selected address.
- [x] Show the selected interface, subnet, and guest IP in `isobox config`; update README and F05/F06 roadmap/context/QA.
- [x] Format, build, and inspect the diff → `gofmt`, `go build ./...`, and `git diff --check` passed; runtime acceptance remains pending.

## Done When
- [x] `network <ifname> <cidr> { ... }` works for direct/proxy/egress and each supplied attribute is optional.
- [x] A configured CIDR gets a random usable guest IPv4 address without colliding with reserved endpoints.
- [x] Without attributes, existing addressing and interface defaults remain unchanged.
