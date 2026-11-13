//go:build linux

package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func configureTestStore(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", base)
	return base, filepath.Join(base, "isobox", storeFileName)
}

func TestProxyStoreRoundTripPrivateModeAndMetadata(t *testing.T) {
	base, path := configureTestStore(t)
	if err := Add("work-proxy", "proxy.example:1080", AuthUserPass, "alice", "p@ss 'word\"\\"); err != nil {
		t.Fatal(err)
	}

	info, err := Inspect("work-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if info != (Info{Alias: "work-proxy", Server: "proxy.example:1080", Auth: AuthUserPass}) {
		t.Fatalf("Inspect() = %#v", info)
	}
	items, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0] != info {
		t.Fatalf("List() = %#v", items)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0600 {
		t.Fatalf("proxy store permissions = %04o, want 0600", fileInfo.Mode().Perm())
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0700 {
		t.Fatalf("proxy directory permissions = %04o, want 0700", directoryInfo.Mode().Perm())
	}
	baseInfo, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	if baseInfo.Mode().Perm() != 0755 {
		t.Fatalf("XDG config directory permissions changed to %04o", baseInfo.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "alice") || !strings.Contains(string(contents), "p@ss") {
		t.Fatal("expected the unencrypted credential fields in the private UNIC store")
	}

	runtimeConfig, err := loadRuntimeConfig("work-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if runtimeConfig.username != "alice" || runtimeConfig.password != "p@ss 'word\"\\" {
		t.Fatal("runtime loader did not preserve SOCKS5 credentials")
	}
	if err := Remove("work-proxy"); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect("work-proxy"); err == nil {
		t.Fatal("Inspect() succeeded after alias removal")
	}
}

func TestProxyStoreRejectsUnsafePermissionsAndSymlinks(t *testing.T) {
	_, path := configureTestStore(t)
	if err := Add("safe", "127.0.0.1:1080", AuthNone, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect("safe"); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("Inspect() with mode 0644 error = %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("List() with directory mode 0755 error = %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "external.unic")
	if err := os.WriteFile(target, []byte("version 1;"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil {
		t.Fatal("List() followed a symlinked proxy store")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("List() with FIFO store error = %v", err)
	}
}

func TestProxyStoreRejectsInsecureConfigHome(t *testing.T) {
	base, _ := configureTestStore(t)
	if err := Add("safe", "127.0.0.1:1080", AuthNone, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect("safe"); err == nil || !strings.Contains(err.Error(), "group or other") {
		t.Fatalf("Inspect() with insecure config home error = %v", err)
	}
}

func TestProxyStoreRejectsCorruptDocumentWithoutEchoingCredential(t *testing.T) {
	_, path := configureTestStore(t)
	if err := Add("corp", "127.0.0.1:1080", AuthUserPass, "user", "TOP-SECRET-DO-NOT-PRINT"); err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []string{
		"version 1; proxy { alias 'corp'; password 'TOP-SECRET-DO-NOT-PRINT' }",
		"version 1; proxy { alias 'corp'; server '127.0.0.1:1080'; auth userpass; username 'user'; password 'TOP-SECRET-DO-NOT-PRINT'; extra 'x'; }",
		"version 1; proxy { alias 'corp'; alias 'other'; server '127.0.0.1:1080'; password 'TOP-SECRET-DO-NOT-PRINT'; }",
	} {
		if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Inspect("corp")
		if err == nil {
			t.Fatal("Inspect() accepted a malformed store")
		}
		if strings.Contains(err.Error(), "TOP-SECRET-DO-NOT-PRINT") {
			t.Fatalf("store error leaked a credential: %v", err)
		}
	}
}

func TestProxyStoreRejectsInvalidDefinitionsAndDuplicateAliases(t *testing.T) {
	for _, server := range []string{"", "127.0.0.1", "[::1]:1080", "proxy.example:0", "proxy.example:65536", "bad host:1080"} {
		if err := Validate("safe", server, AuthNone); err == nil {
			t.Errorf("Validate(%q) succeeded", server)
		}
	}
	if err := Validate("-bad", "127.0.0.1:1080", AuthNone); err == nil {
		t.Fatal("Validate() accepted an invalid alias")
	}
	if err := Validate("safe", "127.0.0.1:1080", "other"); err == nil {
		t.Fatal("Validate() accepted an invalid auth mode")
	}
	if err := validateCredentials(AuthUserPass, "", "secret"); err == nil {
		t.Fatal("validateCredentials() accepted an empty username")
	}
	if err := validateCredentials(AuthUserPass, "user", strings.Repeat("x", 256)); err == nil {
		t.Fatal("validateCredentials() accepted a password over 255 bytes")
	}

	configureTestStore(t)
	if err := Add("same", "127.0.0.1:1080", AuthNone, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Add("same", "127.0.0.2:1080", AuthNone, "", ""); err == nil {
		t.Fatal("Add() accepted a duplicate alias")
	}
}

func TestListMissingStoreReturnsEmptyWithoutCreatingIt(t *testing.T) {
	base, path := configureTestStore(t)
	if err := os.Remove(base); err != nil {
		t.Fatal(err)
	}
	items, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("List() = %#v, want empty", items)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("List() created proxy store, Lstat error = %v", err)
	}
}
