//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const managedLauncherMarker = "X-Isobox-Managed=true"

// InstallLauncher creates or updates an isobox-managed launcher in the user's
// XDG applications directory. Files not owned by isobox are never replaced.
func InstallLauncher(options LauncherOptions) (string, error) {
	content, err := prepareLauncher(options)
	if err != nil {
		return "", err
	}
	directory, err := launcherDirectory()
	if err != nil {
		return "", err
	}
	path, err := launcherPath(directory, options.ID)
	if err != nil {
		return "", err
	}
	if err := verifyManagedLauncher(path, true); err != nil {
		return "", err
	}
	if err := writeLauncherAtomically(directory, options.ID, path, content); err != nil {
		return "", fmt.Errorf("write launcher %q: %w", path, err)
	}
	return path, nil
}

// RemoveLauncher removes only an owned regular file previously created by
// isobox; profile and application data are left untouched.
func RemoveLauncher(id string) (string, error) {
	if !validLauncherID(id) {
		return "", fmt.Errorf("launcher id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}
	directory, err := launcherDirectory()
	if err != nil {
		return "", err
	}
	path, err := launcherPath(directory, id)
	if err != nil {
		return "", err
	}
	if err := verifyManagedLauncher(path, false); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("remove launcher %q: %w", path, err)
	}
	return path, nil
}

func launcherDirectory() (string, error) {
	dataHome, err := userDataHome()
	if err != nil {
		return "", err
	}
	if err := ensureOwnedDirectory(dataHome); err != nil {
		return "", fmt.Errorf("prepare XDG data directory %q: %w", dataHome, err)
	}
	directory := filepath.Join(dataHome, "applications")
	if err := ensureOwnedDirectory(directory); err != nil {
		return "", fmt.Errorf("prepare desktop applications directory %q: %w", directory, err)
	}
	return directory, nil
}

func ensureOwnedDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("path must be a real directory")
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("directory is not owned by the current user")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("directory is writable by another user or group")
	}
	return nil
}

func launcherPath(directory, id string) (string, error) {
	if !validLauncherID(id) {
		return "", fmt.Errorf("launcher id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}
	return filepath.Join(directory, "isobox-"+id+".desktop"), nil
}

func verifyManagedLauncher(path string, allowMissing bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("launcher %q is not installed", path)
		}
		return fmt.Errorf("inspect launcher %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace or remove %q: it is not a regular file", path)
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("refusing to replace or remove %q: it is not owned by the current user", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read launcher %q: %w", path, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSettingsFileSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read launcher %q: %w", path, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close launcher %q: %w", path, closeErr)
	}
	if len(data) > maxSettingsFileSize {
		return fmt.Errorf("refusing to replace or remove %q: launcher file is too large to verify", path)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == managedLauncherMarker {
			return nil
		}
	}
	return fmt.Errorf("refusing to replace or remove %q: file is not marked as managed by isobox", path)
}

func writeLauncherAtomically(directory, id, path string, content []byte) error {
	temporary, err := os.CreateTemp(directory, ".isobox-"+id+"-*.tmp")
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

func ownedByCurrentUser(info os.FileInfo) bool {
	return isOwnedByCurrentUser(info)
}
