package preflight

import (
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/profile"
)

func TestSelectBackend(t *testing.T) {
	tests := []struct {
		name          string
		config        profile.EffectiveProfile
		capabilities  Capabilities
		wantNetwork   BackendKind
		wantForwarder BackendKind
		wantFilter    bool
		wantErr       string
	}{
		{
			name: "off without host ports needs no network helper",
			config: profile.EffectiveProfile{
				NetworkMode: profile.NetworkModeOff,
				Profile:     profile.Profile{Network: profile.NetworkConfig{Mode: profile.NetworkModeOff}},
			},
			wantNetwork:   BackendNone,
			wantForwarder: BackendNone,
		},
		{
			name:   "direct prefers capable pasta",
			config: directTestProfile(nil),
			capabilities: Capabilities{
				Pasta:              "/usr/bin/pasta",
				PastaOptions:       testOptionSet("--config-net", "--tcp-ports", "--udp-ports", "--tcp-ns", "--udp-ns", "--foreground", "--pid", "--dns-forward", "--dns-host"),
				Slirp4netns:        "/usr/bin/slirp4netns",
				Slirp4netnsOptions: testOptionSet("--configure", "--enable-ipv6", "--ready-fd", "--exit-fd"),
			},
			wantNetwork:   BackendPasta,
			wantForwarder: BackendPasta,
		},
		{
			name: "direct falls back to slirp with supported explicit forward",
			config: directTestProfile([]profile.PortRule{{
				HostAddress: "127.0.0.1", Port: 8123, Protocol: profile.PortProtocolTCP,
			}}),
			capabilities: Capabilities{
				Pasta:              "/usr/bin/pasta",
				PastaOptions:       testOptionSet("--tcp-ports"),
				Slirp4netns:        "/usr/bin/slirp4netns",
				Slirp4netnsOptions: testOptionSet("--configure", "--enable-ipv6", "--ready-fd", "--exit-fd", "--api-socket"),
			},
			wantNetwork:   BackendSlirp4netns,
			wantForwarder: BackendSlirp4netns,
		},
		{
			name: "direct refuses IPv6 host forward on slirp",
			config: directTestProfile([]profile.PortRule{{
				HostAddress: "::1", Port: 8123, Protocol: profile.PortProtocolTCP,
			}}),
			capabilities: Capabilities{
				Slirp4netns:        "/usr/bin/slirp4netns",
				Slirp4netnsOptions: testOptionSet("--configure", "--enable-ipv6", "--ready-fd", "--exit-fd", "--api-socket"),
			},
			wantErr: "is IPv6",
		},
		{
			name: "off with ports requires pasta firewall options",
			config: offTestProfile([]profile.PortRule{{
				HostAddress: "127.0.0.1", Port: 8123, Protocol: profile.PortProtocolBoth,
			}}),
			capabilities: Capabilities{
				Pasta: "/usr/bin/pasta",
				PastaOptions: testOptionSet(
					"--config-net", "--tcp-ports", "--udp-ports", "--tcp-ns", "--udp-ns", "--no-splice",
					"--no-icmp", "--no-dhcp", "--no-dhcpv6", "--no-ndp", "--no-ra", "--foreground",
				),
			},
			wantNetwork:   BackendNone,
			wantForwarder: BackendPasta,
			wantFilter:    true,
		},
		{
			name: "off refuses incomplete pasta",
			config: offTestProfile([]profile.PortRule{{
				HostAddress: "127.0.0.1", Port: 8123, Protocol: profile.PortProtocolTCP,
			}}),
			capabilities: Capabilities{Pasta: "/usr/bin/pasta", PastaOptions: testOptionSet("--tcp-ports")},
			wantErr:      "lacks options required to enforce off-mode egress",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SelectBackend(test.config, test.capabilities)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("SelectBackend() error = %v, want text %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectBackend() error = %v", err)
			}
			if got.Network != test.wantNetwork || got.PortForwarder != test.wantForwarder || got.EnforceEgressFilter != test.wantFilter {
				t.Fatalf("SelectBackend() = (network=%q forwarder=%q filter=%t), want (%q, %q, %t)",
					got.Network, got.PortForwarder, got.EnforceEgressFilter,
					test.wantNetwork, test.wantForwarder, test.wantFilter)
			}
		})
	}
}

func directTestProfile(ports []profile.PortRule) profile.EffectiveProfile {
	return profile.EffectiveProfile{
		NetworkMode: profile.NetworkModeDirect,
		DNSServer:   "192.0.2.53",
		Profile: profile.Profile{Network: profile.NetworkConfig{
			Mode:  profile.NetworkModeDirect,
			Ports: ports,
		}},
	}
}

func offTestProfile(ports []profile.PortRule) profile.EffectiveProfile {
	return profile.EffectiveProfile{
		NetworkMode: profile.NetworkModeOff,
		Profile: profile.Profile{Network: profile.NetworkConfig{
			Mode:  profile.NetworkModeOff,
			Ports: ports,
		}},
	}
}

func testOptionSet(options ...string) map[string]bool {
	set := make(map[string]bool, len(options))
	for _, option := range options {
		set[option] = true
	}
	return set
}
