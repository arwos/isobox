//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const maxSystemdUnitFileSize = 1 << 20

// InstallSystemdUnit writes an isobox-managed unit to the user's systemd
// directory and reloads the user manager. It does not enable or start it.
func InstallSystemdUnit(options SystemdUnitOptions) (string, error) {
	unitName, err := systemdUserUnitName(options.AppID)
	if err != nil {
		return "", err
	}
	_, content, err := prepareSystemdUnit(options)
	if err != nil {
		return "", err
	}
	directory, err := systemdUserUnitDirectory()
	if err != nil {
		return "", err
	}
	if err := ensureSystemdUserUnitDirectory(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, unitName)
	previous, existed, err := readManagedSystemdUnit(path, true)
	if err != nil {
		return "", err
	}
	if err := writeSystemdUnitAtomically(directory, unitName, path, content); err != nil {
		return "", fmt.Errorf("write user service unit %q: %w", path, err)
	}
	systemctl, err := userSystemctlPath()
	if err == nil {
		err = runUserSystemctl(systemctl, "daemon-reload")
	}
	if err != nil {
		if existed {
			_ = writeSystemdUnitAtomically(directory, unitName, path, previous)
		} else {
			_ = os.Remove(path)
		}
		if systemctl != "" {
			_ = runUserSystemctl(systemctl, "daemon-reload")
		}
		return "", fmt.Errorf("reload user systemd manager: %w", err)
	}
	return path, nil
}

// RemoveSystemdUnit stops/disables and removes only an isobox-managed user
// service unit.
func RemoveSystemdUnit(appID string) (string, error) {
	unitName, err := systemdUserUnitName(appID)
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
	if err := runUserSystemctl(systemctl, "disable", "--now", unitName); err != nil {
		return "", fmt.Errorf("stop and disable user service %q: %w", unitName, err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("remove user service unit %q: %w", path, err)
	}
	if err := runUserSystemctl(systemctl, "daemon-reload"); err != nil {
		if restoreErr := writeSystemdUnitAtomically(directory, unitName, path, content); restoreErr != nil {
			return "", fmt.Errorf("reload user systemd manager: %v; restore unit: %w", err, restoreErr)
		}
		_ = runUserSystemctl(systemctl, "daemon-reload")
		return "", fmt.Errorf("reload user systemd manager after removing unit: %w", err)
	}
	return path, nil
}

// EnableSystemdUnit enables an installed isobox-managed service for the user's
// default.target. It does not start the service now.
func EnableSystemdUnit(appID string) (string, error) {
	return setSystemdUnitEnabled(appID, true)
}

// DisableSystemdUnit removes the user's enablement for an installed isobox-
// managed service. It does not stop a service that is already running.
func DisableSystemdUnit(appID string) (string, error) {
	return setSystemdUnitEnabled(appID, false)
}

func setSystemdUnitEnabled(appID string, enable bool) (string, error) {
	unitName, err := systemdUserUnitName(appID)
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
	if _, _, err := readManagedSystemdUnit(path, false); err != nil {
		return "", err
	}
	systemctl, err := userSystemctlPath()
	if err != nil {
		return "", err
	}
	action := "enable"
	if !enable {
		action = "disable"
	}
	if err := runUserSystemctl(systemctl, action, unitName); err != nil {
		return "", fmt.Errorf("%s user service %q: %w", action, unitName, err)
	}
	return path, nil
}

func ensureSystemdUserUnitDirectory(directory string) error {
	configHome, err := userConfigHome()
	if err != nil {
		return err
	}
	for _, path := range []string{configHome, filepath.Join(configHome, "systemd"), directory} {
		if err := ensureOwnedDirectory(path); err != nil {
			return fmt.Errorf("prepare systemd user unit directory %q: %w", path, err)
		}
	}
	return nil
}

func verifySystemdUserUnitDirectory(directory string) error {
	configHome, err := userConfigHome()
	if err != nil {
		return err
	}
	for _, path := range []string{configHome, filepath.Join(configHome, "systemd"), directory} {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect systemd user unit directory %q: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("systemd user unit directory %q must be a private directory owned by the current user", path)
		}
	}
	return nil
}

func readManagedSystemdUnit(path string, allowMissing bool) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && allowMissing {
		return nil, false, nil
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, fmt.Errorf("user service unit %q is not installed", path)
		}
		return nil, false, fmt.Errorf("inspect user service unit %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return nil, false, fmt.Errorf("refusing to replace or remove %q: it is not an owned regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("read user service unit %q: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSystemdUnitFileSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return nil, false, fmt.Errorf("read user service unit %q: %w", path, readErr)
	}
	if closeErr != nil {
		return nil, false, fmt.Errorf("close user service unit %q: %w", path, closeErr)
	}
	if len(data) > maxSystemdUnitFileSize {
		return nil, false, fmt.Errorf("refusing to replace or remove %q: unit file is too large to verify", path)
	}
	if !bytes.HasPrefix(data, []byte(managedSystemdUnitMarker+"\n")) {
		return nil, false, fmt.Errorf("refusing to replace or remove %q: file is not marked as managed by isobox", path)
	}
	return data, true, nil
}

func writeSystemdUnitAtomically(directory, name, path string, content []byte) error {
	temporary, err := os.CreateTemp(directory, "."+strings.TrimSuffix(name, ".service")+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func userSystemctlPath() (string, error) {
	path, err := exec.LookPath("systemctl")
	if err != nil {
		return "", fmt.Errorf("systemctl is required for user service integration: %w", err)
	}
	return filepath.Abs(path)
}

func runUserSystemctl(systemctl string, args ...string) error {
	commandArgs := append([]string{"--user"}, args...)
	command := exec.Command(systemctl, commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
		return err
	}
	return nil
}
