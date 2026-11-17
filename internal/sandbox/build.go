/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

// Package sandbox builds and supervises the bubblewrap process.
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/profile"
)

const sandboxPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type systemMount struct {
	source      string
	destination string
	directory   bool
	required    bool
}

const sandboxHome = "/home/isobox"
const internalOffGuardCommand = "__isobox_internal_off_guard"
const internalIPv4NetworkCommand = "__isobox_internal_ipv4_network"
const internalProxyTUNCommand = "__isobox_internal_proxy_tun"
const internalOffGuardControlDir = "/run/.isobox-control"
const internalOffGuardGatePath = internalOffGuardControlDir + "/gate.sock"
const internalProxyTUNGatePath = internalOffGuardGatePath
const internalSandboxBinaryPath = "/tmp/.isobox-internal"
const internalOffGuardNFTPath = "/tmp/.isobox-nft"

// BuildCommand creates a bwrap command for the supported portion of the
// current sandbox profile. Host paths are limited to fixed runtime paths and
// the mounts explicitly granted by the profile.
func BuildCommand(bubblewrap, nftPath string, config profile.EffectiveProfile, profilePath, appID string, application []string, network NetworkRuntime) (*exec.Cmd, error) {
	if bubblewrap == "" {
		return nil, fmt.Errorf("bwrap executable path is required")
	}
	if len(application) == 0 || application[0] == "" {
		return nil, fmt.Errorf("application command is required")
	}
	if err := validateSupportedProfile(config, network); err != nil {
		return nil, err
	}

	systemMounts, err := availableSystemMounts()
	if err != nil {
		return nil, err
	}
	profileMounts, err := resolveProfileMounts(config.Mounts)
	if err != nil {
		return nil, err
	}
	if err := validateProfileMountTargets(profileMounts); err != nil {
		return nil, err
	}
	gui, err := resolveGUI(config.GUI)
	if err != nil {
		return nil, err
	}
	profileMounts = append(profileMounts, gui.mounts...)
	if err := validateProfileMountTargets(profileMounts); err != nil {
		return nil, err
	}
	homeSource, err := resolveHome(config.HomePath, profilePath, appID)
	if err != nil {
		return nil, err
	}
	profileMounts = append(profileMounts, profileMount{source: homeSource, target: sandboxHome, writable: true})
	if err := validateProfileMountTargets(profileMounts); err != nil {
		return nil, err
	}
	for _, mount := range profileMounts {
		if config.NetworkMode == profile.NetworkModeDirect || config.NetworkMode == profile.NetworkModeProxy || config.NetworkMode == profile.NetworkModeEgress {
			if mountTargetsOverlap(mount.target, "/etc/resolv.conf") {
				return nil, fmt.Errorf("profile mount target %q conflicts with the network DNS configuration", mount.target)
			}
		}
		if mountTargetsOverlap(mount.target, "/etc/hosts") {
			return nil, fmt.Errorf("profile mount target %q conflicts with the sandbox hosts entries", mount.target)
		}
	}
	sortProfileMounts(profileMounts)

	offGuard := config.NetworkMode == profile.NetworkModeOff && len(config.Network.Ports) != 0
	directNetwork := config.NetworkMode == profile.NetworkModeDirect
	proxyNetwork := config.NetworkMode == profile.NetworkModeProxy || config.NetworkMode == profile.NetworkModeEgress
	proxyIngress := proxyNetwork && len(config.Network.Ports) != 0
	ipv4Setup := !network.SharedNamespace && (directNetwork || config.NetworkMode == profile.NetworkModeOff && len(config.Network.Ports) == 0)
	activeBackend := network.activeBackend()
	hostUID, hostGID := os.Geteuid(), os.Getegid()
	if network.HasHostIdentity {
		hostUID, hostGID = network.HostUID, network.HostGID
	}
	identity := sandboxIdentity(config.Profile, hostUID, hostGID)
	args := []string{
		"--unshare-user",
		"--uid", strconv.FormatUint(uint64(identity.user.ID), 10),
		"--gid", strconv.FormatUint(uint64(identity.groups[0].ID), 10),
	}
	// Policy TUN modes create their network namespace in the trusted helper, after
	// bwrap has finished its user-namespace setup for --dev/devpts. That keeps
	// CAP_NET_ADMIN in the user namespace that owns the network namespace.
	// The helper installs namespace-blocking seccomp before app exec.
	// The off-mode guard creates its netns after bwrap mounts too, because /dev
	// setup can change the user namespace that should own the network namespace.
	if network.SharedNamespace || !offGuard && !proxyNetwork {
		args = append(args, "--disable-userns", "--assert-userns-disabled")
	}
	args = append(args,
		"--unshare-pid",
		"--unshare-cgroup",
		"--unshare-ipc",
		"--unshare-uts",
		"--hostname", sandboxHostname(appID),
		"--new-session",
		"--die-with-parent",
	)
	if network.SharedNamespace {
		args = append(args, "--share-net")
	} else if !offGuard && !ipv4Setup && !proxyNetwork {
		args = append(args, "--unshare-net")
	}
	args = append(args, "--dir", "/etc")
	for _, mount := range systemMounts {
		args = append(args, "--ro-bind", mount.source, mount.destination)
	}
	accountFD := 3
	resolverConfigured := directNetwork || proxyNetwork || network.SharedNamespace
	if resolverConfigured {
		accountFD++ // fd 3 contains the sandbox resolver.
	}
	args = append(args,
		"--ro-bind-data", strconv.Itoa(accountFD), "/etc/passwd",
		"--ro-bind-data", strconv.Itoa(accountFD+1), "/etc/group",
	)
	args = append(args, "--ro-bind-data", strconv.Itoa(accountFD+2), "/etc/hosts")
	if resolverConfigured {
		args = append(args, "--ro-bind-data", "3", "/etc/resolv.conf")
	}
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--perms", "01777", "--tmpfs", "/tmp",
		"--tmpfs", "/run",
		"--perms", "01777", "--tmpfs", "/var/tmp",
		"--dir", "/home",
		"--dir", "/root",
	)
	if gui.runtimeDir {
		args = append(args, "--perms", "0700", "--dir", sandboxGUIRuntime)
	}
	for _, mount := range profileMounts {
		option := "--bind"
		switch {
		case mount.device:
			option = "--dev-bind"
		case !mount.writable:
			option = "--ro-bind"
		}
		args = append(args, option, mount.source, mount.target)
	}
	applicationPath := sandboxPath
	if len(config.Path) > 0 {
		applicationPath = strings.Join(config.Path, ":") + ":" + sandboxPath
	}
	args = append(args,
		"--clearenv",
		"--setenv", "PATH", applicationPath,
		"--setenv", "HOME", sandboxHome,
		"--setenv", "TMPDIR", "/tmp",
	)
	environment := make([]string, 0, len(gui.environment))
	for name := range gui.environment {
		environment = append(environment, name)
	}
	sort.Strings(environment)
	for _, name := range environment {
		args = append(args, "--setenv", name, gui.environment[name])
	}
	profileEnvironment := make([]string, 0, len(config.Env))
	for name := range config.Env {
		profileEnvironment = append(profileEnvironment, name)
	}
	sort.Strings(profileEnvironment)
	for _, name := range profileEnvironment {
		args = append(args, "--setenv", name, config.Env[name])
	}
	if ipv4Setup || offGuard {
		// Keep the trusted setup helper's Go runtime within the task budget. The
		// value is removed by the helper before it execs the application.
		args = append(args, "--setenv", "GOMAXPROCS", "1")
	}
	if proxyNetwork && !network.SharedNamespace {
		args = append(args, "--setenv", "GOMAXPROCS", "1")
	}
	if ipv4Setup {
		binary, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate isobox executable for IPv4-only setup: %w", err)
		}
		binary, err = filepath.EvalSymlinks(binary)
		if err != nil {
			return nil, fmt.Errorf("resolve isobox executable for IPv4-only setup: %w", err)
		}
		args = append(args,
			"--ro-bind", binary, internalSandboxBinaryPath,
			"--cap-add", "CAP_SYS_ADMIN",
			"--cap-add", "CAP_NET_ADMIN",
		)
	}
	if offGuard {
		if !filepath.IsAbs(nftPath) {
			return nil, fmt.Errorf("an absolute nftables executable path is required for published ports")
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate isobox executable for the off-mode firewall helper: %w", err)
		}
		binary, err = filepath.EvalSymlinks(binary)
		if err != nil {
			return nil, fmt.Errorf("resolve isobox executable for the off-mode firewall helper: %w", err)
		}
		args = append(args,
			"--ro-bind", binary, internalSandboxBinaryPath,
			"--cap-add", "CAP_SYS_ADMIN",
			"--cap-add", "CAP_NET_ADMIN",
		)
	}
	if proxyNetwork {
		if proxyIngress && !filepath.IsAbs(nftPath) {
			return nil, fmt.Errorf("an absolute nftables executable path is required for proxy mode with published ports")
		}
		binary, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate isobox executable for the proxy TUN helper: %w", err)
		}
		binary, err = filepath.EvalSymlinks(binary)
		if err != nil {
			return nil, fmt.Errorf("resolve isobox executable for the proxy TUN helper: %w", err)
		}
		args = append(args,
			"--ro-bind", binary, internalSandboxBinaryPath,
			"--cap-add", "CAP_SYS_ADMIN",
			"--cap-add", "CAP_NET_ADMIN",
			"--dir", "/dev/net",
			"--dev-bind", "/dev/net/tun", "/dev/net/tun",
		)
		if proxyIngress {
			args = append(args, "--ro-bind", nftPath, internalOffGuardNFTPath)
		}
	}
	if !offGuard && !proxyNetwork && ((!ipv4Setup && activeBackend != "none") || limits.Any(config.Limits)) {
		args = append(args, "--block-fd", "3")
	}
	if network.SharedTunnel && limits.Any(config.Limits) {
		args = append(args, "--block-fd", "3")
	}
	args = append(args,
		"--unsetenv", "PWD",
		"--chdir", sandboxHome,
		"--setenv", "PWD", sandboxHome,
		"--json-status-fd", "3",
		"--",
	)
	if offGuard {
		tcpPorts, udpPorts, err := firewallPortSets(config.Network.Ports)
		if err != nil {
			return nil, err
		}
		if tcpPorts == "" {
			tcpPorts = "-"
		}
		if udpPorts == "" {
			udpPorts = "-"
		}
		args = append(args, internalSandboxBinaryPath, internalOffGuardCommand, nftPath, tcpPorts, udpPorts, "--gate-path", internalOffGuardGatePath, "--")
	} else if ipv4Setup {
		if directNetwork {
			args = append(args, internalSandboxBinaryPath, internalIPv4NetworkCommand, "--wait-for-network", "--")
		} else {
			args = append(args, internalSandboxBinaryPath, internalIPv4NetworkCommand, "--")
		}
	} else if proxyNetwork && !network.SharedNamespace {
		args = append(args, internalSandboxBinaryPath, internalProxyTUNCommand)
		if config.Network.InterfaceName != "" {
			args = append(args, "--interface", network.InterfaceName)
		}
		if network.GuestAddress != "" {
			prefix, _ := netip.ParsePrefix(network.Subnet)
			args = append(args, "--address", network.GuestAddress+"/"+strconv.Itoa(prefix.Bits()))
		}
		if proxyIngress {
			tcpPorts, udpPorts, err := firewallPortSets(config.Network.Ports)
			if err != nil {
				return nil, err
			}
			if tcpPorts == "" {
				tcpPorts = "-"
			}
			if udpPorts == "" {
				udpPorts = "-"
			}
			args = append(args, "--ingress-guard", internalOffGuardNFTPath, tcpPorts, udpPorts)
		}
		args = append(args, "--gate-path", internalProxyTUNGatePath, "--", application[0])
		args = append(args, application[1:]...)
	}
	if !proxyNetwork || network.SharedNamespace {
		args = append(args, application...)
	}

	command := exec.Command(bubblewrap, args...)
	if resolverConfigured {
		resolver := network.Resolver
		if network.SharedNamespace {
			resolver = []byte("nameserver 127.0.0.1\noptions timeout:2 attempts:2\n")
		}
		resolverFile, err := anonymousDataFile(resolver)
		if err != nil {
			return nil, fmt.Errorf("prepare sandbox DNS configuration: %w", err)
		}
		command.ExtraFiles = append(command.ExtraFiles, resolverFile)
	}
	passwdFile, err := anonymousDataFile(sandboxPasswdContents(identity.user, identity.groups[0].ID))
	if err != nil {
		closeFiles(command.ExtraFiles)
		return nil, fmt.Errorf("prepare sandbox passwd database: %w", err)
	}
	command.ExtraFiles = append(command.ExtraFiles, passwdFile)
	groupFile, err := anonymousDataFile(sandboxGroupContents(identity.user, identity.groups))
	if err != nil {
		closeFiles(command.ExtraFiles)
		return nil, fmt.Errorf("prepare sandbox group database: %w", err)
	}
	command.ExtraFiles = append(command.ExtraFiles, groupFile)
	hostsFile, err := anonymousDataFile(sandboxHostsContents(appID, config.NetworkMode == profile.NetworkModeEgress, network.GatewayAddress))
	if err != nil {
		closeFiles(command.ExtraFiles)
		return nil, fmt.Errorf("prepare sandbox hosts database: %w", err)
	}
	command.ExtraFiles = append(command.ExtraFiles, hostsFile)
	return command, nil
}

