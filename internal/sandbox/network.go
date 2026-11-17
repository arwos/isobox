/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"net/netip"
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
	SharedNamespace     bool
	SharedTunnel        bool
	BackendNamespacePID int
	HostUID             int
	HostGID             int
	HasHostIdentity     bool
	InterfaceName       string
	Subnet              string
	GroupSubnets        []string
	GuestAddress        string
	GatewayAddress      string
	ResolverEndpoint    string
	Backend             preflight.BackendKind
	PortForwarder       preflight.BackendKind
	Executable          string
	ForwarderExecutable string
	Resolver            []byte
	DNSAddresses        []string
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
func PrepareNetwork(selection preflight.BackendSelection, pastaPath, slirpPath string, configs ...profile.NetworkConfig) (NetworkRuntime, error) {
	var config profile.NetworkConfig
	if len(configs) != 0 {
		config = configs[0]
	}
	runtime := NetworkRuntime{
		Mode:                selection.Mode,
		Backend:             selection.Network,
		PortForwarder:       selection.PortForwarder,
		EnforceEgressFilter: selection.EnforceEgressFilter,
		ProxyAlias:          selection.ProxyAlias,
		ProxyAllowRules:     append([]profile.ProxyAllowRule(nil), selection.ProxyAllowRules...),
		ResolverRecords:     append([]profile.ResolverRecord(nil), selection.ResolverRecords...),
		DNSAddresses:        append([]string(nil), selectedDNSAddresses(selection)...),
		FallbackReason:      selection.FallbackReason,
		InterfaceName:       config.InterfaceName,
		Subnet:              config.Subnet,
	}
	backend := runtime.activeBackend()
	if backend == preflight.BackendProxyTUN {
		runtime.GatewayAddress = "10.123.123.3"
		runtime.ResolverEndpoint = proxy.TransparentDNSAddress
		if runtime.InterfaceName == "" {
			runtime.InterfaceName = proxyTUNName
		}
	} else if runtime.InterfaceName == "" {
		runtime.InterfaceName = "tap0"
	}
	if config.Subnet != "" {
		prefix, err := netip.ParsePrefix(config.Subnet)
		if err != nil || !prefix.Addr().Is4() {
			return NetworkRuntime{}, fmt.Errorf("invalid IPv4 sandbox subnet %q", config.Subnet)
		}
		prefix = prefix.Masked()
		runtime.Subnet = prefix.String()
		reserved := map[netip.Addr]struct{}{}
		guest, err := randomUsableIPv4(prefix, reserved)
		if err != nil {
			return NetworkRuntime{}, err
		}
		runtime.GuestAddress = guest.String()
		switch backend {
		case preflight.BackendProxyTUN:
			reserved[guest] = struct{}{}
			resolver, err := randomUsableIPv4(prefix, reserved)
			if err != nil {
				return NetworkRuntime{}, fmt.Errorf("choose sandbox resolver endpoint: %w", err)
			}
			runtime.ResolverEndpoint = resolver.String()
			reserved[resolver] = struct{}{}
			gateway, err := randomUsableIPv4(prefix, reserved)
			if err != nil {
				return NetworkRuntime{}, fmt.Errorf("choose host gateway endpoint: %w", err)
			}
			runtime.GatewayAddress = gateway.String()
		case preflight.BackendPasta:
			gateway, err := randomUsableIPv4(prefix, map[netip.Addr]struct{}{guest: {}})
			if err != nil {
				return NetworkRuntime{}, fmt.Errorf("choose sandbox gateway: %w", err)
			}
			runtime.GatewayAddress = gateway.String()
		}
	}
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
			resolver, dnsAddresses, err := proxyResolvers(selection, hostResolver, runtime.ResolverEndpoint)
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
				if addressText == runtime.ResolverEndpoint {
					return NetworkRuntime{}, fmt.Errorf("egress upstream DNS cannot point to the internal resolver endpoint")
				}
				runtime.ProxyDNSAddresses = append(runtime.ProxyDNSAddresses, addressText)
			}
			if len(runtime.ProxyDNSAddresses) != 0 {
				runtime.ProxyDNSAddress = runtime.ProxyDNSAddresses[0]
			}
			runtime.Resolver = []byte("nameserver " + runtime.ResolverEndpoint + "\n")
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
	runtime.DNSAddresses = resolverNameservers(runtime.Resolver)
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
		fallback.DNSAddresses = resolverNameservers(fallback.Resolver)
		runtime.Resolver = combineResolverConfigs(runtime.Resolver, fallback.Resolver)
		runtime.DNSAddresses = resolverNameservers(runtime.Resolver)
		runtime.Fallback = &fallback
	}
	return runtime, nil
}

func randomUsableIPv4(prefix netip.Prefix, reserved map[netip.Addr]struct{}) (netip.Addr, error) {
	prefix = prefix.Masked()
	base := prefix.Addr().As4()
	baseValue := new(big.Int).SetBytes(base[:])
	count := new(big.Int).Lsh(big.NewInt(1), uint(32-prefix.Bits()))
	count.Sub(count, big.NewInt(2)) // Exclude network and broadcast addresses.
	if count.Sign() <= 0 {
		return netip.Addr{}, fmt.Errorf("subnet %s has no usable guest IPv4 address", prefix)
	}
	first := new(big.Int).Add(baseValue, big.NewInt(1))
	for attempt := 0; attempt < 256; attempt++ {
		offset, err := rand.Int(rand.Reader, count)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("choose random guest IPv4 address: %w", err)
		}
		candidateValue := new(big.Int).Add(first, offset)
		bytes := candidateValue.FillBytes(make([]byte, 4))
		candidate := netip.AddrFrom4([4]byte{bytes[0], bytes[1], bytes[2], bytes[3]})
		if _, exists := reserved[candidate]; !exists {
			return candidate, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("subnet %s contains no unreserved guest IPv4 address", prefix)
}

func proxyResolvers(selection preflight.BackendSelection, hostResolver []byte, endpoints ...string) ([]byte, []string, error) {
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
		endpoint := proxy.TransparentDNSAddress
		if len(endpoints) != 0 && endpoints[0] != "" {
			endpoint = endpoints[0]
		}
		if addressText == endpoint {
			return nil, nil, fmt.Errorf("proxy mode DNS server cannot point to the internal resolver endpoint")
		}
		validated = append(validated, addressText)
	}
	endpoint := proxy.TransparentDNSAddress
	if len(endpoints) != 0 && endpoints[0] != "" {
		endpoint = endpoints[0]
	}
	return []byte("nameserver " + endpoint + "\n"), validated, nil
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
