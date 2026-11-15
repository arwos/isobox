//go:build linux

package processlist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	EnvAppID   = "ISOBOX_RUN_APP_ID"
	EnvCWD     = "ISOBOX_RUN_CWD"
	EnvNetwork = "ISOBOX_RUN_NETWORK"
)

type Entry struct {
	PID     int    `json:"pid"`
	AppID   string `json:"app_id"`
	CWD     string `json:"cwd"`
	Network string `json:"network"`
	Started uint64 `json:"started"`
}

type Usage struct {
	Entry
	MemoryBytes uint64
	CPUPercent  float64
}

func Register(pid int, appID, cwd, network string) (func(), error) {
	started, _, _, err := processStat(pid)
	if err != nil {
		return nil, err
	}
	entry := Entry{PID: pid, AppID: appID, CWD: cwd, Network: network, Started: started}
	directory, err := registryDirectory()
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

func List() ([]Usage, error) {
	directory, err := registryDirectory()
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, path := range files {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var entry Entry
		if json.Unmarshal(data, &entry) != nil || entry.PID < 1 {
			continue
		}
		started, _, _, statErr := processStat(entry.PID)
		if statErr != nil || started != entry.Started {
			_ = os.Remove(path)
			continue
		}
		entries = append(entries, entry)
	}
	first := make([]uint64, len(entries))
	for index, entry := range entries {
		_, ticks, _, _ := processStat(entry.PID)
		first[index] = ticks
	}
	if len(entries) > 0 {
		timeSleep()
	}
	result := make([]Usage, 0, len(entries))
	for index, entry := range entries {
		started, ticks, rssPages, statErr := processStat(entry.PID)
		if statErr != nil || started != entry.Started {
			continue
		}
		pageSize := uint64(os.Getpagesize())
		usage := Usage{Entry: entry, MemoryBytes: rssPages * pageSize}
		if ticks >= first[index] {
			usage.CPUPercent = float64(ticks-first[index]) / 100 * 100 / 0.5
		}
		result = append(result, usage)
	}
	return result, nil
}

func processStat(pid int) (started, ticks, rssPages uint64, err error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, 0, 0, err
	}
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 || close+2 >= len(data) {
		return 0, 0, 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(data[close+2:]))
	if len(fields) <= 21 {
		return 0, 0, 0, fmt.Errorf("incomplete process stat")
	}
	user, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	system, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	started, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, 0, err
	}
	rss, err := strconv.ParseInt(fields[21], 10, 64)
	if err != nil || rss < 0 {
		return 0, 0, 0, fmt.Errorf("invalid RSS value")
	}
	return started, user + system, uint64(rss), nil
}

func registryDirectory() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" || !filepath.IsAbs(base) {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("locate user runtime directory: %w", err)
		}
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(base)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is not a directory")
	}
	baseStat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || baseStat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("user runtime/cache directory is not private and owned by the current user")
	}
	parent := filepath.Join(base, "isobox")
	if err := ensurePrivateDirectory(parent); err != nil {
		return "", err
	}
	dir := filepath.Join(parent, "processes")
	if err := ensurePrivateDirectory(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("process registry path %q is not a real directory owned by the current user", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("process registry directory %q is writable by another user or group", path)
	}
	return os.Chmod(path, 0o700)
}

var timeSleep = func() { time.Sleep(500 * time.Millisecond) }