func sandboxHostsContents(appID string, includeHostGateway bool, gatewayAddresses ...string) []byte {
	contents := fmt.Sprintf("127.0.0.1 localhost %s\n", sandboxHostname(appID))
	if includeHostGateway {
		gateway := "10.123.123.3"
		if len(gatewayAddresses) != 0 && gatewayAddresses[0] != "" {
			gateway = gatewayAddresses[0]
		}
		contents += gateway + " host.gateway.internal\n"
	}
	return []byte(contents)
}

func closeFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

type sandboxAccountIdentity struct {
	user   profile.Account
	groups []profile.Account
}

func sandboxIdentity(config profile.Profile, hostUID, hostGID int) sandboxAccountIdentity {
	user := config.User
	if user.Name == "" {
		user = profile.Account{ID: uint32(hostUID), Name: "isobox"}
	}
	groups := append([]profile.Account(nil), config.Groups...)
	if len(groups) == 0 {
		groups = []profile.Account{{ID: uint32(hostGID), Name: "isobox"}}
	}
	return sandboxAccountIdentity{user: user, groups: groups}
}

func sandboxPasswdContents(user profile.Account, gid uint32) []byte {
	entries := []string{
		fmt.Sprintf("%s:x:%d:%d:%s:%s:/bin/sh", user.Name, user.ID, gid, user.Name, sandboxHome),
	}
	if user.ID != 0 && user.Name != "root" {
		entries = append(entries, "root:x:0:0:root:/root:/bin/sh")
	}
	if user.ID != 65534 && user.Name != "nobody" {
		entries = append(entries, "nobody:x:65534:65534:nobody:/nonexistent:/bin/false")
	}
	return []byte(strings.Join(entries, "\n") + "\n")
}

