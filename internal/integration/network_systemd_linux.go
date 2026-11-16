//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// NetworkManagerUnitOptions describes the persistent user service for a
// rootless bridge network.
type NetworkManagerUnitOptions struct {
	Name       string
	ConfigPath string
}

var networkUnitNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// InstallAndStartNetworkManager installs, enables, and starts an isobox-owned
// manager unit. The unit uses Type=notify, so success means the bridge and its
// control socket are ready.
func InstallAndStartNetworkManager(options NetworkManagerUnitOptions) (string, error) {
	unitName, err := networkManagerUnitName(options.Name)
	if err != nil {
		return "", err
	}
	configPath, err := resolveOwnedNetworkConfig(options.ConfigPath)
	if err != nil {
		return "", err
	}
	isoboxPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate isobox executable: %w", err)
	}
	isoboxPath, err = resolveExecutable(isoboxPath)
	if err != nil {
		return "", fmt.Errorf("resolve isobox executable: %w", err)
	}
	executableArgument, err := systemdExecArgument(isoboxPath)
	if err != nil {
		return "", fmt.Errorf("encode isobox path for systemd: %w", err)
	}
	configArgument, err := systemdExecArgument("--config=" + configPath)
	if err != nil {
		return "", fmt.Errorf("encode network configuration path for systemd: %w", err)
	}
	content := []byte(fmt.Sprintf(
		"%s\n[Unit]\nDescription=Isobox network %s\n\n[Service]\nType=notify\nNotifyAccess=all\nExecStart=%s network __manager %s\nRestart=on-failure\nRestartSec=2s\nTimeoutStartSec=30s\nTimeoutStopSec=15s\nKillMode=control-group\n\n[Install]\nWantedBy=default.target\n",
		managedSystemdUnitMarker, options.Name, executableArgument, configArgument,
	))

	directory, err := systemdUserUnitDirectory()
	if err != nil {
		return "", err
	}
	if err := ensureSystemdUserUnitDirectory(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, unitName)
	if _, existed, err := readManagedSystemdUnit(path, true); err != nil {
		return "", err
	} else if existed {
		return "", fmt.Errorf("user service %q already exists", unitName)
	}
	systemctl, err := userSystemctlPath()
	if err != nil {
		return "", err
	}
	if err := requireUserSystemdManager(systemctl); err != nil {
		return "", err
	}
	if err := writeSystemdUnitAtomically(directory, unitName, path, content); err != nil {
		return "", fmt.Errorf("write user service unit %q: %w", path, err)
	}
	cleanup := func() {
		_ = runUserSystemctl(systemctl, "disable", "--now", unitName)
		if _, _, verifyErr := readManagedSystemdUnit(path, false); verifyErr == nil {
			_ = os.Remove(path)
		}
		_ = runUserSystemctl(systemctl, "daemon-reload")
	}
	if err := runUserSystemctl(systemctl, "daemon-reload"); err != nil {
		cleanup()
		return "", fmt.Errorf("reload user systemd manager: %w", err)
	}
	if err := runUserSystemctl(systemctl, "enable", unitName); err != nil {
		cleanup()
		return "", fmt.Errorf("enable network manager service %q: %w", unitName, err)
	}
	if err := runUserSystemctl(systemctl, "start", unitName); err != nil {
		cleanup()
		return "", fmt.Errorf("start network manager service %q: %w", unitName, err)
	}
	return path, nil
}

// CheckUserSystemdManager fails unless the current user's systemd manager is
// reachable. Call it before writing persistent network state.
func CheckUserSystemdManager() error {
	systemctl, err := userSystemctlPath()
	if err != nil {
		return err
	}
	return requireUserSystemdManager(systemctl)
}

// NetworkManagerUnitState returns systemd's ActiveState for an installed
// isobox-managed network manager unit.
func NetworkManagerUnitState(name string) (string, error) {
	unitName, err := networkManagerUnitName(name)
	if err != nil {
		return "", err
	}
	directory, err := systemdUserUnitDirectory()
	if err != nil {
		return "", err
	}
	if err := verifySystemdUserUnitDirectory(directory); err != nil {
		return "", err
	}
	if _, _, err := readManagedSystemdUnit(filepath.Join(directory, unitName), false); err != nil {
		return "", err
	}
	systemctl, err := userSystemctlPath()
	if err != nil {
		return "", err
	}
	if err := requireUserSystemdManager(systemctl); err != nil {
		return "", err
	}
	command := exec.Command(systemctl, "--user", "show", "--property=ActiveState", "--value", unitName)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("inspect network manager service %q: %w", unitName, err)
	}
	state := strings.TrimSpace(string(output))
	if state == "" {
		return "", fmt.Errorf("systemd returned an empty state for network manager service %q", unitName)
	}
	return state, nil
}

// RemoveNetworkManagerUnit stops and disables an isobox-managed network unit,
// then removes its file and reloads the user manager.
func RemoveNetworkManagerUnit(name string) (string, error) {
	unitName, err := networkManagerUnitName(name)
	if err != nil {
		return "", err
	}
	directory, err := systemdUserUnitDirectory()
	if err != nil {
		return "", err
	}
	if err := verifySystemdUserUnitDirectory(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, unitName)
	content, _, err := readManagedSystemdUnit(path, false)
	if err != nil {
		return "", err
	}
	systemctl, err := userSystemctlPath()
	if err != nil {
		return "", err
	}
	if err := requireUserSystemdManager(systemctl); err != nil {
		return "", err
	}
	if err := runUserSystemctl(systemctl, "disable", "--now", unitName); err != nil {
		return "", fmt.Errorf("stop and disable network manager service %q: %w", unitName, err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("remove network manager service %q: %w", path, err)
	}
	if err := runUserSystemctl(systemctl, "daemon-reload"); err != nil {
		if restoreErr := writeSystemdUnitAtomically(directory, unitName, path, content); restoreErr != nil {
			return "", fmt.Errorf("reload user systemd manager: %w; restore unit: %w", err, restoreErr)
		}
		_ = runUserSystemctl(systemctl, "daemon-reload")
		return "", fmt.Errorf("reload user systemd manager after removing network service: %w", err)
	}
	return path, nil
}

func networkManagerUnitName(name string) (string, error) {
	if !networkUnitNamePattern.MatchString(name) {
		return "", fmt.Errorf("network name must start with an ASCII letter or digit and contain only letters, digits, '_' or '-' (maximum 63 characters)")
	}
	return "isobox-network-" + name + ".service", nil
}

func requireUserSystemdManager(systemctl string) error {
	if err := runUserSystemctl(systemctl, "show-environment"); err != nil {
		return fmt.Errorf("user systemd manager is unavailable: %w", err)
	}
	return nil
}

func resolveOwnedNetworkConfig(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("network configuration path must be absolute")
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect network configuration %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("network configuration %q must be an owned regular file with mode 0600", path)
	}
	return path, nil
}
