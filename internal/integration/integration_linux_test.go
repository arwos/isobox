//go:build linux

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func readLauncherTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
