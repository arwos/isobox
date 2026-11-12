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

// BuildCommand creates a bwrap command for the supported portion of the
// current sandbox profile. Host paths are limited to fixed runtime paths and
// the mounts explicitly granted by the profile.
func BuildCommand(bubblewrap, nftPath string, config profile.EffectiveProfile, profilePath, appID string, application []string) (*exec.Cmd, error) {
	if bubblewrap == "" {
		return nil, fmt.Errorf("bwrap executable path is required")
	}
	if len(application) == 0 || application[0] == "" {
		return nil, fmt.Errorf("application command is required")
	}
	if err := validateSupportedProfile(config); err != nil {
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
	sortProfileMounts(profileMounts)

	args := []string{"--unshare-user"}
	if len(config.Network.Ports) == 0 {
		args = append(args, "--disable-userns", "--assert-userns-disabled")
	}
	args = append(args,
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-net",
		"--new-session",
		"--die-with-parent",
	)
	for _, mount := range systemMounts {
		args = append(args, "--ro-bind", mount.source, mount.destination)
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
	if len(config.Network.Ports) != 0 {
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
			"--ro-bind", binary, "/tmp/.isobox-internal",
			"--cap-add", "CAP_NET_ADMIN",
			"--block-fd", "3",
		)
	}
	args = append(args,
		"--unsetenv", "PWD",
		"--chdir", "/",
		"--setenv", "PWD", "/",
		"--json-status-fd", "3",
		"--",
	)
	if len(config.Network.Ports) != 0 {
		tcpPorts, udpPorts, err := portForwardSpecs(config.Network.Ports)
		if err != nil {
			return nil, err
		}
		if tcpPorts == "" {
			tcpPorts = "-"
		}
		if udpPorts == "" {
			udpPorts = "-"
		}
		args = append(args, "/tmp/.isobox-internal", internalOffGuardCommand, nftPath, tcpPorts, udpPorts, "--")
	}
	args = append(args, application...)

	return exec.Command(bubblewrap, args...), nil
}

func validateSupportedProfile(config profile.EffectiveProfile) error {
	if len(config.Network.Ports) != 0 && config.NetworkMode != profile.NetworkModeOff {
		return fmt.Errorf("profile.network.port is currently supported only with network mode off; refusing to ignore published ports")
	}
	if config.NetworkMode != profile.NetworkModeOff {
		return fmt.Errorf("profile.network.mode=%q is not supported by the sandbox runtime yet; refusing to launch with different network semantics until F05/F06 is implemented", config.NetworkMode)
	}
	return nil
}

func portForwardSpecs(rules []profile.PortRule) (string, string, error) {
	var tcp, udp []string
	for index, rule := range rules {
		ip := net.ParseIP(rule.HostAddress)
		if ip == nil {
			return "", "", fmt.Errorf("profile.network.port[%d].host_address is not an IP address", index)
		}
		portRange := strconv.Itoa(int(rule.Port))
		if rule.EndPort != 0 {
			portRange += "-" + strconv.Itoa(int(rule.EndPort))
		}
		forward := ip.String() + "/" + portRange
		if rule.Protocol == profile.PortProtocolBoth || rule.Protocol == profile.PortProtocolTCP {
			tcp = append(tcp, forward)
		}
		if rule.Protocol == profile.PortProtocolBoth || rule.Protocol == profile.PortProtocolUDP {
			udp = append(udp, forward)
		}
	}
	return strings.Join(tcp, ","), strings.Join(udp, ","), nil
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
