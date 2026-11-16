//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package proxy

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/arwos/isobox/internal/profile"
)

// TUNPolicy configures the proxy data plane. Egress mode uses host networking
// directly and applies Allow before every outbound dial.
type TUNPolicy struct {
	Mode                profile.NetworkMode
	AppID               string
	ProxyAlias          string
	ResolverEndpoint    string
	HostGatewayEndpoint string
	DNSAddress          string
	DNSAddresses        []string
	Allow               []profile.ProxyAllowRule
	Records             []profile.ResolverRecord
}

type compiledAllowRule struct {
	prefix   netip.Prefix
	ports    []compiledPortRange
	protocol profile.PortProtocol
}

type compiledPortRange struct {
	start uint16
	end   uint16
}

func compileAllowRules(rules []profile.ProxyAllowRule) ([]compiledAllowRule, error) {
	compiled := make([]compiledAllowRule, 0, len(rules))
	for index, rule := range rules {
		protocol := rule.Protocol
		if protocol == "" {
			protocol = profile.PortProtocolBoth
		}
		ports := make([]compiledPortRange, 0, len(rule.Ports)+len(rule.PortRanges))
		for _, port := range rule.Ports {
			ports = append(ports, compiledPortRange{start: port, end: port})
		}
		for _, portRange := range rule.PortRanges {
			ports = append(ports, compiledPortRange{start: portRange.Start, end: portRange.End})
		}
		for _, ip := range rule.IPs {
			prefix, err := netip.ParsePrefix(ip)
			if err != nil {
				address, parseErr := netip.ParseAddr(ip)
				if parseErr != nil || !address.Is4() {
					return nil, fmt.Errorf("network.proxy.allow[%d].ip: invalid IPv4 address or CIDR", index)
				}
				prefix = netip.PrefixFrom(address, 32)
			}
			if !prefix.Addr().Is4() {
				return nil, fmt.Errorf("network.proxy.allow[%d].ip: IPv6 is disabled", index)
			}
			compiled = append(compiled, compiledAllowRule{prefix: prefix.Masked(), ports: ports, protocol: protocol})
		}
	}
	return compiled, nil
}

func compileDNSRecords(records []profile.ResolverRecord) (map[string][]net.IP, error) {
	result := make(map[string][]net.IP, len(records))
	for index, record := range records {
		domain, err := profile.NormalizeDomain(record.Domain)
		if err != nil {
			return nil, fmt.Errorf("network.resolver.record[%d]: %w", index, err)
		}
		for _, value := range record.IPs {
			ip := net.ParseIP(value)
			if ip == nil || ip.To4() == nil || strings.Contains(value, ":") {
				return nil, fmt.Errorf("network.resolver.record[%d].ip: must be an IPv4 address", index)
			}
			for _, previous := range result[domain] {
				if previous.Equal(ip.To4()) {
					return nil, fmt.Errorf("network.resolver.record[%d]: duplicate domain and IP", index)
				}
			}
			result[domain] = append(result[domain], append(net.IP(nil), ip.To4()...))
		}
	}
	return result, nil
}

func (tunnel *Tunnel) destination(address net.IP, port uint16, protocol profile.PortProtocol) (string, error) {
	if address == nil || address.To4() == nil || port == 0 {
		return "", fmt.Errorf("TUN destination must be IPv4 with a nonzero port")
	}
	ip := address.To4()
	if ip.Equal(net.ParseIP(tunnel.resolverEndpoint()).To4()) && port == 53 {
		addresses := tunnel.configuredDNSAddresses()
		if len(addresses) == 0 {
			return "", fmt.Errorf("no upstream DNS configured")
		}
		return net.JoinHostPort(addresses[0].String(), "53"), nil
	}
	if ip.Equal(net.ParseIP(tunnel.resolverEndpoint()).To4()) {
		return "", fmt.Errorf("reserved resolver endpoint only accepts DNS on port 53")
	}
	if ip.Equal(net.ParseIP(tunnel.hostGatewayEndpoint()).To4()) {
		if tunnel.mode != profile.NetworkModeEgress || !tunnel.allowed(net.ParseIP("127.0.0.1"), port, protocol) {
			tunnel.logBlocked(net.ParseIP("127.0.0.1"), port, protocol, "host gateway destination is not allowed")
			return "", fmt.Errorf("host gateway access is not allowed")
		}
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), nil
	}
	if tunnel.enforceACL && !tunnel.allowed(ip, port, protocol) {
		tunnel.logBlocked(ip, port, protocol, "destination does not match an allow rule")
		return "", fmt.Errorf("destination %s:%d is not allowed", ip, port)
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(port))), nil
}

func (tunnel *Tunnel) resolverEndpoint() string {
	if tunnel != nil && tunnel.dnsEndpoint != "" {
		return tunnel.dnsEndpoint
	}
	return TransparentDNSAddress
}

func (tunnel *Tunnel) hostGatewayEndpoint() string {
	if tunnel != nil && tunnel.gatewayEndpoint != "" {
		return tunnel.gatewayEndpoint
	}
	return "10.89.0.3"
}

func (tunnel *Tunnel) configuredDNSAddresses() []net.IP {
	if len(tunnel.dnsAddresses) != 0 {
		return tunnel.dnsAddresses
	}
	if tunnel.dnsAddress == "" {
		return nil
	}
	address := net.ParseIP(tunnel.dnsAddress)
	if address == nil || address.To4() == nil {
		return nil
	}
	return []net.IP{address.To4()}
}

func (tunnel *Tunnel) logBlocked(ip net.IP, port uint16, protocol profile.PortProtocol, reason string) {
	if tunnel != nil && tunnel.mode == profile.NetworkModeEgress {
		tunnel.eventLog.logf("egress blocked destination=%s:%d protocol=%s reason=%q", ip, port, protocol, reason)
	}
}

func (tunnel *Tunnel) allowed(ip net.IP, port uint16, protocol profile.PortProtocol) bool {
	ipv4 := ip.To4()
	if ipv4 == nil {
		return false
	}
	address, ok := netip.AddrFromSlice(ipv4)
	if !ok || !address.Is4() {
		return false
	}
	for _, rule := range tunnel.allowRules {
		if !rule.prefix.Contains(address) || !rule.allowsPort(port) {
			continue
		}
		if rule.protocol == profile.PortProtocolBoth || rule.protocol == protocol {
			return true
		}
	}
	return false
}

func (rule compiledAllowRule) allowsPort(port uint16) bool {
	if len(rule.ports) == 0 {
		return true
	}
	for _, allowed := range rule.ports {
		if port >= allowed.start && port <= allowed.end {
			return true
		}
	}
	return false
}

func (tunnel *Tunnel) dialContext(ctx context.Context, network, destination string) (net.Conn, error) {
	if tunnel.client != nil {
		return tunnel.client.DialContext(ctx, network, destination)
	}
	return (&net.Dialer{}).DialContext(ctx, network, destination)
}
