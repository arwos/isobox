package preflight

import (
	"fmt"
	"net"
	"strings"

	"github.com/arwos/isobox/internal/profile"
)

// BackendKind identifies the user-space data plane selected for a profile.
type BackendKind string

const (
	BackendNone        BackendKind = "none"
	BackendPasta       BackendKind = "pasta"
	BackendSlirp4netns BackendKind = "slirp4netns"
	BackendProxyTUN    BackendKind = "proxy-tun"
)

// BackendSelection is the validated network plan consumed by the runtime
// builder. Host forwards are explicit; a backend must never add automatic
// forwarding behind the profile's back.
type BackendSelection struct {
	Mode                       profile.NetworkMode
	Network                    BackendKind
	PortForwarder              BackendKind
	HostForwards               []profile.PortRule
	DisableAutomaticForwarding bool
	EnforceEgressFilter        bool
	EnableIPv6                 bool
	DNSAddress                 string
	UseHostDNS                 bool
	FallbackReason             string
}

// SelectBackend chooses pasta first and slirp4netns only when pasta is missing
// or lacks required options. It rejects configurations for which the available
// helper cannot preserve the profile's network policy.
func SelectBackend(config profile.EffectiveProfile, capabilities Capabilities) (BackendSelection, error) {
	selection := BackendSelection{
		Mode:         config.NetworkMode,
		DNSAddress:   config.DNSServer,
		UseHostDNS:   config.DNSServer == "",
		HostForwards: append([]profile.PortRule(nil), config.Network.Ports...),
	}

	switch config.NetworkMode {
	case profile.NetworkModeOff:
		if len(config.Network.Ports) == 0 {
			selection.Network = BackendNone
			selection.PortForwarder = BackendNone
			return selection, nil
		}
		if capabilities.Pasta == "" {
			return BackendSelection{}, fmt.Errorf("network mode off with published ports currently requires pasta's explicit forwarding path; install passt (pasta) and retry")
		}
		if capabilities.PastaInspectionError != "" {
			return BackendSelection{}, fmt.Errorf("cannot verify pasta's off-mode forwarding options: %s", capabilities.PastaInspectionError)
		}
		if !hasAllOptions(capabilities.PastaOptions, offPastaRequiredOptions(config.Network.Ports)...) {
			return BackendSelection{}, fmt.Errorf("installed pasta lacks options required to enforce off-mode egress and explicit forwarding; update passt and retry")
		}
		if err := validateHostForwards(config.Network.Ports, false); err != nil {
			return BackendSelection{}, err
		}
		selection.Network = BackendNone
		selection.PortForwarder = BackendPasta
		selection.DisableAutomaticForwarding = true
		selection.EnforceEgressFilter = true
		selection.EnableIPv6 = true
		return selection, nil
	case profile.NetworkModeProxy:
		if len(config.Network.Ports) != 0 {
			return BackendSelection{}, fmt.Errorf("proxy mode with published ports needs the F05/F06 ingress path to be combined with the SOCKS TUN; that path is not available yet, so isobox refuses to start")
		}
		selection.Network = BackendProxyTUN
		selection.PortForwarder = BackendNone
		selection.EnableIPv6 = true
		return selection, nil
	case profile.NetworkModeDirect:
		// Continue below and select a direct network backend.
	default:
		return BackendSelection{}, fmt.Errorf("network mode %q has no backend plan", config.NetworkMode)
	}

	pastaSupported := capabilities.Pasta != "" && capabilities.PastaInspectionError == "" && hasAllOptions(capabilities.PastaOptions,
		"--config-net", "--tcp-ports", "--udp-ports", "--tcp-ns", "--udp-ns",
		"--quiet", "--foreground", "--pid", "--dns-forward", "--dns-host")
	if pastaSupported {
		if err := validateHostForwards(config.Network.Ports, false); err != nil {
			return BackendSelection{}, err
		}
		selection.Network = BackendPasta
		selection.PortForwarder = BackendPasta
		selection.DisableAutomaticForwarding = true
		selection.EnableIPv6 = true
		return selection, nil
	}

	slirpSupported := capabilities.Slirp4netns != "" && capabilities.SlirpInspectionError == "" && hasAllOptions(capabilities.Slirp4netnsOptions,
		"--configure", "--enable-ipv6", "--ready-fd", "--exit-fd")
	if len(config.Network.Ports) != 0 {
		slirpSupported = slirpSupported && hasAllOptions(capabilities.Slirp4netnsOptions, "--api-socket")
	}
	if slirpSupported {
		if err := validateHostForwards(config.Network.Ports, true); err != nil {
			return BackendSelection{}, fmt.Errorf("pasta is unavailable and slirp4netns cannot preserve the profile: %w", err)
		}
		selection.Network = BackendSlirp4netns
		selection.PortForwarder = BackendSlirp4netns
		selection.DisableAutomaticForwarding = true
		selection.EnableIPv6 = true
		if capabilities.Pasta != "" && capabilities.PastaInspectionError != "" {
			selection.FallbackReason = "pasta capabilities could not be inspected: " + capabilities.PastaInspectionError
		} else if capabilities.Pasta != "" {
			selection.FallbackReason = "pasta lacks options required to preserve this profile"
		} else {
			selection.FallbackReason = "pasta was not found; slirp4netns supports this profile"
		}
		return selection, nil
	}

	var reasons []string
	if capabilities.Pasta == "" {
		reasons = append(reasons, "pasta was not found")
	} else if capabilities.PastaInspectionError != "" {
		reasons = append(reasons, "pasta capabilities could not be inspected: "+capabilities.PastaInspectionError)
	} else if !hasAllOptions(capabilities.PastaOptions,
		"--config-net", "--tcp-ports", "--udp-ports", "--tcp-ns", "--udp-ns",
		"--quiet", "--foreground", "--pid", "--dns-forward", "--dns-host") {
		reasons = append(reasons, "pasta lacks required network, lifecycle, or DNS-forwarding options")
	}
	if capabilities.Slirp4netns == "" {
		reasons = append(reasons, "slirp4netns was not found")
	} else if capabilities.SlirpInspectionError != "" {
		reasons = append(reasons, "slirp4netns capabilities could not be inspected: "+capabilities.SlirpInspectionError)
	} else if !hasAllOptions(capabilities.Slirp4netnsOptions, "--configure", "--enable-ipv6", "--ready-fd", "--exit-fd") {
		reasons = append(reasons, "slirp4netns lacks required IPv6 or lifecycle options")
	} else if len(config.Network.Ports) != 0 && !hasAllOptions(capabilities.Slirp4netnsOptions, "--api-socket") {
		reasons = append(reasons, "slirp4netns lacks the explicit host-forward API")
	}
	return BackendSelection{}, fmt.Errorf("no available network backend can preserve this direct profile: %s", strings.Join(reasons, "; "))
}

