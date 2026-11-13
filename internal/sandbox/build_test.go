package sandbox

import (
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/profile"
)

func TestBuildDirectNetworkUsesIPv4BootstrapHelper(t *testing.T) {
	config := profile.EffectiveProfile{
		Profile: profile.Profile{
			HomePath: t.TempDir(),
			Network:  profile.NetworkConfig{Mode: profile.NetworkModeDirect},
		},
		NetworkMode: profile.NetworkModeDirect,
	}
	network := NetworkRuntime{
		Mode:       profile.NetworkModeDirect,
		Backend:    preflight.BackendPasta,
		Executable: "/usr/bin/pasta",
		Resolver:   []byte("nameserver 192.0.2.53\n"),
	}
	command, err := BuildCommand("/usr/bin/bwrap", "", config, "/tmp/profile.unic", "test-app", []string{"/usr/bin/true"}, network)
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	defer closeExtraFiles(command.ExtraFiles)

	args := strings.Join(command.Args, "\x00")
	for _, required := range []string{
		"--setenv\x00GOMAXPROCS\x001",
		"--cap-add\x00CAP_SYS_ADMIN",
		"--cap-add\x00CAP_NET_ADMIN",
		"\x00" + internalSandboxBinaryPath + "\x00--cap-add\x00CAP_SYS_ADMIN",
		"--ro-bind-data\x003\x00/etc/resolv.conf",
		internalSandboxBinaryPath + "\x00" + internalIPv4NetworkCommand + "\x00--wait-for-network\x00--\x00/usr/bin/true",
	} {
		if !strings.Contains(args, required) {
			t.Errorf("BuildCommand() arguments omit %q: %q", required, command.Args)
		}
	}
	for _, forbidden := range []string{"--unshare-net", "--block-fd"} {
		if strings.Contains(args, "\x00"+forbidden+"\x00") {
			t.Errorf("BuildCommand() includes %q; direct networking must use the late namespace gate", forbidden)
		}
	}
}

func TestBuildOffWithoutPortsUsesIPv4BootstrapHelper(t *testing.T) {
	config := profile.EffectiveProfile{
		Profile: profile.Profile{
			HomePath: t.TempDir(),
			Network:  profile.NetworkConfig{Mode: profile.NetworkModeOff},
			Limits:   profile.ResourceLimits{OpenFiles: 64},
		},
		NetworkMode: profile.NetworkModeOff,
	}
	command, err := BuildCommand("/usr/bin/bwrap", "", config, "/tmp/profile.unic", "test-app", []string{"/usr/bin/true"}, NetworkRuntime{Mode: profile.NetworkModeOff})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	defer closeExtraFiles(command.ExtraFiles)

	args := strings.Join(command.Args, "\x00")
	for _, required := range []string{
		"--setenv\x00GOMAXPROCS\x001",
		"--cap-add\x00CAP_SYS_ADMIN",
		"--cap-add\x00CAP_NET_ADMIN",
		"--block-fd\x003",
		internalSandboxBinaryPath + "\x00" + internalIPv4NetworkCommand + "\x00--\x00/usr/bin/true",
	} {
		if !strings.Contains(args, required) {
			t.Errorf("BuildCommand() arguments omit %q: %q", required, command.Args)
		}
	}
	if strings.Contains(args, "\x00--unshare-net\x00") {
		t.Fatalf("BuildCommand() includes --unshare-net; helper must disable IPv6 in the network namespace it creates")
	}
}

func TestBuildOffWithPortsLimitsHelperRuntimeTasks(t *testing.T) {
	config := profile.EffectiveProfile{
		Profile: profile.Profile{
			HomePath: t.TempDir(),
			Network: profile.NetworkConfig{
				Mode: profile.NetworkModeOff,
				Ports: []profile.PortRule{{
					HostAddress: "127.0.0.1",
					Port:        12345,
					Protocol:    profile.PortProtocolTCP,
				}},
			},
			Limits: profile.ResourceLimits{ProcessCount: 4},
		},
		NetworkMode: profile.NetworkModeOff,
	}
	command, err := BuildCommand("/usr/bin/bwrap", "/usr/sbin/nft", config, "/tmp/profile.unic", "test-app", []string{"/usr/bin/true"}, NetworkRuntime{
		Mode:       profile.NetworkModeOff,
		Backend:    preflight.BackendPasta,
		Executable: "/usr/bin/pasta",
	})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	defer closeExtraFiles(command.ExtraFiles)

	args := strings.Join(command.Args, "\x00")
	for _, required := range []string{
		"--setenv\x00GOMAXPROCS\x001",
		internalSandboxBinaryPath + "\x00" + internalOffGuardCommand,
	} {
		if !strings.Contains(args, required) {
			t.Errorf("BuildCommand() arguments omit %q: %q", required, command.Args)
		}
	}
}

func TestBuildProxyNetworkUsesPrivateTUNHelperAndIPv4Resolver(t *testing.T) {
	config := profile.EffectiveProfile{
		Profile: profile.Profile{
			HomePath: t.TempDir(),
			Network: profile.NetworkConfig{
				Mode:       profile.NetworkModeProxy,
				ProxyAlias: "private-exit",
			},
		},
		NetworkMode: profile.NetworkModeProxy,
		DNSServer:   "192.0.2.53",
	}
	command, err := BuildCommand("/usr/bin/bwrap", "", config, "/tmp/profile.unic", "test-app", []string{"/usr/bin/curl", "https://example.org"}, NetworkRuntime{
		Mode:       profile.NetworkModeProxy,
		Backend:    preflight.BackendProxyTUN,
		ProxyAlias: "private-exit",
		Resolver:   []byte("nameserver 192.0.2.53\n"),
	})
	if err != nil {
		t.Fatalf("BuildCommand() error = %v", err)
	}
	defer closeExtraFiles(command.ExtraFiles)

	args := strings.Join(command.Args, "\x00")
	for _, required := range []string{
		"--unshare-net",
		"--disable-userns",
		"--assert-userns-disabled",
		"--cap-add\x00CAP_NET_ADMIN",
		"--dev-bind\x00/dev/net/tun\x00/dev/net/tun",
		"--ro-bind-data\x003\x00/etc/resolv.conf",
		internalSandboxBinaryPath + "\x00" + internalProxyTUNCommand + "\x00--gate-path\x00" + internalProxyTUNGatePath + "\x00--\x00/usr/bin/curl\x00https://example.org",
	} {
		if !strings.Contains(args, required) {
			t.Errorf("BuildCommand() arguments omit %q: %q", required, command.Args)
		}
	}
	for _, forbidden := range []string{"--cap-add\x00CAP_SYS_ADMIN", "--block-fd", "private-exit"} {
		if strings.Contains(args, forbidden) {
			t.Errorf("BuildCommand() includes %q; proxy credentials and alias belong to the host network component, not the application command", forbidden)
		}
	}
	if len(command.ExtraFiles) != 1 {
		t.Fatalf("proxy DNS data descriptors = %d, want one", len(command.ExtraFiles))
	}
}
