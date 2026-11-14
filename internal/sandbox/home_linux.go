//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

func resolveHome(profileHome, profilePath, appID string) (string, error) {
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

	canonicalProfile, err := filepath.Abs(profilePath)
	if err != nil {
		return "", fmt.Errorf("resolve profile path %q: %w", profilePath, err)
	}
	canonicalProfile, err = filepath.EvalSymlinks(canonicalProfile)
	if err != nil {
		return "", fmt.Errorf("resolve profile path %q: %w", profilePath, err)
	}
	profileInfo, err := os.Stat(canonicalProfile)
	if err != nil {
		return "", fmt.Errorf("inspect profile path %q: %w", canonicalProfile, err)
	}
	if !profileInfo.Mode().IsRegular() {
		return "", fmt.Errorf("profile path %q must be a regular file to identify its persistent HOME", canonicalProfile)
	}

	tempInfo, err := os.Lstat("/tmp")
	if err != nil {
		return "", fmt.Errorf("inspect /tmp for persistent HOME: %w", err)
	}
	if !tempInfo.IsDir() || tempInfo.Mode()&os.ModeSymlink != 0 || tempInfo.Mode()&os.ModeSticky == 0 {
		return "", fmt.Errorf("/tmp must be a real directory with the sticky bit enabled for persistent HOME storage")
	}

	uid := os.Geteuid()
	privateRoot := filepath.Join("/tmp", "isobox-"+strconv.Itoa(uid))
	if err := ensurePrivateDirectory(privateRoot); err != nil {
		return "", fmt.Errorf("prepare private HOME root %q: %w", privateRoot, err)
	}
	identity := append([]byte(canonicalProfile), 0)
	identity = append(identity, appID...)
	digest := sha256.Sum256(identity)
	home := filepath.Join(privateRoot, fmt.Sprintf("%x", digest[:]))
	if err := ensurePrivateDirectory(home); err != nil {
		return "", fmt.Errorf("prepare persistent HOME %q: %w", home, err)
	}
	return home, nil
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
