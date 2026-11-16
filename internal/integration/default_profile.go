/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const defaultProfileFileName = "default.unic"

const defaultProfileContents = "version 1;\nnetwork { mode off; }\n"

// EnsureDefaultProfile returns the user's default profile path, creating a
// safe starter profile on the first run when it does not exist.
func EnsureDefaultProfile() (string, error) {
	return EnsureProfileForApp("")
}

// EnsureProfileForApp returns an app-specific profile when it exists next to
// default.unic. Otherwise it returns the default profile, creating a safe
// starter profile on the first run when needed.
func EnsureProfileForApp(appID string) (string, error) {
	if appID != "" && !validProfileAppID(appID) {
		return "", fmt.Errorf("invalid app-id %q for profile lookup", appID)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate isobox home directory: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("cannot determine the user's home directory for isobox profiles")
	}
	root := filepath.Join(home, ".isobox")
	directory := filepath.Join(root, "config")
	if err := ensurePrivateProfileDirectory(root); err != nil {
		return "", fmt.Errorf("prepare isobox directory %q: %w", root, err)
	}
	if err := ensurePrivateProfileDirectory(directory); err != nil {
		return "", fmt.Errorf("prepare isobox config directory %q: %w", directory, err)
	}
	if appID != "" {
		appProfile := filepath.Join(directory, appID+".unic")
		if _, err := os.Stat(appProfile); err == nil {
			return appProfile, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect app profile %q: %w", appProfile, err)
		}
	}
	path := filepath.Join(directory, defaultProfileFileName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("create default profile %q: %w", path, err)
	}
	if _, err := file.WriteString(defaultProfileContents); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write default profile %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close default profile %q: %w", path, err)
	}
	return path, nil
}

func validProfileAppID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if index > 0 {
			valid = valid || char == '.' || char == '_' || char == '-'
		}
		if !valid {
			return false
		}
	}
	return true
}

func ensurePrivateProfileDirectory(path string) error {
	err := os.Mkdir(path, 0o700)
	if err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path must be a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("directory is not owned by the current user")
	}
	if info.Mode().Perm() != 0o700 {
		// #nosec G302 -- directories need execute permission for owner-only traversal.
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("set directory mode to 0700: %w", err)
		}
		info, err = os.Lstat(path)
		if err != nil {
			return err
		}
		stat, ok = info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("directory owner or mode changed while securing it")
		}
	}
	return nil
}

// userConfigHome is kept for integrations that use standard XDG locations,
// such as user systemd units. Isobox profiles use ~/.isobox/config directly.
func userConfigHome() (string, error) {
	if directory := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(directory) {
		return filepath.Clean(directory), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine the user's configuration directory: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("cannot determine the user's configuration directory")
	}
	return filepath.Join(home, ".config"), nil
}
