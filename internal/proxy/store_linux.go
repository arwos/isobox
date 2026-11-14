//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package proxy

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"go.osspkg.com/unic"

	"github.com/arwos/isobox/internal/profile"
)

const (
	storeFileName = "proxies.unic"
	maxStoreSize  = 1 << 20
)

type storeDocument struct {
	Version int           `unic:"version"`
	Proxies []storedProxy `unic:"proxy"`
}

type storedProxy struct {
	Alias    string   `unic:"alias"`
	Server   string   `unic:"server"`
	Auth     AuthMode `unic:"auth,default=none"`
	Username string   `unic:"username,omitempty"`
	Password string   `unic:"password,omitempty"`
}

// Add stores a proxy definition. Credentials are accepted as input only so
// the CLI can pass values read interactively; they are never returned by the
// management API.
func Add(alias, server string, auth AuthMode, username, password string) error {
	if err := Validate(alias, server, auth); err != nil {
		return err
	}
	server, err := normalizeServer(server)
	if err != nil {
		return err
	}
	if err := validateCredentials(auth, username, password); err != nil {
		return err
	}
	directory, _, err := storeDirectory(true)
	if err != nil {
		return err
	}
	lock, err := lockStoreDirectory(directory)
	if err != nil {
		return err
	}
	defer unlockStoreDirectory(lock)

	document, err := readDocument(directory)
	if err != nil {
		return err
	}
	for _, existing := range document.Proxies {
		if existing.Alias == alias {
			return fmt.Errorf("proxy alias already exists")
		}
	}
	document.Proxies = append(document.Proxies, storedProxy{
		Alias:    alias,
		Server:   server,
		Auth:     auth,
		Username: username,
		Password: password,
	})
	return writeDocument(directory, document)
}

// List returns aliases and connection metadata, never credentials. A missing
// store is an empty list and does not create configuration files.
func List() ([]Info, error) {
	directory, exists, err := storeDirectory(false)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []Info{}, nil
	}
	document, err := readDocument(directory)
	if err != nil {
		return nil, err
	}
	items := make([]Info, 0, len(document.Proxies))
	for _, entry := range document.Proxies {
		items = append(items, Info{Alias: entry.Alias, Server: entry.Server, Auth: entry.Auth})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Alias < items[j].Alias })
	return items, nil
}

// Inspect returns metadata for alias without exposing its username or
// password. It is used by run preflight to fail closed before starting bwrap.
func Inspect(alias string) (Info, error) {
	if !profile.IsValidProxyAlias(alias) {
		return Info{}, fmt.Errorf("proxy alias is invalid")
	}
	directory, exists, err := storeDirectory(false)
	if err != nil {
		return Info{}, err
	}
	if !exists {
		return Info{}, fmt.Errorf("proxy alias is not configured")
	}
	document, err := readDocument(directory)
	if err != nil {
		return Info{}, err
	}
	for _, entry := range document.Proxies {
		if entry.Alias == alias {
			return Info{Alias: entry.Alias, Server: entry.Server, Auth: entry.Auth}, nil
		}
	}
	return Info{}, fmt.Errorf("proxy alias is not configured")
}

// Remove deletes a stored alias and atomically replaces the secrets file.
func Remove(alias string) error {
	if !profile.IsValidProxyAlias(alias) {
		return fmt.Errorf("proxy alias is invalid")
	}
	directory, exists, err := storeDirectory(false)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("proxy alias is not configured")
	}
	lock, err := lockStoreDirectory(directory)
	if err != nil {
		return err
	}
	defer unlockStoreDirectory(lock)

	document, err := readDocument(directory)
	if err != nil {
		return err
	}
	index := -1
	for i := range document.Proxies {
		if document.Proxies[i].Alias == alias {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("proxy alias is not configured")
	}
	document.Proxies = append(document.Proxies[:index], document.Proxies[index+1:]...)
	return writeDocument(directory, document)
}

// loadRuntimeConfig is the only credential-reading API. Keeping it private
// prevents the CLI package from receiving the stored username or password.
func loadRuntimeConfig(alias string) (runtimeConfig, error) {
	if !profile.IsValidProxyAlias(alias) {
		return runtimeConfig{}, fmt.Errorf("proxy alias is invalid")
	}
	directory, exists, err := storeDirectory(false)
	if err != nil {
		return runtimeConfig{}, err
	}
	if !exists {
		return runtimeConfig{}, fmt.Errorf("proxy alias is not configured")
	}
	document, err := readDocument(directory)
	if err != nil {
		return runtimeConfig{}, err
	}
	for _, entry := range document.Proxies {
		if entry.Alias == alias {
			return runtimeConfig{
				alias:    entry.Alias,
				server:   entry.Server,
				auth:     entry.Auth,
				username: entry.Username,
				password: entry.Password,
			}, nil
		}
	}
	return runtimeConfig{}, fmt.Errorf("proxy alias is not configured")
}

func validateCredentials(auth AuthMode, username, password string) error {
	if auth == AuthNone {
		if username != "" || password != "" {
			return fmt.Errorf("credentials cannot be supplied when proxy authentication is none")
		}
		return nil
	}
	if auth != AuthUserPass {
		return fmt.Errorf("proxy authentication must be none or userpass")
	}
	if !validCredential(username) || !validCredential(password) {
		return fmt.Errorf("SOCKS5 username and password must each contain 1-255 bytes and no NUL byte")
	}
	return nil
}

func validCredential(value string) bool {
	return len(value) >= 1 && len(value) <= 255 && !strings.ContainsRune(value, '\x00')
}