func offPastaRequiredOptions(rules []profile.PortRule) []string {
	required := []string{
		"--config-net", "--tcp-ports", "--udp-ports", "--tcp-ns", "--udp-ns",
		"--no-splice", "--no-icmp", "--no-dhcp", "--no-dhcpv6", "--no-ndp", "--no-ra", "--pid",
		"--quiet", "--foreground",
	}
	hasTCP, hasUDP := false, false
	for _, rule := range rules {
		switch rule.Protocol {
		case profile.PortProtocolTCP:
			hasTCP = true
		case profile.PortProtocolUDP:
			hasUDP = true
		default:
			hasTCP, hasUDP = true, true
		}
	}
	if !hasTCP {
		required = append(required, "--no-tcp")
	}
	if !hasUDP {
		required = append(required, "--no-udp")
	}
	return required
}

func hasAllOptions(available map[string]bool, required ...string) bool {
	for _, option := range required {
		if !available[option] {
			return false
		}
	}
	return true
}

func validateHostForwards(rules []profile.PortRule, ipv4Only bool) error {
	for index, rule := range rules {
		address := net.ParseIP(rule.HostAddress)
		if address == nil {
			return fmt.Errorf("profile.network.port[%d].host_address is not an IP address", index)
		}
		if ipv4Only && address.To4() == nil {
			return fmt.Errorf("profile.network.port[%d].host_address %q is IPv6; slirp4netns host-forward API supports IPv4 only", index, rule.HostAddress)
		}
	}
	return nil
}