func sandboxGroupContents(user profile.Account, groups []profile.Account) []byte {
	entries := make([]string, 0, len(groups)+2)
	for index, group := range groups {
		members := ""
		if index > 0 {
			members = user.Name
		}
		entries = append(entries, fmt.Sprintf("%s:x:%d:%s", group.Name, group.ID, members))
	}
	rootID, noGroupID := uint32(0), uint32(65534)
	hasRoot, hasNoGroup := false, false
	for _, group := range groups {
		hasRoot = hasRoot || group.ID == rootID || group.Name == "root"
		hasNoGroup = hasNoGroup || group.ID == noGroupID || group.Name == "nogroup"
	}
	if !hasRoot {
		entries = append(entries, "root:x:0:")
	}
	if !hasNoGroup {
		entries = append(entries, "nogroup:x:65534:")
	}
	return []byte(strings.Join(entries, "\n") + "\n")
}

func sandboxHostname(appID string) string {
	const hostnamePrefix = "isobox-"
	if appID == "" {
		return "isobox"
	}
	maxAppIDLength := 64 - len(hostnamePrefix)
	if len(appID) > maxAppIDLength {
		appID = appID[:maxAppIDLength]
	}
	return hostnamePrefix + appID
}

func validateSupportedProfile(config profile.EffectiveProfile, network NetworkRuntime) error {
	if network.Mode != config.NetworkMode {
		return fmt.Errorf("selected network mode %q does not match profile mode %q", network.Mode, config.NetworkMode)
	}
	if len(config.Network.Ports) != 0 && config.NetworkMode != profile.NetworkModeOff && config.NetworkMode != profile.NetworkModeDirect && config.NetworkMode != profile.NetworkModeProxy && config.NetworkMode != profile.NetworkModeEgress {
		return fmt.Errorf("profile.network.port is not supported with network mode %q; refusing to ignore published ports", config.NetworkMode)
	}
	if config.NetworkMode == profile.NetworkModeDirect {
		if network.activeBackend() == "none" || network.Executable == "" {
			return fmt.Errorf("direct network mode requires a selected network backend")
		}
		if len(network.Resolver) == 0 {
			return fmt.Errorf("direct network mode requires an explicit sandbox DNS configuration")
		}
		return nil
	}
	if config.NetworkMode == profile.NetworkModeProxy || config.NetworkMode == profile.NetworkModeEgress {
		if network.activeBackend() != "proxy-tun" {
			return fmt.Errorf("network mode %q requires the selected policy TUN backend", config.NetworkMode)
		}
		if string(network.Resolver) != "nameserver "+network.ResolverEndpoint+"\n" {
			return fmt.Errorf("network mode %q requires the internal DNS endpoint routed through the TUN", config.NetworkMode)
		}
		if config.NetworkMode == profile.NetworkModeProxy {
			if network.ProxyAlias == "" {
				return fmt.Errorf("proxy network mode requires a stored SOCKS5 alias")
			}
			if len(networkDNSAddresses(network)) == 0 {
				return fmt.Errorf("proxy network mode requires an IPv4 DNS destination for the SOCKS data plane")
			}
		} else {
			if network.ProxyAlias != "" {
				return fmt.Errorf("egress mode cannot use a remote SOCKS5 alias")
			}
			for _, address := range networkDNSAddresses(network) {
				dnsAddress := net.ParseIP(address)
				if dnsAddress == nil || dnsAddress.To4() == nil || strings.Contains(address, ":") {
					return fmt.Errorf("egress upstream DNS must be IPv4")
				}
			}
		}
		if len(config.Network.Ports) != 0 && (network.PortForwarder != "pasta" || network.ForwarderExecutable == "" || !network.EnforceEgressFilter) {
			return fmt.Errorf("network mode %q with published ports requires explicit pasta ingress forwarding and an enforced egress filter", config.NetworkMode)
		}
		return nil
	}
	if config.NetworkMode != profile.NetworkModeOff {
		return fmt.Errorf("profile.network.mode=%q is not supported by the sandbox runtime yet; refusing to launch with different network semantics until F06 is implemented", config.NetworkMode)
	}
	if len(config.Network.Ports) != 0 && network.activeBackend() != "pasta" {
		return fmt.Errorf("network mode off with published ports requires the pasta forwarding backend")
	}
	return nil
}

