//go:build linux

package sandbox

import (
	"strings"
	"testing"
)

func TestBuildProxyIngressFilterRulesDropsUnmarkedEgress(t *testing.T) {
	rules := buildProxyIngressFilterRules([]string{"8123", "9000-9002"}, []string{"8123"})
	for _, expected := range []string{
		"chain input { type filter hook input priority -150; policy drop;",
		"iifname \"isoboxp0\" tcp dport { 8123, 9000-9002 } ct state new ct mark set 0x49534f42 accept",
		"iifname \"isoboxp0\" udp dport { 8123 } ct state new ct mark set 0x49534f42 accept",
		"chain output_route { type route hook output priority -151; policy accept;",
		"ct mark 0x49534f42 meta mark set ct mark",
		"oifname \"isobox0\" accept",
		"oifname \"isoboxp0\" ct mark 0x49534f42 ct state established accept",
		"chain output { type filter hook output priority -150; policy drop;",
		"chain forward { type filter hook forward priority -150; policy drop; }",
	} {
		if !strings.Contains(rules, expected) {
			t.Errorf("proxy ingress rules omit %q:\n%s", expected, rules)
		}
	}
}

func TestParseProxyTUNHelperArgs(t *testing.T) {
	plain := []string{internalProxyTUNCommand, "--gate-path", internalProxyTUNGatePath, "--", "/usr/bin/true", "arg"}
	ingress := []string{internalProxyTUNCommand, "--ingress-guard", "/tmp/.isobox-nft", "8123", "-", "--gate-path", internalProxyTUNGatePath, "--", "/usr/bin/true", "arg"}

	guard, nft, tcp, udp, appIndex, ok := parseProxyTUNHelperArgs(plain)
	if !ok || guard || nft != "" || appIndex != 4 || plain[appIndex] != "/usr/bin/true" {
		t.Fatalf("plain helper args = (%t, %q, %v, %v, %d, %t)", guard, nft, tcp, udp, appIndex, ok)
	}
	guard, nft, tcp, udp, appIndex, ok = parseProxyTUNHelperArgs(ingress)
	if !ok || !guard || nft != "/tmp/.isobox-nft" || strings.Join(tcp, ",") != "8123" || len(udp) != 0 || appIndex != 8 || ingress[appIndex] != "/usr/bin/true" {
		t.Fatalf("ingress helper args = (%t, %q, %v, %v, %d, %t)", guard, nft, tcp, udp, appIndex, ok)
	}
	invalid := append([]string(nil), ingress...)
	invalid[3] = ""
	if _, _, _, _, _, ok := parseProxyTUNHelperArgs(invalid); ok {
		t.Fatal("helper accepted an empty ingress port set")
	}
}
