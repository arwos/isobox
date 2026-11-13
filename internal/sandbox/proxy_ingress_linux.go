//go:build linux

package sandbox

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"
)

const (
	proxyIngressName          = "isoboxp0"
	proxyIngressAddress       = "10.89.1.2/24"
	proxyIngressGateway       = "10.89.1.1"
	proxyIngressSubnet        = "10.89.1.0/24"
	proxyIngressTable         = 18771
	proxyIngressRulePriority  = 101
	proxyIngressConnectionTag = uint32(0x49534f42)
)

func installProxyIngressFilter(nftPath string, tcpPorts, udpPorts []string) error {
	if nftPath == "" || !strings.HasPrefix(nftPath, "/") {
		return fmt.Errorf("an absolute nftables executable path is required")
	}
	if len(tcpPorts) == 0 && len(udpPorts) == 0 {
		return fmt.Errorf("proxy ingress filter requires at least one announced TCP or UDP port")
	}
	command := exec.Command(nftPath, "-f", "-")
	command.Stdin = bytes.NewBufferString(buildProxyIngressFilterRules(tcpPorts, udpPorts))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft -f - failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func buildProxyIngressFilterRules(tcpPorts, udpPorts []string) string {
	var rules strings.Builder
	rules.WriteString("table inet isobox {\n")
	rules.WriteString(" chain input { type filter hook input priority -150; policy drop;\n")
	rules.WriteString("  iifname \"lo\" accept\n")
	rules.WriteString("  iifname \"" + proxyTUNName + "\" ct state established accept\n")
	rules.WriteString("  iifname \"" + proxyIngressName + "\" ct state established ct mark " + fmt.Sprintf("0x%x", proxyIngressConnectionTag) + " accept\n")
	if len(tcpPorts) != 0 {
		rules.WriteString("  iifname \"" + proxyIngressName + "\" tcp dport { " + strings.Join(tcpPorts, ", ") + " } ct state new ct mark set " + fmt.Sprintf("0x%x", proxyIngressConnectionTag) + " accept\n")
	}
	if len(udpPorts) != 0 {
		rules.WriteString("  iifname \"" + proxyIngressName + "\" udp dport { " + strings.Join(udpPorts, ", ") + " } ct state new ct mark set " + fmt.Sprintf("0x%x", proxyIngressConnectionTag) + " accept\n")
	}
	rules.WriteString(" }\n")
	rules.WriteString(" chain output_route { type route hook output priority -151; policy accept;\n")
	rules.WriteString("  ct mark " + fmt.Sprintf("0x%x", proxyIngressConnectionTag) + " meta mark set ct mark\n")
	rules.WriteString(" }\n")
	rules.WriteString(" chain output { type filter hook output priority -150; policy drop;\n")
	rules.WriteString("  oifname \"lo\" accept\n")
	rules.WriteString("  oifname \"" + proxyTUNName + "\" accept\n")
	rules.WriteString("  oifname \"" + proxyIngressName + "\" ct mark " + fmt.Sprintf("0x%x", proxyIngressConnectionTag) + " ct state established accept\n")
	rules.WriteString(" }\n")
	rules.WriteString(" chain forward { type filter hook forward priority -150; policy drop; }\n")
	rules.WriteString("}\n")
	return rules.String()
}

// configureProxyIngressRouting keeps the SOCKS TUN as the only main-table
// default and installs a marked-only policy route over pasta for replies to
// host-initiated published-port connections.
func configureProxyIngressRouting() error {
	tun, err := netlink.LinkByName(proxyTUNName)
	if err != nil {
		return fmt.Errorf("find proxy TUN interface: %w", err)
	}
	ingress, err := netlink.LinkByName(proxyIngressName)
	if err != nil {
		return fmt.Errorf("find pasta ingress interface: %w", err)
	}
	if tun.Attrs().Index == ingress.Attrs().Index || tun.Attrs().Index <= 0 || ingress.Attrs().Index <= 0 {
		return fmt.Errorf("proxy TUN and pasta ingress interfaces are not distinct valid links")
	}
	if err := verifyProxyIngressAddress(ingress); err != nil {
		return err
	}

	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("inspect IPv4 routes after pasta setup: %w", err)
	}
	for _, route := range routes {
		if route.Dst == nil && route.LinkIndex == ingress.Attrs().Index {
			if err := netlink.RouteDel(&route); err != nil {
				return fmt.Errorf("remove pasta default route from the main table: %w", err)
			}
		}
	}
	if err := netlink.RouteReplace(&netlink.Route{
		LinkIndex: tun.Attrs().Index,
		Scope:     netlink.SCOPE_LINK,
		Table:     254,
		Family:    netlink.FAMILY_V4,
	}); err != nil {
		return fmt.Errorf("restore the proxy TUN as the main-table IPv4 default: %w", err)
	}
	routes, err = netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("verify the proxy TUN default route: %w", err)
	}
	defaultRoutes := 0
	for _, route := range routes {
		if route.Dst == nil {
			defaultRoutes++
			if route.LinkIndex != tun.Attrs().Index {
				return fmt.Errorf("an IPv4 default route bypasses the proxy TUN")
			}
		}
	}
	if defaultRoutes != 1 {
		return fmt.Errorf("expected exactly one main-table IPv4 default route through the proxy TUN, found %d", defaultRoutes)
	}

	_, subnet, err := net.ParseCIDR(proxyIngressSubnet)
	if err != nil {
		return fmt.Errorf("parse fixed pasta ingress subnet: %w", err)
	}
	if err := netlink.RouteReplace(&netlink.Route{
		LinkIndex: ingress.Attrs().Index,
		Dst:       subnet,
		Scope:     netlink.SCOPE_LINK,
		Table:     proxyIngressTable,
		Family:    netlink.FAMILY_V4,
	}); err != nil {
		return fmt.Errorf("install pasta subnet in the ingress policy table: %w", err)
	}
	if err := netlink.RouteReplace(&netlink.Route{
		LinkIndex: ingress.Attrs().Index,
		Dst:       nil,
		Gw:        net.ParseIP(proxyIngressGateway).To4(),
		Table:     proxyIngressTable,
		Family:    netlink.FAMILY_V4,
	}); err != nil {
		return fmt.Errorf("install marked-only pasta default route: %w", err)
	}
	mask := ^uint32(0)
	rule := netlink.NewRule()
	rule.Priority = proxyIngressRulePriority
	rule.Family = netlink.FAMILY_V4
	rule.Table = proxyIngressTable
	rule.Mark = proxyIngressConnectionTag
	rule.Mask = &mask
	if err := netlink.RuleAdd(rule); err != nil {
		return fmt.Errorf("install marked-only pasta routing rule: %w", err)
	}
	return verifyProxyIngressPolicy(tun, ingress)
}