func validateDocument(document storeDocument) error {
	if document.Version != 1 {
		return fmt.Errorf("proxy store has an unsupported schema version")
	}
	seen := make(map[string]struct{}, len(document.Proxies))
	for index, entry := range document.Proxies {
		if !profile.IsValidProxyAlias(entry.Alias) {
			return fmt.Errorf("proxy store entry %d has an invalid alias", index)
		}
		if _, exists := seen[entry.Alias]; exists {
			return fmt.Errorf("proxy store contains duplicate aliases")
		}
		seen[entry.Alias] = struct{}{}
		if err := Validate(entry.Alias, entry.Server, entry.Auth); err != nil {
			return fmt.Errorf("proxy store entry %d is invalid", index)
		}
		if err := validateCredentials(entry.Auth, entry.Username, entry.Password); err != nil {
			return fmt.Errorf("proxy store entry %d has invalid credentials", index)
		}
	}
	return nil
}

func storeDirectory(create bool) (string, bool, error) {
	base, err := configHome()
	if err != nil {
		return "", false, err
	}
	baseInfo, err := os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(base, 0700); err != nil {
			return "", false, fmt.Errorf("create proxy configuration directory: %w", err)
		}
		baseInfo, err = os.Lstat(base)
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect proxy configuration directory: %w", err)
	}
	if err := validateConfigHome(baseInfo); err != nil {
		return "", false, err
	}

	directory := filepath.Join(base, "isobox")
	directoryInfo, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", false, fmt.Errorf("create private proxy directory: %w", err)
		}
		directoryInfo, err = os.Lstat(directory)
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect private proxy directory: %w", err)
	}
	if err := validatePrivateDirectory(directoryInfo); err != nil {
		return "", false, err
	}
	return directory, true, nil
}

func configHome() (string, error) {
	if value := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("cannot determine the user's configuration directory for proxy secrets")
	}
	return filepath.Join(home, ".config"), nil
}

func validateConfigHome(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("proxy configuration directory must be a real directory")
	}
	if !isOwnedByCurrentUser(info) {
		return fmt.Errorf("proxy configuration directory must be owned by the current user")
	}
	if info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("proxy configuration directory must not be writable by group or other users")
	}
	if info.Mode().Perm()&0300 != 0300 {
		return fmt.Errorf("proxy configuration directory must be writable and searchable by its owner")
	}
	return nil
}

func validatePrivateDirectory(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("private proxy path must be a real directory")
	}
	if !isOwnedByCurrentUser(info) {
		return fmt.Errorf("private proxy directory must be owned by the current user")
	}
	if info.Mode().Perm() != 0700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return fmt.Errorf("private proxy directory permissions must be exactly 0700")
	}
	return nil
}

func readDocument(directory string) (storeDocument, error) {
	document := storeDocument{Version: 1, Proxies: []storedProxy{}}
	path := filepath.Join(directory, storeFileName)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return document, nil
	}
	if err != nil {
		return storeDocument{}, fmt.Errorf("open proxy store: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return storeDocument{}, fmt.Errorf("inspect proxy store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return storeDocument{}, fmt.Errorf("proxy store must be a regular file")
	}
	if !isOwnedByCurrentUser(info) {
		return storeDocument{}, fmt.Errorf("proxy store must be owned by the current user")
	}
	if info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return storeDocument{}, fmt.Errorf("proxy store permissions must be exactly 0600")
	}
	if info.Size() > maxStoreSize {
		return storeDocument{}, fmt.Errorf("proxy store exceeds %d bytes", maxStoreSize)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStoreSize+1))
	if err != nil {
		return storeDocument{}, fmt.Errorf("read proxy store: %w", err)
	}
	if len(data) > maxStoreSize {
		return storeDocument{}, fmt.Errorf("proxy store exceeds %d bytes", maxStoreSize)
	}
	if profile.ValidateUNICFields(data, storeDocument{}, "proxy store") != nil {
		return storeDocument{}, fmt.Errorf("proxy store contains an invalid UNIC document")
	}
	if err := unic.Unmarshal(data, &document); err != nil {
		// Do not return decoder errors: malformed values may include credentials.
		return storeDocument{}, fmt.Errorf("proxy store contains an invalid UNIC document")
	}
	if err := validateDocument(document); err != nil {
		return storeDocument{}, err
	}
	return document, nil
}

func writeDocument(directory string, document storeDocument) error {
	if err := validateDocument(document); err != nil {
		return err
	}
	data, err := unic.Marshal(&document)
	if err != nil {
		return fmt.Errorf("serialize proxy store")
	}
	if len(data) > maxStoreSize {
		return fmt.Errorf("proxy store exceeds %d bytes", maxStoreSize)
	}
	temporary, err := os.CreateTemp(directory, ".proxies.unic.tmp-")
	if err != nil {
		return fmt.Errorf("create temporary proxy store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary proxy store: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary proxy store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush temporary proxy store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary proxy store: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, storeFileName)); err != nil {
		return fmt.Errorf("replace proxy store: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open private proxy directory for sync: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("flush private proxy directory: %w", err)
	}
	return nil
}

func lockStoreDirectory(directory string) (*os.File, error) {
	fd, err := syscall.Open(directory, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open private proxy directory: %w", err)
	}
	file := os.NewFile(uintptr(fd), directory)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect private proxy directory: %w", err)
	}
	if err := validatePrivateDirectory(info); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock proxy store: %w", err)
	}
	return file, nil
}

func unlockStoreDirectory(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func isOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
