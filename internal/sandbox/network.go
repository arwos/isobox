/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/profile"
	"github.com/arwos/isobox/internal/proxy"
)

// NetworkRuntime is the selected backend and the launch data shared by the
// bubblewrap builder and network-process supervisor.
type NetworkRuntime struct {
	Mode                profile.NetworkMode
	Backend             preflight.BackendKind
	PortForwarder       preflight.BackendKind
	Executable          string
	ForwarderExecutable string
	Resolver            []byte
	ProxyDNSAddress     string
	ProxyDNSAddresses   []string
	ProxyAlias          string
	ProxyAllowRules     []profile.ProxyAllowRule
	ResolverRecords     []profile.ResolverRecord
	EnforceEgressFilter bool
	PastaDNSArgs        []string
	FallbackReason      string
	// Fallback is a prevalidated direct backend attempted before bwrap's gate opens.
	Fallback *NetworkRuntime
}

func (runtime NetworkRuntime) activeBackend() preflight.BackendKind {
	if runtime.Backend != preflight.BackendNone {
		return runtime.Backend
	}
	return runtime.PortForwarder
}

// PrepareNetwork turns the preflight selection into the exact executable and
// resolver configuration used by bwrap and the network backend.
func PrepareNetwork(selection preflight.BackendSelection, pastaPath, slirpPath string) (NetworkRuntime, error) {
	runtime := NetworkRuntime{
		Mode:                selection.Mode,
		Backend:             selection.Network,
		PortForwarder:       selection.PortForwarder,
		EnforceEgressFilter: selection.EnforceEgressFilter,
		ProxyAlias:          selection.ProxyAlias,
		ProxyAllowRules:     append([]profile.ProxyAllowRule(nil), selection.ProxyAllowRules...),
		ResolverRecords:     append([]profile.ResolverRecord(nil), selection.ResolverRecords...),
		FallbackReason:      selection.FallbackReason,
	}
	backend := runtime.activeBackend()
	switch backend {
	case preflight.BackendNone:
		return runtime, nil
	case preflight.BackendPasta:
		runtime.Executable = pastaPath
	case preflight.BackendSlirp4netns:
		runtime.Executable = slirpPath
	case preflight.BackendProxyTUN:
		if runtime.Mode != profile.NetworkModeProxy && runtime.Mode != profile.NetworkModeEgress {
			return NetworkRuntime{}, fmt.Errorf("proxy TUN backend cannot serve network mode %q", runtime.Mode)
		}
		if runtime.Mode == profile.NetworkModeProxy {
			if runtime.ProxyAlias == "" {
				return NetworkRuntime{}, fmt.Errorf("proxy network mode requires a configured proxy alias")
			}
			var hostResolver []byte
			if selection.UseHostDNS || len(selectedDNSAddresses(selection)) == 0 {
				var readErr error
				hostResolver, readErr = os.ReadFile("/etc/resolv.conf")
				if readErr != nil {
					return NetworkRuntime{}, fmt.Errorf("read host DNS configuration for proxy mode: %w", readErr)
				}
			}
			resolver, dnsAddresses, err := proxyResolvers(selection, hostResolver)
			if err != nil {
				return NetworkRuntime{}, err
			}
			runtime.Resolver = resolver
			runtime.ProxyDNSAddresses = dnsAddresses
			if len(dnsAddresses) != 0 {
				runtime.ProxyDNSAddress = dnsAddresses[0]
			}
		} else {
			if runtime.ProxyAlias != "" {
				return NetworkRuntime{}, fmt.Errorf("egress mode cannot use a remote SOCKS5 alias")
			}
			for _, value := range selectedDNSAddresses(selection) {
				address := net.ParseIP(value)
				if address == nil || address.To4() == nil || strings.Contains(value, ":") {
					return NetworkRuntime{}, fmt.Errorf("egress upstream DNS %q is not an IPv4 address", value)
				}
				addressText := address.To4().String()
				if addressText == proxy.TransparentDNSAddress {
					return NetworkRuntime{}, fmt.Errorf("egress upstream DNS cannot point to the internal resolver endpoint")
				}
				runtime.ProxyDNSAddresses = append(runtime.ProxyDNSAddresses, addressText)
			}
			if len(runtime.ProxyDNSAddresses) != 0 {
				runtime.ProxyDNSAddress = runtime.ProxyDNSAddresses[0]
			}
			runtime.Resolver = []byte("nameserver " + proxy.TransparentDNSAddress + "\n")
		}
		if selection.PortForwarder == preflight.BackendPasta {
			runtime.ForwarderExecutable = pastaPath
			if runtime.ForwarderExecutable == "" {
				return NetworkRuntime{}, fmt.Errorf("selected proxy ingress forwarder pasta has no executable")
			}
		}
		return runtime, nil
	default:
		return NetworkRuntime{}, fmt.Errorf("unsupported selected network backend %q", backend)
	}
	if runtime.Executable == "" {
		return NetworkRuntime{}, fmt.Errorf("selected network backend %q has no executable", backend)
	}
	if runtime.Mode != profile.NetworkModeDirect {
		return runtime, nil
	}

	resolver, pastaArgs, err := directResolver(selection, backend)
	if err != nil {
		return NetworkRuntime{}, err
	}
	runtime.Resolver = []byte(resolver)
	runtime.PastaDNSArgs = pastaArgs
	if selection.FallbackNetwork != "" && selection.FallbackNetwork != preflight.BackendNone {
		fallback := NetworkRuntime{
			Mode:          selection.Mode,
			Backend:       selection.FallbackNetwork,
			PortForwarder: selection.FallbackNetwork,
		}
		switch selection.FallbackNetwork {
		case preflight.BackendPasta:
			fallback.Executable = pastaPath
		case preflight.BackendSlirp4netns:
			fallback.Executable = slirpPath
		default:
			return NetworkRuntime{}, fmt.Errorf("unsupported fallback network backend %q", selection.FallbackNetwork)
		}
		if fallback.Executable == "" {
			return NetworkRuntime{}, fmt.Errorf("selected fallback network backend %q has no executable", selection.FallbackNetwork)
		}
		fallbackResolver, _, err := directResolver(selection, fallback.activeBackend())
		if err != nil {
			return NetworkRuntime{}, err
		}
		fallback.Resolver = []byte(fallbackResolver)
		runtime.Resolver = combineResolverConfigs(runtime.Resolver, fallback.Resolver)
		runtime.Fallback = &fallback
	}
	return runtime, nil
}

