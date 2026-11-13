package sandbox

import (
	"reflect"
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/profile"
)

func TestPrepareNetworkBuildsCompatibleSlirpFallback(t *testing.T) {
	selection := preflight.BackendSelection{
		Mode:            profile.NetworkModeDirect,
		Network:         preflight.BackendPasta,
		PortForwarder:   preflight.BackendPasta,
		FallbackNetwork: preflight.BackendSlirp4netns,
		DNSAddress:      "192.0.2.53",
	}
	runtime, err := PrepareNetwork(selection, "/usr/bin/pasta", "/usr/bin/slirp4netns")
	if err != nil {
		t.Fatalf("PrepareNetwork() error = %v", err)
	}
	if runtime.Backend != preflight.BackendPasta || runtime.Executable != "/usr/bin/pasta" {
		t.Fatalf("primary runtime = (%q, %q), want pasta", runtime.Backend, runtime.Executable)
	}
	if runtime.Fallback == nil || runtime.Fallback.activeBackend() != preflight.BackendSlirp4netns || runtime.Fallback.Executable != "/usr/bin/slirp4netns" {
		t.Fatalf("fallback runtime = %#v, want slirp4netns", runtime.Fallback)
	}
	if !reflect.DeepEqual(resolverNameservers(runtime.Resolver), []string{"192.0.2.53"}) {
		t.Fatalf("combined resolver = %q, want one shared DNS server", runtime.Resolver)
	}
}

func TestPrepareNetworkAcceptsZeroFallbackBackend(t *testing.T) {
	selection := preflight.BackendSelection{
		Mode:          profile.NetworkModeDirect,
		Network:       preflight.BackendSlirp4netns,
		PortForwarder: preflight.BackendSlirp4netns,
		DNSAddress:    "192.0.2.53",
	}
	runtime, err := PrepareNetwork(selection, "", "/usr/bin/slirp4netns")
	if err != nil {
		t.Fatalf("PrepareNetwork() error = %v", err)
	}
	if runtime.Fallback != nil {
		t.Fatalf("runtime fallback = %#v, want nil", runtime.Fallback)
	}
}

func TestCombineResolverConfigsAddsBackendSpecificFallback(t *testing.T) {
	got := combineResolverConfigs(
		[]byte("nameserver 169.254.1.1\nnameserver 192.0.2.53\n"),
		[]byte("nameserver 10.0.2.3\n"),
	)
	want := "nameserver 169.254.1.1\nnameserver 192.0.2.53\nnameserver 10.0.2.3\noptions timeout:1 attempts:1\n"
	if string(got) != want {
		t.Fatalf("combineResolverConfigs() = %q, want %q", got, want)
	}
}

func TestCombineResolverConfigsKeepsAtMostThreeServers(t *testing.T) {
	got := combineResolverConfigs(
		[]byte("nameserver 192.0.2.1\nnameserver 192.0.2.2\nnameserver 192.0.2.3\n"),
		[]byte("nameserver 10.0.2.3\n"),
	)
	wantServers := []string{"192.0.2.1", "192.0.2.2", "10.0.2.3"}
	if servers := resolverNameservers(got); !reflect.DeepEqual(servers, wantServers) {
		t.Fatalf("resolverNameservers() = %v, want %v", servers, wantServers)
	}
}

func TestCombineResolverConfigsReusesSharedDNS(t *testing.T) {
	primary := []byte("nameserver 192.0.2.53\n")
	got := combineResolverConfigs(primary, []byte("nameserver 192.0.2.53\n"))
	if string(got) != string(primary) || strings.Contains(string(got), "options ") {
		t.Fatalf("combineResolverConfigs() = %q, want unchanged shared DNS", got)
	}
}

func TestHostIPv4NameserversIgnoresIPv6(t *testing.T) {
	got := hostIPv4Nameservers("# resolver config\nnameserver 2001:db8::53\nnameserver 192.0.2.53\nnameserver 192.0.2.53\n")
	if len(got) != 1 || got[0].String() != "192.0.2.53" {
		t.Fatalf("hostIPv4Nameservers() = %v, want only deduplicated IPv4 nameserver", got)
	}
}

func TestDirectResolverRejectsIPv6Override(t *testing.T) {
	selection := preflight.BackendSelection{
		Mode:       profile.NetworkModeDirect,
		DNSAddress: "2001:db8::53",
		UseHostDNS: false,
	}
	for _, backend := range []preflight.BackendKind{preflight.BackendPasta, preflight.BackendSlirp4netns} {
		if _, _, err := directResolver(selection, backend); err == nil || !strings.Contains(err.Error(), "IPv6 is disabled") {
			t.Errorf("directResolver(%q) error = %v, want IPv6-disabled error", backend, err)
		}
	}
}