func networkDNSAddresses(network NetworkRuntime) []string {
	if len(network.ProxyDNSAddresses) != 0 {
		return network.ProxyDNSAddresses
	}
	if network.ProxyDNSAddress != "" {
		return []string{network.ProxyDNSAddress}
	}
	return nil
}

func anonymousDataFile(data []byte) (*os.File, error) {
	file, err := os.CreateTemp("", "isobox-data-*")
	if err != nil {
		return nil, err
	}
	name := file.Name()
	if err := os.Remove(name); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func portForwardSpecs(rules []profile.PortRule) (string, string, error) {
	var tcp, udp []string
	for index, rule := range rules {
		ip := net.ParseIP(rule.HostAddress)
		if ip == nil || ip.To4() == nil || strings.Contains(rule.HostAddress, ":") {
			return "", "", fmt.Errorf("profile.network.port[%d].host_address must be IPv4; IPv6 is disabled", index)
		}
		ip = ip.To4()
		portRange := strconv.Itoa(int(rule.Port))
		if rule.EndPort != 0 {
			portRange += "-" + strconv.Itoa(int(rule.EndPort))
		}
		forward := ip.String() + "/" + portRange
		switch rule.Protocol {
		case profile.PortProtocolBoth:
			tcp = append(tcp, forward)
			udp = append(udp, forward)
		case profile.PortProtocolTCP:
			tcp = append(tcp, forward)
		case profile.PortProtocolUDP:
			udp = append(udp, forward)
		default:
			return "", "", fmt.Errorf("profile.network.port[%d].protocol %q is invalid", index, rule.Protocol)
		}
	}
	return strings.Join(tcp, ","), strings.Join(udp, ","), nil
}

// firewallPortSets returns only the numeric destination ports accepted by the
// in-namespace nftables guard. pasta forwarding specs also include the host
// bind address and cannot be parsed as nft port sets.
func firewallPortSets(rules []profile.PortRule) (string, string, error) {
	var tcp, udp []string
	for index, rule := range rules {
		if rule.Port == 0 || rule.EndPort != 0 && rule.EndPort < rule.Port {
			return "", "", fmt.Errorf("profile.network.port[%d] has invalid firewall port range", index)
		}
		portRange := strconv.Itoa(int(rule.Port))
		if rule.EndPort != 0 {
			portRange += "-" + strconv.Itoa(int(rule.EndPort))
		}
		switch rule.Protocol {
		case profile.PortProtocolBoth:
			tcp = append(tcp, portRange)
			udp = append(udp, portRange)
		case profile.PortProtocolTCP:
			tcp = append(tcp, portRange)
		case profile.PortProtocolUDP:
			udp = append(udp, portRange)
		default:
			return "", "", fmt.Errorf("profile.network.port[%d].protocol %q is invalid", index, rule.Protocol)
		}
	}
	return strings.Join(tcp, ","), strings.Join(udp, ","), nil
}

func mountTargetsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right || left == string(filepath.Separator) || right == string(filepath.Separator) {
		return true
	}
	return strings.HasPrefix(left, right+string(filepath.Separator)) || strings.HasPrefix(right, left+string(filepath.Separator))
}