func proxyResolver(selection preflight.BackendSelection, hostResolver []byte) ([]byte, string, error) {
	resolver, addresses, err := proxyResolvers(selection, hostResolver)
	if err != nil {
		return nil, "", err
	}
	if len(addresses) == 0 {
		return resolver, "", nil
	}
	return resolver, addresses[0], nil
}

func proxyResolvers(selection preflight.BackendSelection, hostResolver []byte) ([]byte, []string, error) {
	addresses := selectedDNSAddresses(selection)
	if selection.UseHostDNS || len(addresses) == 0 {
		nameservers := hostIPv4Nameservers(string(hostResolver))
		if len(nameservers) == 0 {
			return nil, nil, fmt.Errorf("host DNS configuration contains no usable IPv4 nameserver for proxy mode; set profile.network.dns or settings.defaults.dns")
		}
		addresses = []string{nameservers[0].String()}
	}
	validated := make([]string, 0, len(addresses))
	for _, addressText := range addresses {
		address := net.ParseIP(addressText)
		if address == nil || address.To4() == nil || strings.Contains(addressText, ":") {
			return nil, nil, fmt.Errorf("proxy mode DNS server %q is not IPv4; IPv6 is disabled", addressText)
		}
		addressText = address.To4().String()
		if addressText == proxy.TransparentDNSAddress {
			return nil, nil, fmt.Errorf("proxy mode DNS server cannot point to the internal resolver endpoint")
		}
		validated = append(validated, addressText)
	}
	return []byte("nameserver " + proxy.TransparentDNSAddress + "\n"), validated, nil
}

func selectedDNSAddresses(selection preflight.BackendSelection) []string {
	if len(selection.DNSAddresses) != 0 {
		return selection.DNSAddresses
	}
	if selection.DNSAddress != "" {
		return []string{selection.DNSAddress}
	}
	return nil
}

