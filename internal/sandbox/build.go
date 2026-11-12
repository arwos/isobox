// Package sandbox builds and supervises the bubblewrap process.
package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"

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

// BuildCommand creates a bwrap command for the supported portion of the
// current sandbox profile. Host paths are limited to fixed runtime paths and
// the mounts explicitly granted by the profile.
func BuildCommand(bubblewrap string, config profile.EffectiveProfile, profilePath, appID string, application []string) (*exec.Cmd, error) {
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
	homeSource, err := resolveHome(config.HomePath, profilePath, appID)
	if err != nil {
		return nil, err
	}
	profileMounts = append(profileMounts, profileMount{source: homeSource, target: sandboxHome, writable: true})
	if err := validateProfileMountTargets(profileMounts); err != nil {
		return nil, err
	}
	sortProfileMounts(profileMounts)

	args := []string{
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-net",
		"--new-session",
		"--die-with-parent",
	}
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
	for _, mount := range profileMounts {
		option := "--bind"
		if !mount.writable {
			option = "--ro-bind"
		}
		args = append(args, option, mount.source, mount.target)
	}
	args = append(args,
		"--clearenv",
		"--setenv", "PATH", sandboxPath,
		"--setenv", "HOME", sandboxHome,
		"--setenv", "TMPDIR", "/tmp",
		"--unsetenv", "PWD",
		"--chdir", "/",
		"--setenv", "PWD", "/",
		"--json-status-fd", "3",
		"--",
	)
	args = append(args, application...)

	return exec.Command(bubblewrap, args...), nil
}

func validateSupportedProfile(config profile.EffectiveProfile) error {
	if config.GUI.Wayland || config.GUI.X11 || config.GUI.Audio || config.GUI.GPU || config.GUI.Desktop {
		return fmt.Errorf("profile.gui requests access not supported by this runtime yet; explicit GUI grants are added in F04-T03")
	}
	if config.NetworkMode != profile.NetworkModeOff {
		return fmt.Errorf("profile.network.mode=%q is not supported by the sandbox runtime yet; refusing to launch with different network semantics until F05/F06 is implemented", config.NetworkMode)
	}
	if len(config.Network.Ports) != 0 {
		return fmt.Errorf("profile.network.port is not supported by this runtime yet; refusing to ignore published ports")
	}
	return nil
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
