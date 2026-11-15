package profile

import (
	"reflect"
	"strings"
	"testing"

	"go.osspkg.com/unic"
)

func TestDecodeProfileRoundTrip(t *testing.T) {
	want := Profile{
		Version:  SchemaVersion,
		HomePath: "/home/test/isobox-app",
		Mounts: []Mount{{
			Source:   "/srv/application-data",
			Target:   "/data",
			ReadOnly: false,
		}},
		Network: NetworkConfig{
			Mode: NetworkModeDirect,
			DNS:  []string{"192.0.2.53"},
			Ports: []PortRule{
				{HostAddress: "127.0.0.1", Port: 8080, Protocol: PortProtocolTCP},
				{HostAddress: "127.0.0.1", Port: 9000, EndPort: 9002, Protocol: PortProtocolBoth},
			},
		},
		GUI: GUIAccess{Wayland: true, Audio: true},
		Limits: ResourceLimits{
			CPUQuotaPercent: 125,
			MemoryBytes:     1 << 30,
			ProcessCount:    8,
			OpenFiles:       64,
		},
	}

	wireProfile := want
	wireProfile.Network.Ports = nil
	wireProfile.Network.PortValues = []any{
		[]any{"127.0.0.1", int64(8080), "tcp"},
		[]any{"127.0.0.1", int64(9000), "both", int64(9002)},
	}
	data, err := unic.Marshal(&wireProfile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	got, err := DecodeProfile(data)
	if err != nil {
		t.Fatalf("decode profile: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded profile mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDecodeProfileRejectsUnknownField(t *testing.T) {
	_, err := DecodeProfile([]byte(`version 1; network { mode off; } security { allow_host true; }`))
	if err == nil || !strings.Contains(err.Error(), "profile.security") {
		t.Fatalf("DecodeProfile error = %v, want unknown profile.security field", err)
	}
}

func TestDecodeProfileRejectsMissingMountSource(t *testing.T) {
	_, err := DecodeProfile([]byte(`version 1; mount { target '/data'; }`))
	if err == nil || !strings.Contains(err.Error(), "profile.mount[0].source") {
		t.Fatalf("DecodeProfile error = %v, want missing profile.mount[0].source", err)
	}
}

func TestDecodeProfileRejectsIPv6NetworkSettings(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "IPv6 DNS",
			data: `version 1; network { mode direct; dns '2001:db8::53'; }`,
		},
		{
			name: "IPv6 host bind",
			data: `version 1; network { mode off; port '::1' 8080 both; }`,
		},
		{
			name: "IPv4-mapped IPv6 host bind",
			data: `version 1; network { mode off; port '::ffff:192.0.2.10' 8080 both; }`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeProfile([]byte(test.data)); err == nil || !strings.Contains(err.Error(), "IPv6 is disabled") {
				t.Fatalf("DecodeProfile() error = %v, want IPv6-disabled validation error", err)
			}
		})
	}
}

func TestSettingsRejectIPv6DNS(t *testing.T) {
	_, err := ApplySettingsOverride(DefaultSettings(), []byte(`version 1; defaults { dns '2001:db8::53'; }`))
	if err == nil || !strings.Contains(err.Error(), "IPv6 is disabled") {
		t.Fatalf("ApplySettingsOverride() error = %v, want IPv6-disabled validation error", err)
	}
}

func TestSettingsOverridesMergeByFieldBeforeProfile(t *testing.T) {
	system, err := ApplySettingsOverride(DefaultSettings(), []byte(`
		version 1;
		defaults {
			network_mode direct;
			dns '192.0.2.53';
		}
	`))
	if err != nil {
		t.Fatalf("apply system settings: %v", err)
	}
	user, err := ApplySettingsOverride(system, []byte(`
		version 1;
		defaults { dns '198.51.100.53'; }
	`))
	if err != nil {
		t.Fatalf("apply user settings: %v", err)
	}
	got, err := ResolveProfile(Profile{
		Version: SchemaVersion,
		Network: NetworkConfig{DNS: []string{"203.0.113.53"}},
	}, user)
	if err != nil {
		t.Fatalf("resolve profile: %v", err)
	}
	if got.NetworkMode != NetworkModeDirect {
		t.Errorf("effective network mode = %q, want direct from system settings", got.NetworkMode)
	}
	if got.DNSServer != "203.0.113.53" {
		t.Errorf("effective DNS = %q, want profile override", got.DNSServer)
	}
}

func TestResolveProxyProfileAllowsHostDNSFallback(t *testing.T) {
	got, err := ResolveProfile(Profile{
		Version: SchemaVersion,
		Network: NetworkConfig{Mode: NetworkModeProxy, ProxyAlias: "secure-exit"},
	}, DefaultSettings())
	if err != nil {
		t.Fatalf("ResolveProfile() error = %v", err)
	}
	if got.DNSServer != "" {
		t.Fatalf("effective DNS = %q, want unset for runtime host-resolver fallback", got.DNSServer)
	}
}
