//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package proxy

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

type egressLogger struct {
	mu     sync.Mutex
	file   *os.File
	logger *log.Logger
	warned bool
}

func openEgressLogger(appID string) (*egressLogger, error) {
	if appID == "" {
		appID = "default"
	}
	if !validLogAppID(appID) {
		return nil, fmt.Errorf("app-id is invalid for the egress log filename")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, fmt.Errorf("cannot determine the user's home directory for the egress log")
	}
	root := filepath.Join(home, ".isobox")
	directory := filepath.Join(root, "log")
	if err := ensureEgressDirectory(root); err != nil {
		return nil, fmt.Errorf("prepare isobox log root %q: %w", root, err)
	}
	if err := ensureEgressDirectory(directory); err != nil {
		return nil, fmt.Errorf("prepare isobox log directory %q: %w", directory, err)
	}
	path := filepath.Join(directory, appID+".log")
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = file.Close()
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("%s must not have additional hard links", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, fmt.Errorf("%s is not owned by the current user", path)
	}
	if stat.Mode&0777 != 0600 {
		if err := unix.Fchmod(fd, 0600); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("restrict permissions on %s: %w", path, err)
		}
	}
	return &egressLogger{
		file:   file,
		logger: log.New(file, "", log.Ldate|log.Ltime|log.LUTC),
	}, nil
}

func ensureEgressDirectory(path string) error {
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
	}
	return nil
}

func validLogAppID(appID string) bool {
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

func (logger *egressLogger) logf(format string, values ...any) {
	if logger == nil {
		return
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if err := logger.logger.Output(2, fmt.Sprintf(format, values...)); err != nil && !logger.warned {
		logger.warned = true
		_, _ = fmt.Fprintf(os.Stderr, "isobox: egress event log write failed: %v\n", err)
	}
}

func (logger *egressLogger) close() error {
	if logger == nil {
		return nil
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	return logger.file.Close()
}
