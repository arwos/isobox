//go:build linux

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/proxy"
)

func executeForTest(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := Execute(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestProxyCLIAddListRemoveAndNoCredentialOutput(t *testing.T) {
	configHome := t.TempDir()
	if err := os.Chmod(configHome, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)

	code, stdout, stderr := executeForTest("proxy", "add", "--alias=work", "--server=127.0.0.1:1080")
	if code != 0 || !strings.Contains(stdout, "Added SOCKS5 proxy") {
		t.Fatalf("proxy add = (%d, %q, %q)", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "does not encrypt") {
		t.Fatalf("proxy add warning = %q", stderr)
	}
	if err := proxy.Add("secure-auth", "proxy.example:1080", proxy.AuthUserPass, "user-secret", "password-secret"); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr = executeForTest("proxy", "list")
	if code != 0 || !strings.Contains(stdout, "work\t127.0.0.1:1080\tnone") || !strings.Contains(stdout, "secure-auth") {
		t.Fatalf("proxy list = (%d, %q, %q)", code, stdout, stderr)
	}
	if strings.Contains(stdout, "user-secret") || strings.Contains(stdout, "password-secret") || strings.Contains(stderr, "user-secret") || strings.Contains(stderr, "password-secret") {
		t.Fatalf("proxy list exposed credentials: stdout=%q stderr=%q", stdout, stderr)
	}

	code, stdout, stderr = executeForTest("proxy", "remove", "--alias=work")
	if code != 0 || !strings.Contains(stdout, "Removed SOCKS5 proxy") {
		t.Fatalf("proxy remove = (%d, %q, %q)", code, stdout, stderr)
	}
}

func TestProxyCLIRejectsCredentialArgumentsWithoutEchoingThem(t *testing.T) {
	secret := "DO-NOT-LEAK-CREDENTIAL-ARG"
	code, stdout, stderr := executeForTest("proxy", "add", "--alias=work", "--server=127.0.0.1:1080", "--auth=userpass", "--username="+secret, "--password="+secret)
	if code == 0 {
		t.Fatal("proxy add accepted credential command-line options")
	}
	if strings.Contains(stdout, secret) || strings.Contains(stderr, secret) {
		t.Fatalf("credential argument leaked to output: stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestProxyRunRejectsMissingAliasBeforeHostPreflight(t *testing.T) {
	configHome := t.TempDir()
	if err := os.Chmod(configHome, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	profilePath := filepath.Join(t.TempDir(), "proxy-profile.unic")
	if err := os.WriteFile(profilePath, []byte("version 1; network { mode proxy; dns '1.1.1.1'; proxy_alias 'missing'; }"), 0600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := executeForTest("run", "--profile="+profilePath, "--", "/usr/bin/true")
	if code == 0 || !strings.Contains(stderr, "proxy alias \"missing\" is unavailable") {
		t.Fatalf("run missing alias = (%d, %q)", code, stderr)
	}
	if strings.Contains(stderr, "host preflight failed") {
		t.Fatalf("run attempted host preflight before validating alias: %q", stderr)
	}
}

func TestProxyRunRejectsInsecureStoreBeforeHostPreflight(t *testing.T) {
	configHome := t.TempDir()
	if err := os.Chmod(configHome, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := proxy.Add("present", "127.0.0.1:1080", proxy.AuthNone, "", ""); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(configHome, "isobox", "proxies.unic")
	if err := os.Chmod(storePath, 0644); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(t.TempDir(), "proxy-profile.unic")
	if err := os.WriteFile(profilePath, []byte("version 1; network { mode proxy; dns '1.1.1.1'; proxy_alias 'present'; }"), 0600); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := executeForTest("run", "--profile="+profilePath, "--", "/usr/bin/true")
	if code == 0 || !strings.Contains(stderr, "permissions must be exactly 0600") {
		t.Fatalf("run with insecure store = (%d, %q)", code, stderr)
	}
	if strings.Contains(stderr, "host preflight failed") {
		t.Fatalf("run attempted host preflight before checking store permissions: %q", stderr)
	}
}

func TestProxyRunWarnsForConfiguredUnencryptedProxyWithoutExposingCredentials(t *testing.T) {
	configHome := t.TempDir()
	if err := os.Chmod(configHome, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configHome)
	if err := proxy.Add("corp", "proxy.example:1080", proxy.AuthUserPass, "private-user", "private-password"); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := inspectAndWarnProxy("corp", &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), `SOCKS5 proxy "corp" does not encrypt credentials or application traffic`) {
		t.Fatalf("warning = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "private-user") || strings.Contains(stderr.String(), "private-password") {
		t.Fatalf("warning exposed proxy credentials: %q", stderr.String())
	}
}