func combineResolverConfigs(primary, fallback []byte) []byte {
	primaryServers := resolverNameservers(primary)
	fallbackServers := resolverNameservers(fallback)
	seen := make(map[string]struct{}, len(primaryServers)+len(fallbackServers))
	servers := make([]string, 0, 3)
	fallbackAdded := false
	for _, address := range primaryServers {
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		servers = append(servers, address)
	}
	for _, address := range fallbackServers {
		if _, exists := seen[address]; exists {
			continue
		}
		if len(servers) == 3 {
			servers = servers[:2]
		}
		seen[address] = struct{}{}
		servers = append(servers, address)
		fallbackAdded = true
	}
	if !fallbackAdded {
		return primary
	}
	var config strings.Builder
	for _, address := range servers {
		config.WriteString("nameserver ")
		config.WriteString(address)
		config.WriteByte('\n')
	}
	if len(servers) > 1 {
		config.WriteString("options timeout:1 attempts:1\n")
	}
	return []byte(config.String())
}

func resolverNameservers(config []byte) []string {
	var addresses []string
	for _, line := range strings.Split(string(config), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" {
			addresses = append(addresses, fields[1])
		}
	}
	return addresses
}

func directResolver(selection preflight.BackendSelection, backend preflight.BackendKind) (string, []string, error) {
	addresses := selectedDNSAddresses(selection)
	for _, value := range addresses {
		address := net.ParseIP(value)
		if address == nil || address.To4() == nil || strings.Contains(value, ":") {
			return "", nil, fmt.Errorf("configured DNS address %q is not IPv4; IPv6 is disabled", value)
		}
	}
	if backend == preflight.BackendSlirp4netns {
		if selection.UseHostDNS || len(addresses) == 0 {
			addresses = []string{"10.0.2.3"}
		}
		return nameserverConfig(addresses), nil, nil
	}
	if backend != preflight.BackendPasta {
		return "", nil, fmt.Errorf("no direct resolver implementation exists for backend %q", backend)
	}

	var nameservers []net.IP
	if selection.UseHostDNS || len(addresses) == 0 {
		contents, err := os.ReadFile("/etc/resolv.conf")
		if err != nil {
			return "", nil, fmt.Errorf("read host DNS configuration: %w", err)
		}
		nameservers = hostIPv4Nameservers(string(contents))
	} else {
		for _, value := range addresses {
			address := net.ParseIP(value)
			if address == nil || address.To4() == nil || strings.Contains(value, ":") {
				return "", nil, fmt.Errorf("configured DNS address %q is not IPv4; IPv6 is disabled", value)
			}
			nameservers = append(nameservers, address.To4())
		}
	}
	if len(nameservers) == 0 {
		return "", nil, fmt.Errorf("host DNS configuration contains no usable IPv4 nameserver; IPv6 is disabled")
	}

	lines := make([]string, 0, len(nameservers))
	var pastaArgs []string
	forwardedLoopback := map[string]bool{}
	loopbackResolver := ""
	for _, address := range nameservers {
		resolverAddress := address.String()
		if address.IsLoopback() {
			if loopbackResolver != "" && loopbackResolver != resolverAddress {
				return "", nil, fmt.Errorf("pasta DNS forwarding supports only one distinct loopback resolver")
			}
			loopbackResolver = resolverAddress
			family := "v4"
			if !forwardedLoopback[family] {
				forwardAddress := "169.254.1.1"
				pastaArgs = append(pastaArgs,
					"--dns-forward", forwardAddress,
					"--dns-host", resolverAddress,
				)
				forwardedLoopback[family] = true
			}
			resolverAddress = "169.254.1.1"
		}
		lines = append(lines, "nameserver "+resolverAddress)
	}
	return strings.Join(lines, "\n") + "\n", pastaArgs, nil
}

func nameserverConfig(addresses []string) string {
	var config strings.Builder
	for _, address := range addresses {
		config.WriteString("nameserver ")
		config.WriteString(address)
		config.WriteByte('\n')
	}
	return config.String()
}

func hostIPv4Nameservers(contents string) []net.IP {
	var addresses []net.IP
	for _, line := range strings.Split(contents, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		address := net.ParseIP(fields[1])
		if address == nil || address.To4() == nil || strings.Contains(fields[1], ":") {
			continue
		}
		addresses = appendUniqueIP(addresses, address.To4())
	}
	return addresses
}

func appendUniqueIP(addresses []net.IP, candidate net.IP) []net.IP {
	for _, address := range addresses {
		if address.Equal(candidate) {
			return addresses
		}
	}
	return append(addresses, candidate)
}
