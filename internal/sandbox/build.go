// Package sandbox builds and supervises the bubblewrap process.
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/profile"
	"github.com/arwos/isobox/internal/proxy"
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
	if config.NetworkMode == profile.NetworkModeDirect || config.NetworkMode == profile.NetworkModeProxy {
		for _, mount := range profileMounts {
			if mountTargetsOverlap(mount.target, "/etc/resolv.conf") {
				return nil, fmt.Errorf("profile mount target %q conflicts with the network DNS configuration", mount.target)
			}
		}
	}
	sortProfileMounts(profileMounts)

	offGuard := config.NetworkMode == profile.NetworkModeOff && len(config.Network.Ports) != 0
	directNetwork := config.NetworkMode == profile.NetworkModeDirect
	proxyNetwork := config.NetworkMode == profile.NetworkModeProxy
	proxyIngress := proxyNetwork && len(config.Network.Ports) != 0
	ipv4Setup := directNetwork || config.NetworkMode == profile.NetworkModeOff && len(config.Network.Ports) == 0
	activeBackend := network.activeBackend()
	args := []string{"--unshare-user"}
	// Proxy mode creates its network namespace in the trusted helper, after
	// bwrap has finished its user-namespace setup for --dev/devpts. That keeps
	// CAP_NET_ADMIN in the user namespace that owns the network namespace.
	// The helper installs namespace-blocking seccomp before app exec.
	// The off-mode guard creates its netns after bwrap mounts too, because /dev
	// setup can change the user namespace that should own the network namespace.
	if !offGuard && !proxyNetwork {
		args = append(args, "--disable-userns", "--assert-userns-disabled")
	}
	args = append(args,
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--new-session",
		"--die-with-parent",
	)
	if !offGuard && !ipv4Setup && !proxyNetwork {
		args = append(args, "--unshare-net")
	}
	for _, mount := range systemMounts {
		args = append(args, "--ro-bind", mount.source, mount.destination)
	}
	if directNetwork || proxyNetwork {
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
	args = append(args,
		"--clearenv",
		"--setenv", "PATH", sandboxPath,
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
	if ipv4Setup || offGuard {
		// Keep the trusted setup helper's Go runtime within the task budget. The
		// value is removed by the helper before it execs the application.
		args = append(args, "--setenv", "GOMAXPROCS", "1")
	}
	if proxyNetwork {
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
	args = append(args,
		"--unsetenv", "PWD",
		"--chdir", "/",
		"--setenv", "PWD", "/",
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
	} else if proxyNetwork {
		args = append(args, internalSandboxBinaryPath, internalProxyTUNCommand)
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
	if !proxyNetwork {
		args = append(args, application...)
	}

	command := exec.Command(bubblewrap, args...)
	if directNetwork || proxyNetwork {
		resolverFile, err := anonymousDataFile(network.Resolver)
		if err != nil {
			return nil, fmt.Errorf("prepare sandbox DNS configuration: %w", err)
		}
		command.ExtraFiles = append(command.ExtraFiles, resolverFile)
	}
	return command, nil
}

func validateSupportedProfile(config profile.EffectiveProfile, network NetworkRuntime) error {
	if network.Mode != config.NetworkMode {
		return fmt.Errorf("selected network mode %q does not match profile mode %q", network.Mode, config.NetworkMode)
	}
	if len(config.Network.Ports) != 0 && config.NetworkMode != profile.NetworkModeOff && config.NetworkMode != profile.NetworkModeDirect && config.NetworkMode != profile.NetworkModeProxy {
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
	if config.NetworkMode == profile.NetworkModeProxy {
		if network.activeBackend() != "proxy-tun" || network.ProxyAlias == "" {
			return fmt.Errorf("proxy network mode requires the selected proxy TUN backend and a stored proxy alias")
		}
		if string(network.Resolver) != "nameserver "+proxy.TransparentDNSAddress+"\n" {
			return fmt.Errorf("proxy network mode requires the internal DNS endpoint routed through the TUN")
		}
		dnsAddress := net.ParseIP(network.ProxyDNSAddress)
		if dnsAddress == nil || dnsAddress.To4() == nil || strings.Contains(network.ProxyDNSAddress, ":") {
			return fmt.Errorf("proxy network mode requires an IPv4 DNS destination for the SOCKS data plane")
		}
		if len(config.Network.Ports) != 0 && (network.PortForwarder != "pasta" || network.ForwarderExecutable == "" || !network.EnforceEgressFilter) {
			return fmt.Errorf("proxy mode with published ports requires explicit pasta ingress forwarding and an enforced egress filter")
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
		if rule.Port == 0 || rule.Port > 65535 || rule.EndPort != 0 && (rule.EndPort < rule.Port || rule.EndPort > 65535) {
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
		{source: "/etc/passwd", destination: "/etc/passwd"},
		{source: "/etc/group", destination: "/etc/group"},
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
