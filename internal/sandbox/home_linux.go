//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func resolveHome(profileHome, _ string, appID string) (string, error) {
	if profileHome != "" {
		if !filepath.IsAbs(profileHome) || filepath.Clean(profileHome) != profileHome {
			return "", fmt.Errorf("profile.home_path must be a clean absolute path")
		}
		home, err := filepath.EvalSymlinks(profileHome)
		if err != nil {
			return "", fmt.Errorf("profile.home_path %q is unavailable: %w", profileHome, err)
		}
		info, err := os.Stat(home)
		if err != nil {
			return "", fmt.Errorf("inspect profile.home_path %q: %w", home, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("profile.home_path %q must be a directory", profileHome)
		}
		if home == string(filepath.Separator) {
			return "", fmt.Errorf("profile.home_path resolves to the host root; choose a narrower directory")
		}
		return home, nil
	}
	if appID == "" {
		return "", fmt.Errorf("profile.home_path is empty; pass --app-id to create a persistent isolated HOME")
	}

	if !validManagedHomeAppID(appID) {
		return "", fmt.Errorf("--app-id must contain only letters, digits, '.', '_' or '-' and start with a letter or digit")
	}
	userHome, err := os.UserHomeDir()
	if err != nil || userHome == "" {
		return "", fmt.Errorf("cannot determine the user's home directory for persistent HOME storage")
	}
	privateRoot := filepath.Join(userHome, ".isobox")
	homeRoot := filepath.Join(privateRoot, "home")
	for _, directory := range []string{privateRoot, homeRoot} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return "", fmt.Errorf("prepare private HOME directory %q: %w", directory, err)
		}
	}
	home := filepath.Join(homeRoot, appID)
	if err := ensurePrivateDirectory(home); err != nil {
		return "", fmt.Errorf("prepare persistent HOME %q: %w", home, err)
	}
	return home, nil
}

func validManagedHomeAppID(appID string) bool {
	if len(appID) == 0 || len(appID) > 64 {
		return false
	}
	for index, character := range appID {
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if index > 0 {
			valid = valid || character == '.' || character == '_' || character == '-'
		}
		if !valid {
			return false
		}
	}
	return true
}

func ensurePrivateDirectory(path string) error {
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
		// #nosec G302 -- this private directory needs owner execute permission.
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
