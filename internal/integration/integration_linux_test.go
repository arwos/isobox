//go:build linux

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/profile"
)

func TestInstallUpdateAndRemoveLauncher(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "xdg data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	profilePath := filepath.Join(t.TempDir(), "example profile.unic")
	if err := os.WriteFile(profilePath, []byte("version 1;"), 0o600); err != nil {
		t.Fatal(err)
	}

	options := LauncherOptions{
		ID:      "example-app",
		Name:    "Example Application",
		Profile: profilePath,
		AppID:   "example.app",
		Command: []string{"/usr/bin/true", "argument with spaces", "100%", "$HOME"},
	}
	path, err := InstallLauncher(options)
	if err != nil {
		t.Fatalf("InstallLauncher() error = %v", err)
	}
	wantPath := filepath.Join(dataHome, "applications", "isobox-example-app.desktop")
	if path != wantPath {
		t.Fatalf("InstallLauncher() path = %q, want %q", path, wantPath)
	}
	content := readLauncherTestFile(t, path)
	if !strings.Contains(content, "X-Isobox-Managed=true") || !strings.Contains(content, "--app-id=example.app") {
		t.Fatalf("launcher is missing its marker or app-id:\n%s", content)
	}
	if !strings.Contains(content, "%%") || !strings.Contains(content, "argument with spaces") {
		t.Fatalf("launcher did not encode literal percent or spaced arguments:\n%s", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("launcher permissions = %#o, want 0600", got)
	}

	options.Name = "Updated Example"
	if _, err := InstallLauncher(options); err != nil {
		t.Fatalf("update managed launcher: %v", err)
	}
	content = readLauncherTestFile(t, path)
	if !strings.Contains(content, "Name=Updated Example") {
		t.Fatalf("managed launcher was not updated:\n%s", content)
	}

	if _, err := RemoveLauncher(options.ID); err != nil {
		t.Fatalf("RemoveLauncher() error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("launcher still exists after removal, Lstat error = %v", err)
	}
	if _, err := os.Stat(profilePath); err != nil {
		t.Fatalf("launcher removal changed the profile: %v", err)
	}
}

func TestLauncherManagementRefusesUnmarkedFile(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	directory := filepath.Join(dataHome, "applications")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "isobox-foreign.desktop")
	original := []byte("[Desktop Entry]\nName=Foreign\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "profile.unic")
	if err := os.WriteFile(profilePath, []byte("version 1;"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RemoveLauncher("foreign"); err == nil {
		t.Fatal("RemoveLauncher() removed an unmarked file")
	}
	if _, err := InstallLauncher(LauncherOptions{
		ID: "foreign", Name: "Foreign", Profile: profilePath, AppID: "foreign", Command: []string{"/usr/bin/true"},
	}); err == nil {
		t.Fatal("InstallLauncher() replaced an unmarked file")
	}
	if got := readLauncherTestFile(t, path); got != string(original) {
		t.Fatalf("unmarked file changed: %q", got)
	}
}

func TestLoadSettingsSystemUserAndProfilePrecedence(t *testing.T) {
	configOne := filepath.Join(t.TempDir(), "system one")
	configTwo := filepath.Join(t.TempDir(), "system two")
	userConfig := filepath.Join(t.TempDir(), "user config")
	writeSettingsTestFile(t, filepath.Join(configOne, "isobox", settingsFileName), `version 1; defaults { network_mode direct; dns '192.0.2.10'; }`)
	writeSettingsTestFile(t, filepath.Join(configTwo, "isobox", settingsFileName), `version 1; defaults { network_mode off; dns '192.0.2.20'; }`)
	writeSettingsTestFile(t, filepath.Join(userConfig, "isobox", settingsFileName), `version 1; defaults { dns '198.51.100.10'; }`)
	t.Setenv("XDG_CONFIG_DIRS", configOne+string(os.PathListSeparator)+configTwo)
	t.Setenv("XDG_CONFIG_HOME", userConfig)

	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if settings.Defaults.NetworkMode != profile.NetworkModeDirect {
		t.Fatalf("system network default = %q, want first system layer direct", settings.Defaults.NetworkMode)
	}
	if settings.Defaults.DNS != "198.51.100.10" {
		t.Fatalf("user DNS default = %q, want 198.51.100.10", settings.Defaults.DNS)
	}

	effective, err := profile.ResolveProfile(profile.Profile{
		Version: profile.SchemaVersion,
		Network: profile.NetworkConfig{Mode: profile.NetworkModeOff, DNS: "203.0.113.10"},
	}, settings)
	if err != nil {
		t.Fatalf("ResolveProfile() error = %v", err)
	}
	if effective.NetworkMode != profile.NetworkModeOff || effective.DNSServer != "203.0.113.10" {
		t.Fatalf("profile overrides = (%q, %q), want (off, 203.0.113.10)", effective.NetworkMode, effective.DNSServer)
	}
}

func TestLoadSettingsRejectsMalformedExistingOverride(t *testing.T) {
	config := filepath.Join(t.TempDir(), "system")
	writeSettingsTestFile(t, filepath.Join(config, "isobox", settingsFileName), `version 1; defaults { unknown true; }`)
	t.Setenv("XDG_CONFIG_DIRS", config)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "user"))

	if _, err := LoadSettings(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("LoadSettings() error = %v, want unknown field error", err)
	}
}

func readLauncherTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeSettingsTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
