// Package integration provides optional XDG desktop integration and settings
// defaults without adding a service or privilege requirement to `isobox run`.
package integration

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/arwos/isobox/internal/profile"
)

const settingsFileName = "settings.unic"

const maxSettingsFileSize = 1 << 20

// LoadSettings combines built-in defaults, the highest-priority system XDG
// settings file, and the user's settings file, in that order.
func LoadSettings() (profile.Settings, error) {
	settings := profile.DefaultSettings()
	systemPath, err := firstSystemSettingsPath()
	if err != nil {
		return profile.Settings{}, err
	}
	if systemPath != "" {
		settings, err = applySettingsFile(settings, systemPath)
		if err != nil {
			return profile.Settings{}, fmt.Errorf("load system defaults %q: %w", systemPath, err)
		}
	}

	userPath := userSettingsPath()
	if userPath != "" {
		settings, err = applySettingsFileIfPresent(settings, userPath)
		if err != nil {
			return profile.Settings{}, fmt.Errorf("load user defaults %q: %w", userPath, err)
		}
	}
	return settings, nil
}

func applySettingsFileIfPresent(base profile.Settings, path string) (profile.Settings, error) {
	data, err := readSettingsFile(path)
	if os.IsNotExist(err) {
		return base, nil
	}
	if err != nil {
		return profile.Settings{}, err
	}
	settings, err := profile.ApplySettingsOverride(base, data)
	if err != nil {
		return profile.Settings{}, err
	}
	return settings, nil
}

func applySettingsFile(base profile.Settings, path string) (profile.Settings, error) {
	data, err := readSettingsFile(path)
	if err != nil {
		return profile.Settings{}, err
	}
	settings, err := profile.ApplySettingsOverride(base, data)
	if err != nil {
		return profile.Settings{}, err
	}
	return settings, nil
}

func readSettingsFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("settings path must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSettingsFileSize {
		return nil, fmt.Errorf("settings file exceeds %d bytes", maxSettingsFileSize)
	}
	return data, nil
}

func firstSystemSettingsPath() (string, error) {
	directories := systemConfigDirectories()
	for _, directory := range directories {
		path := filepath.Join(directory, "isobox", settingsFileName)
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect system settings %q: %w", path, err)
		}
		return path, nil
	}
	return "", nil
}

func systemConfigDirectories() []string {
	value := os.Getenv("XDG_CONFIG_DIRS")
	if value == "" {
		return []string{"/etc/xdg"}
	}
	var directories []string
	for _, directory := range filepath.SplitList(value) {
		if filepath.IsAbs(directory) {
			directories = append(directories, filepath.Clean(directory))
		}
	}
	if len(directories) == 0 {
		return []string{"/etc/xdg"}
	}
	return directories
}

func userConfigHome() (string, error) {
	if directory := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(directory) {
		return filepath.Clean(directory), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine the user's home directory for isobox settings: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("cannot determine the user's home directory for isobox settings")
	}
	return filepath.Join(home, ".config"), nil
}

func userDataHome() (string, error) {
	if directory := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(directory) {
		return filepath.Clean(directory), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine the user's home directory for desktop integration: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("cannot determine the user's home directory for desktop integration")
	}
	return filepath.Join(home, ".local", "share"), nil
}

func userSettingsPath() string {
	base, err := userConfigHome()
	if err != nil {
		// User-wide defaults are optional. A valid XDG_CONFIG_HOME still works
		// without HOME; otherwise retain system and built-in defaults.
		return ""
	}
	return filepath.Join(base, "isobox", settingsFileName)
}
