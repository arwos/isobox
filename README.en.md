# isobox

[RUSSIAN README](./README.md)

---

`isobox` runs Linux applications in an isolated environment based on [bubblewrap](https://github.com/containers/bubblewrap). A profile controls access to files, the network, the graphical session, and system resources, and can define a default application command. The user can override it with arguments after `--`.

## Features

- A minimal filesystem with system directories mounted read-only and explicit profile-defined mounts.
- An isolated `HOME`: an existing directory can be set in the profile; otherwise, a persistent directory is created under `~/.isobox/home/<app-id>/`.
- Network modes `off`, `direct`, `proxy`, and `egress`. Networking is IPv4-only; only explicitly declared incoming ports can be published.
- `proxy` routes TCP and UDP through an external SOCKS5 server. `egress` uses a built-in IPv4 policy: only explicitly allowed IPs/CIDRs, ports, and protocols can reach external destinations.
- Separate permissions for Wayland, X11, audio, GPU, and the user's session D-Bus.
- CPU, memory, task-count, and open-file limits when supported by the host.
- Environment variables are set explicitly in the profile; the host environment is cleared before the application starts.
- A default profile is created on the first run without `--profile`; optional user desktop launchers and systemd user units are available.
- `isobox ps` displays a table of applications launched through isobox, including their PID, app-id, working directory, memory, CPU, and network mode.

## Requirements

- Linux with user namespaces enabled.
- Go `1.26.8` to build from source.
- `bubblewrap` (`bwrap`). `direct` mode requires `pasta` or a compatible `slirp4netns`; `proxy` and `egress` require an available `/dev/net/tun`. `proxy` also requires a configured SOCKS5 alias.
- `off`, `proxy`, or `egress` with published ports requires `pasta` and `nft`. CPU, memory, and process limits require cgroup v2 and the user's `systemd-run`.

Some features depend on kernel settings, the user session, and installed programs. isobox does not install system dependencies and stops the launch if it cannot apply a required restriction.

The sandbox also creates a separate cgroup namespace so the application sees only a virtualized path for its cgroup. CPU, memory, and task-count limits are still applied through a user `systemd-run` scope and checked by isobox before the application starts.

## Install with go install

With Go `1.26.8` or later installed, install the latest CLI version:

```sh
go install github.com/arwos/isobox/cmd/isobox@latest
```

Go places the binary in `GOBIN` if it is set, or otherwise in `$(go env GOPATH)/bin`. Make sure that directory is in `PATH`, then verify the installation:

```sh
isobox help
```

`go install` installs only isobox. Install system dependencies, including `bubblewrap` and network backends, separately.

## Build

```sh
git clone https://github.com/arwos/isobox.git
cd isobox
go build -o isobox ./cmd/isobox
```

Check that the CLI is available:

```sh
./isobox help
```

## Quick start

Create a file named `minimal.unic`:

```unic
version 1;

network {
  mode off;
}
```

Run an application by passing its command after `--`:

```sh
./isobox run --app-id=demo -- /usr/bin/true
```

The command looks for `demo.unic` in the user configuration directory and uses the default profile if that file does not exist. To run the `minimal.unic` created above, specify it explicitly:

```sh
./isobox run --profile=./minimal.unic --app-id=demo -- /usr/bin/true
```

To inspect the calculated launch parameters without starting the application, use `config` with the same flags and command:

```sh
./isobox config --profile=./minimal.unic --app-id=demo -- /usr/bin/true
```

The command prints a table containing the effective profile, environment, network backend, limits, the egress log path for `egress` mode, and the full list of bubblewrap arguments. It validates the profile and runs host preflight checks, but does not start the application.

All arguments after `--` are passed to the application unchanged and replace the profile's `command`. If no command follows `--`, the profile's command is used. If neither command is set, isobox reports an error. You can run a profile without `--app-id` when it has a `home_path`; otherwise, `--app-id` selects a persistent HOME at `~/.isobox/home/<app-id>/`.

The application starts in `/home/isobox` inside the sandbox; `PWD` is also set to `/home/isobox`.

For `direct`, `proxy`, and `egress`, you can set the network interface name and an IPv4 subnet:

```unic
network isobox0 10.10.10.1/24 {
  mode direct;
}
```

Both positional values are optional: `network isobox0 { ... }` sets only the name, while `network 10.10.10.1/24 { ... }` sets only the subnet. When a subnet is specified, isobox randomly selects a container address from its host addresses. In `direct` mode with pasta, it also selects a separate gateway address. In `proxy` and `egress`, the container, DNS, and host gateway addresses are selected separately from the same subnet; therefore, `host.gateway.internal` always points to the virtual gateway in the configured network. Without a subnet, the default DNS `10.123.123.2` and host gateway `10.123.123.3` are used. A custom network in `direct` mode requires pasta with `--ns-ifname`; when a subnet is specified, it also requires `--address`, `--netmask`, and `--gateway`. Launching with slirp4netns in this case fails before the application starts. Interface, DNS, and gateway addresses appear in the `isobox config` table.

By default, the sandbox keeps the current effective host UID/GID, while the user and primary group are named `isobox`. The host `/etc/passwd` and `/etc/group` are not mounted. A profile can set a guest UID/name and multiple groups: the user's UID and the first group's GID are mapped to the host user who started isobox. Additional groups are added as account entries in the private `/etc/group`; they are not supplementary process GIDs and do not by themselves grant access to host files or devices.

The hostname in the container's UTS namespace is `isobox-<app-id>`, or `isobox` when `--app-id` is not set. In the container's `/etc/hosts`, `localhost` and this hostname point to `127.0.0.1`, the sandbox's own loopback. Linux limits hostnames to 64 bytes, so an app-id that is too long is truncated to 57 bytes for the hostname; the app-id and managed HOME path are unchanged.

## Profile

Profile files use the UNIC format. This example allows the application to read `/opt/my-app/data`, sets a separate `HOME`, and enables Wayland only:

```unic
version 1;
user 1000 alice;
group 1000 alice;
group 44 video;
home_path '/home/alice/.local/share/my-app';
command ['/usr/bin/my-app', '--safe-mode'];

env (TERM, 'xterm-256color');
path ['/opt/my-app/data/bin'];

mount {
  source '/opt/my-app/data';
  target '/opt/my-app/data';
  read_only true;
}

network {
  mode off;
}

gui {
  wayland true;
}
```

All profile parameters:

| Path | Type and allowed values | Defaults and constraints |
| --- | --- | --- |
| `version` | integer | `1`; other versions are rejected |
| `user` | One `user UID NAME;` entry, for example `user 1000 alice;` | If omitted, uses the launching user's UID and the name `isobox`; UID is an integer from `0` to `4294967294`, and the name is up to 32 characters matching `[a-z_][a-z0-9_-]*` |
| `group` | Repeatable `group GID NAME;` entry, for example `group 1000 alice; group 44 video;` | If omitted, uses the launching user's GID and the name `isobox`; the first entry is the process's primary GID, later entries appear only in `/etc/group`; GID is an integer from `0` to `4294967294`, names use the same rules as `user`; duplicate names/IDs are rejected |
| `command` | String list containing the executable and arguments, for example `command ['/usr/bin/my-app', '--safe-mode'];` | Unset; used if no command is passed after `--`; an empty list requires a command on the CLI |
| `home_path` | Absolute path to a directory on the host | Unset; `/` is forbidden; the directory must be accessible at launch |
| `env` | Map of string names and values, for example `env (TERM, 'xterm-256color');` | Empty; host environment is cleared. Names must match `[A-Za-z_][A-Za-z0-9_]*`; NUL is forbidden in values. `HOME`, `PATH`, `PWD`, `TMPDIR`, and `GOMAXPROCS` are reserved |
| `path` | List of absolute directories inside the sandbox, for example `path ['/opt/my-app/data/bin'];` | Empty; entries are prepended to the built-in sandbox `PATH`; host `PATH` is not inherited. A directory must be available inside the sandbox (for example, mounted with `mount`); `:` is forbidden in entries |
| `mount` | Repeatable block | No mounts by default; see the fields below |
| `mount.source` | Absolute path to a source on the host | Required; `/` is forbidden; existence and access are checked at launch |
| `mount.target` | Absolute path inside the sandbox | Required; `/` is forbidden; mount targets must be unique |
| `mount.read_only` | `true` or `false` | `true`; set `false` explicitly to allow writes |
| `network.mode` | `off`, `direct`, `proxy`, `egress` | `off`; an empty value uses the built-in `off` default |
| `network.group` | Repeatable name of a created network, for example `group frontend; group backend;` | Up to 16 groups; create names with `isobox network create`; an address is allocated automatically for each group; supports `off`, `direct`, `proxy`, and `egress`; grouped launches require `--app-id`; published ports are not supported yet |
| `network.alias` | Repeatable DNS name, for example `alias api; alias api.internal;` | Names are validated as DNS domains and published with the app-id in every sandbox group; visible only to members of shared groups |
| `network` positional value 1 | Interface name (up to 15 ASCII characters) or IPv4 CIDR, for example `network isobox0 { ... }` or `network 10.10.10.1/24 { ... }` | Optional; `lo` and invalid names are rejected; a CIDR in the first position means no name was set |
| `network` positional value 2 | IPv4 CIDR, for example `10.10.10.1/24` | Optional; applies to `direct`, `proxy`, and `egress`; a custom network in `direct` mode requires compatible pasta |
| `network.dns` | Repeatable IPv4 address without a port; order sets upstream priority, for example `dns 1.1.1.1; dns 1.0.0.1;` | A profile list replaces `settings.defaults.dns` entirely; without a list, `direct` uses its backend's DNS, `proxy` uses the first IPv4 host resolver, and `egress` does not use the host resolver (an explicit global default still applies) |
| `network.proxy_alias` | SOCKS5 alias | Required for `mode proxy`; forbidden with `off`, `direct`, and `egress` |
| `network.proxy.allow` | Repeatable rule block for IPv4/CIDR access | In `egress`, an empty list blocks all outbound traffic; in `proxy`, the list is optional and filters destinations before SOCKS5 |
| `network.proxy.allow.ip` | One or more IPv4 addresses/CIDRs; the field can be repeated or set as a list, for example `ip ['8.8.8.8/24', '1.1.1.1/24'];` | At least one address is required; IPv6 is forbidden |
| `network.proxy.allow.port` | List of ports `1–65535`, for example `[443, 8443]`; a single `port 443;` is also supported | If unset, all ports for a matching IP/CIDR are allowed |
| `network.proxy.allow.port_range` | Repeatable `port_range START END;` entry, for example `port_range 8080 8090;` | Unset; ranges are inclusive and combined with the `port` list |
| `network.proxy.allow.port_range` value 1 | Start port `1–65535` | Required |
| `network.proxy.allow.port_range` value 2 | End port `1–65535` | Required; must be greater than or equal to the start port |
| `network.proxy.allow.protocol` | `both`, `tcp`, `udp` | `both` |
| `network.resolver.record` | Repeatable block with the domain as the first positional value, for example `record service.example { ... }` | In `egress`, A records are answered locally and take precedence over upstreams |
| `network.resolver.record.ip` | One or more IPv4 addresses; the field can be repeated or set as a list | At least one address is required; IPv6 is forbidden; up to 16 unique addresses per domain |
| `network.port` | Repeatable `port HOST_ADDRESS PORT [PROTOCOL [END_PORT]];` entry | Ports are closed unless explicitly declared |
| `network.port` value 1 | IPv4 host bind address | Required; IPv6 is forbidden |
| `network.port` value 2 | Port `1–65535` | Required; used as both the host and sandbox port |
| `network.port` value 3 | `both`, `tcp`, `udp` | `both`; can be omitted |
| `network.port` value 4 | End port `1–65535` | Unset; defines the inclusive end of a range and must be greater than or equal to the second value |
| `gui.wayland` | `true` or `false` | `false` |
| `gui.x11` | `true` or `false` | `false` |
| `gui.audio` | `true` or `false` | `false` |
| `gui.gpu` | `true` or `false` | `false` |
| `gui.desktop` | `true` or `false` | `false`; allows the user's session D-Bus |
| `limits.cpu_quota_percent` | Positive CPU percentage; `100` equals one logical CPU | `0` or omitted means no limit was requested |
| `limits.memory_bytes` | Memory size in bytes | `0` or omitted means no limit was requested |
| `limits.process_count` | Number of tasks, including threads | `0` or omitted means no limit was requested |
| `limits.open_files` | Number of open file descriptors | `0` or omitted means no limit was requested |

## Network modes

All modes use an IPv4-only namespace. Incoming ports remain closed unless explicitly declared with `network.port`.

### `off`

| Parameter | Behavior |
| --- | --- |
| Outbound traffic | Blocked to the Internet, LAN, and host. Loopback is available only inside the sandbox. |
| Incoming ports | Only explicit `network.port <IPv4 host address> <port> [protocol [end_port]]` entries; requires `pasta` and `nft`. Replies to accepted connections are allowed. |
| DNS and IPv6 | No external DNS; IPv6 is disabled. |
| Interface name and CIDR | Not supported. |

### `direct`

| Parameter | Behavior |
| --- | --- |
| Outbound traffic | Rootless IPv4 network via `pasta` or compatible `slirp4netns`; no IP allowlist is applied. |
| DNS | IPv4 DNS from `network.dns` or the selected backend. IPv6 DNS is unsupported. |
| Incoming ports | Only explicit `network.port` entries; automatic port forwarding is disabled. |
| Interface name and CIDR | Either value can be set independently. A container address and separate gateway are randomly selected from a configured CIDR. Compatible `pasta` is required; `slirp4netns` is not used with a custom network because it assigns a fixed guest IP. |

### `proxy`

| Parameter | Behavior |
| --- | --- |
| Outbound traffic | Requires `network.proxy_alias`. TCP and UDP go through external SOCKS5; there is no direct fallback. |
| DNS | Queries use the same SOCKS5 route. By default, the first IPv4 host resolver is used; `network.dns` can override it. If `network.proxy.allow` is set, it must allow the DNS server IP and port 53. |
| Filtering | `network.proxy.allow` is optional; if set, it filters destinations by IPv4/CIDR, ports, and protocols before SOCKS5. |
| Incoming ports | Explicit `network.port` entries use guarded `pasta` ingress; requires `pasta` and `nft`. |
| Interface name and CIDR | Either value can be set independently. With a CIDR, the container, DNS endpoint, and gateway addresses are selected separately. Without one, the interface is `isobox0`, the address is `10.123.123.1/24`, and the DNS endpoint is `10.123.123.2`. |
| ICMP and IPv6 | The TUN supports only IPv4 TCP/UDP; IPv6, ICMP, and raw IP do not pass. `ping` does not test network or SOCKS5 availability. |

### `egress`

| Parameter | Behavior |
| --- | --- |
| Outbound traffic | The built-in TUN connects directly to allowed IPv4 destinations. An empty `network.proxy.allow` blocks all egress; no SOCKS5 alias is used. |
| Allowlist | Rules specify IP/CIDR and optional ports/ranges with protocol `tcp`, `udp`, or `both`. Blocking happens before the host-side dial. |
| DNS | `network.resolver.record` and group member names are served locally. Unknown single-label names return NXDOMAIN; other unknown names use only an explicitly configured `network.dns`, and each upstream requires an allow rule for port 53. Without an upstream, external unknown names do not resolve. The reserved `host.gateway.internal` name returns the gateway's A record locally and NODATA for AAAA/other types. |
| Host loopback | `/etc/hosts` maps `host.gateway.internal` to the virtual gateway. To connect to host loopback, separately allow the target `127.0.0.1`, port, and protocol. The gateway appears in `isobox config`; without a custom subnet it is `10.123.123.3`. |
| Incoming ports | Explicit `network.port` entries use guarded `pasta` ingress; requires `pasta` and `nft`. |
| Interface name and CIDR | Either value can be set independently. With a CIDR, the container, DNS endpoint, and gateway addresses are selected separately. Without one, the interface is `isobox0`, the address is `10.123.123.1/24`, the DNS endpoint is `10.123.123.2`, and the gateway is `10.123.123.3`. |
| ICMP and IPv6 | The TUN supports only IPv4 TCP/UDP; IPv6, ICMP, and raw IP do not pass. `ping` does not test gateway availability. |

Profile mounts cannot cover the service-managed `/etc/hosts` or network `/etc/resolv.conf`. A profile `command` is an argv array, not a shell string; isobox does not start a shell automatically.

In `egress` mode, DNS resolution errors and blocked destinations are written to `~/.isobox/log/<app-id>.log`; if app-id is unset, the path is `~/.isobox/log/default.log`. Directories are created with permissions `0700`, and the file with `0600`. Events contain the domain or IP, port/protocol, and block reason, but not traffic contents. If the file cannot be accessed, isobox reports the error to stderr and continues with the same network policy.

Example `egress` profile with multiple allowed networks, individual ports and a range, and a static DNS record:

```unic
network {
  mode egress;
  dns 1.1.1.1;
  dns 1.0.0.1;
  proxy {
    allow {
      ip '203.0.113.10';
      ip '198.51.100.0/24';
      port [443, 8443];
      port_range 9000 9100;
      protocol tcp;
    }
    allow {
      ip '1.1.1.1';
      ip '1.0.0.1';
      port 53;
      protocol both;
    }
  }
  resolver {
    record service.example {
      ip '203.0.113.10';
      ip '203.0.113.11';
    }
  }
}
```

Example publishing TCP ports on host loopback:

```unic
network {
  mode off;
  port 127.0.0.1 8080 tcp;
  port 127.0.0.1 8081 tcp;
}
```

Network permissions are never expanded automatically: in particular, undeclared incoming ports remain closed and IPv6 is disabled.

## SOCKS5

Add a proxy alias. With `userpass`, the program prompts for the username and password in the terminal; they are not passed as command-line arguments.

```sh
./isobox proxy add --alias=work --server=proxy.example:1080 --auth=userpass
./isobox proxy list
```

Set the alias in the profile:

```unic
network {
  mode proxy;
  proxy_alias 'work';
}
```

The proxy store is kept in the user configuration directory and has restricted permissions. SOCKS5 does not encrypt the username, password, or application traffic: use a trusted network or a protected transport to the proxy. Remove an alias with `./isobox proxy remove --alias=work`.

## Named network groups

Create a persistent rootless bridge network with an IPv4 subnet:

```sh
./isobox network create --name=backend --subnet=192.168.40.0/24
./isobox network list
./isobox network remove --name=backend
```

You can specify an address with a CIDR, such as `192.168.40.1/24`; isobox uses the entire subnet and stores its canonical form, `192.168.40.0/24`. Prefixes `/8` through `/29` are supported; the first available address is reserved as the bridge gateway. Network definitions are stored in `~/.isobox/networks/`, and a shared user-level `isobox-network-manager.service` under `systemd --user` keeps all bridges active. `create` succeeds only after the bridge is ready; `list` shows manager status and the number of member interfaces; `remove` refuses to proceed while members are connected to the bridge.

Linux with rootless user/network namespaces and a running user systemd manager is required. The manager automatically starts created networks when the user session starts. Starting the user manager before interactive login requires systemd lingering; isobox does not enable it automatically.

Connect a sandbox to one or more networks by repeating `group` in the profile:

```unic
network {
  mode off;
  group frontend;
  group backend;
}
```

Each launch gets a separate network namespace, its own loopback, and a random available IPv4 address from each selected subnet. A separate connected route is created for each group; traffic to its subnet goes directly between members and does not pass through the external backend. The manager publishes app-id and all `alias` values through local DNS only to members of shared groups and removes records when a process exits. An absent single-label name (for example, the app-id of a stopped container) receives a local NXDOMAIN response and is not forwarded to external DNS; other unknown names follow the network mode's DNS policy. If the manager or an external DNS upstream is unavailable, the group resolver returns SERVFAIL over UDP and TCP.

External network behavior when connected to groups:

| `network.mode` | Connectivity to members of shared groups | External traffic |
| --- | --- | --- |
| `off` | Allowed by IP and DNS names of members in connected groups | Blocked; there is no default route |
| `direct` | Uses the group's connected route | Uses the regular `pasta`/`slirp4netns` direct backend |
| `proxy` | Goes directly only to connected subnets | Other IPv4 packets use the SOCKS5 policy TUN; no direct fallback |
| `egress` | Goes directly only to connected subnets | Other destinations use the built-in allowlist policy TUN |

Group membership does not grant access to host loopback, the host/LAN, or other groups. Do not use overlapping CIDRs in networks selected by the same launch. Group profiles do not support `network.port` yet.

## Default profile and desktop launcher

Without `--profile`, isobox first looks for `~/.isobox/config/<app-id>.unic`. If `--app-id` is unset or `<app-id>.unic` does not exist, it uses `~/.isobox/config/default.unic`. On the first run without a matching file, `default.unic` is created with permissions `0600` and the safe network mode `off`:

```unic
version 1;
network {
  mode off;
}
```

Edit `~/.isobox/config/default.unic` to set default permissions, `env`, and `path` additions. You can create an app-specific file such as `~/.isobox/config/my-app.unic` for `--app-id=my-app`; it completely replaces `default.unic`, and the two files are not merged. If `--profile` is provided, only that profile is used; automatic profile lookup is skipped. `XDG_CONFIG_HOME` does not affect the profile directory. Profiles from the old `~/.config/isobox/` directory and managed HOME directories from `/tmp/isobox-<uid>/` are not migrated automatically.

Install or remove a user desktop launcher:

```sh
./isobox integration desktop install \
  --id=my-app --name='My App' --profile=/home/alice/.config/isobox/my-app.unic \
  --app-id=my-app -- /usr/bin/my-app
./isobox integration desktop remove --id=my-app
```

### User systemd service

Install a unit for the `command` defined in the profile:

```sh
./isobox integration systemd install --profile=/home/alice/.config/isobox/my-app.unic --app-id=my-app
```

Installation fails if the profile has no `command`. The unit is stored in `$XDG_CONFIG_HOME/systemd/user/` (by default `~/.config/systemd/user/`) and reloads the user systemd manager. Installation does not enable or start the service. Manage autostart as follows:

```sh
./isobox integration systemd enable --app-id=my-app
./isobox integration systemd disable --app-id=my-app
```

`enable` enables startup with the user's `default.target` but does not start the service immediately. To start or stop the current process, use `systemctl --user start/stop isobox-my-app.service`. `disable` turns off autostart without stopping a running service. Removal stops and disables the isobox-owned unit before deleting its file:

```sh
./isobox integration systemd remove --app-id=my-app
```

## List running applications

```sh
./isobox ps
```

The command prints a table with `PID`, `APP-ID`, `WORKING DIRECTORY`, `MEMORY`, `CPU`, and `INTERNET`. Memory and CPU are for the application PID; CPU is measured over a short interval when `ps` runs. `INTERNET` shows the profile's network mode (`off`, `direct`, `proxy`, or `egress`), not whether an external address is reachable. Runs started before this version was installed are not listed.

## Checks

Run Go checks from the repository root:

```sh
go test ./...
go vet ./...
go build ./...
```

Host and runtime acceptance scenarios are available with `./tests/run.sh --list`. Run them as a regular user, not through `sudo`; some scenarios require networking, a graphical session, Podman, or user systemd/cgroup support. Requirements and scenario descriptions are in [`tests/README.md`](tests/README.md).

## License

MIT. See [`LICENSE`](LICENSE).