func verifyProxyIngressAddress(link netlink.Link) error {
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("inspect pasta ingress IPv4 address: %w", err)
	}
	_, expected, err := net.ParseCIDR(proxyIngressAddress)
	if err != nil {
		return fmt.Errorf("parse fixed pasta ingress address: %w", err)
	}
	for _, address := range addresses {
		if address.IPNet != nil && address.IPNet.String() == expected.String() {
			return nil
		}
	}
	return fmt.Errorf("pasta ingress interface lacks expected address %s", proxyIngressAddress)
}

func verifyProxyIngressPolicy(tun, ingress netlink.Link) error {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: proxyIngressTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("verify pasta policy routes: %w", err)
	}
	hasSubnet, hasDefault := false, false
	for _, route := range routes {
		if route.LinkIndex != ingress.Attrs().Index {
			return fmt.Errorf("pasta policy table contains a route through an unexpected interface")
		}
		if route.Dst != nil && route.Dst.String() == proxyIngressSubnet {
			hasSubnet = true
		}
		if route.Dst == nil && route.Gw != nil && route.Gw.Equal(net.ParseIP(proxyIngressGateway)) {
			hasDefault = true
		}
	}
	if !hasSubnet || !hasDefault {
		return fmt.Errorf("pasta policy table does not contain the expected connected and default routes")
	}
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("verify marked-only pasta routing rule: %w", err)
	}
	mask := ^uint32(0)
	for _, rule := range rules {
		if rule.Priority == proxyIngressRulePriority && rule.Table == proxyIngressTable && rule.Mark == proxyIngressConnectionTag && rule.Mask != nil && *rule.Mask == mask {
			return nil
		}
	}
	return fmt.Errorf("marked-only pasta routing rule is missing or incomplete (TUN link %d, pasta link %d)", tun.Attrs().Index, ingress.Attrs().Index)
}