func availableSystemMounts() ([]systemMount, error) {
	candidates := []systemMount{
		{source: "/usr", destination: "/usr", directory: true, required: true},
		{source: "/bin", destination: "/bin", directory: true},
		{source: "/sbin", destination: "/sbin", directory: true},
		{source: "/lib", destination: "/lib", directory: true},
		{source: "/lib64", destination: "/lib64", directory: true},
		{source: "/etc/alternatives", destination: "/etc/alternatives", directory: true},
		{source: "/etc/ld.so.conf.d", destination: "/etc/ld.so.conf.d", directory: true},
		{source: "/etc/ssl/certs", destination: "/etc/ssl/certs", directory: true},
		{source: "/etc/pki/tls/certs", destination: "/etc/pki/tls/certs", directory: true},
		{source: "/etc/nsswitch.conf", destination: "/etc/nsswitch.conf"},
		{source: "/etc/ld.so.cache", destination: "/etc/ld.so.cache"},
		{source: "/etc/ld.so.conf", destination: "/etc/ld.so.conf"},
	}

	mounts := make([]systemMount, 0, len(candidates))
	for _, candidate := range candidates {
		info, err := os.Stat(candidate.source)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && !candidate.required {
				continue
			}
			if errors.Is(err, fs.ErrNotExist) && candidate.required {
				return nil, fmt.Errorf("required system path %q is unavailable", candidate.source)
			}
			return nil, fmt.Errorf("inspect system path %q: %w", candidate.source, err)
		}
		if info.IsDir() != candidate.directory {
			kind := "regular file"
			if candidate.directory {
				kind = "directory"
			}
			return nil, fmt.Errorf("system path %q must be a %s", candidate.source, kind)
		}
		mounts = append(mounts, candidate)
	}
	return mounts, nil
}
