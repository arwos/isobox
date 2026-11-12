package profile

import (
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strings"

	"go.osspkg.com/unic"
)

const maxConfigSize = 1 << 20

// EffectiveProfile combines profile permissions with merged settings defaults.
// Network values are copied into explicit fields so runtime builders never
// need to apply defaults themselves.
type EffectiveProfile struct {
	Profile
	NetworkMode NetworkMode
	DNSServer   string
}

// DecodeProfile parses and validates a UNIC application profile.
func DecodeProfile(data []byte) (Profile, error) {
	if len(data) > maxConfigSize {
		return Profile{}, fmt.Errorf("profile: file exceeds %d bytes", maxConfigSize)
	}
	if err := rejectUnknownFields(data, profileType(), "profile"); err != nil {
		return Profile{}, err
	}
	profile := Profile{Version: SchemaVersion}
	if err := unic.Unmarshal(data, &profile); err != nil {
		return Profile{}, fmt.Errorf("profile: %w", err)
	}
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// DecodeSettings parses and validates global isobox settings.
func DecodeSettings(data []byte) (Settings, error) {
	return ApplySettingsOverride(DefaultSettings(), data)
}

// ApplySettingsOverride applies one partial settings document over an
// existing settings layer. Fields omitted from the document keep their base
// values, allowing user settings to override only selected system defaults.
func ApplySettingsOverride(base Settings, data []byte) (Settings, error) {
	if len(data) > maxConfigSize {
		return Settings{}, fmt.Errorf("settings: file exceeds %d bytes", maxConfigSize)
	}
	if err := ValidateSettings(base); err != nil {
		return Settings{}, fmt.Errorf("settings base: %w", err)
	}
	if err := rejectUnknownFields(data, settingsOverrideType(), "settings"); err != nil {
		return Settings{}, err
	}
	override := settingsOverride{
		Defaults: settingsDefaultOverride{
			NetworkMode: unsetNetworkMode,
			DNS:         unsetDNS,
		},
	}
	if err := unic.Unmarshal(data, &override); err != nil {
		return Settings{}, fmt.Errorf("settings: %w", err)
	}
	if override.Version != SchemaVersion {
		return Settings{}, fmt.Errorf("settings.version: unsupported version %d; use %d", override.Version, SchemaVersion)
	}
	if override.Defaults.NetworkMode != unsetNetworkMode {
		base.Defaults.NetworkMode = override.Defaults.NetworkMode
	}
	if override.Defaults.DNS != unsetDNS {
		base.Defaults.DNS = override.Defaults.DNS
	}
	if err := ValidateSettings(base); err != nil {
		return Settings{}, err
	}
	return base, nil
}

// ValidateProfile checks all profile-only invariants. It does not inspect the
// host filesystem; the launch preflight is responsible for checking that mount
// sources exist and are usable at the time the profile is run.
func ValidateProfile(profile Profile) error {
	if profile.Version != SchemaVersion {
		return fmt.Errorf("profile.version: unsupported version %d; use %d", profile.Version, SchemaVersion)
	}
	if profile.HomePath != "" {
		if err := validateAbsolutePath("profile.home_path", profile.HomePath); err != nil {
			return err
		}
		if profile.HomePath == string(filepath.Separator) {
			return fmt.Errorf("profile.home_path: root cannot be used as an application HOME")
		}
	}

	targets := make(map[string]int, len(profile.Mounts))
	for index, mount := range profile.Mounts {
		prefix := fmt.Sprintf("profile.mount[%d]", index)
		if err := validateAbsolutePath(prefix+".source", mount.Source); err != nil {
			return err
		}
		if err := validateAbsolutePath(prefix+".target", mount.Target); err != nil {
			return err
		}
		if mount.Source == string(filepath.Separator) {
			return fmt.Errorf("%s.source: root cannot be mounted; choose a narrower host directory", prefix)
		}
		if mount.Target == string(filepath.Separator) {
			return fmt.Errorf("%s.target: root cannot be replaced by a profile mount", prefix)
		}
		if previous, exists := targets[mount.Target]; exists {
			return fmt.Errorf("%s.target: duplicates profile.mount[%d].target; choose a unique container path", prefix, previous)
		}
		targets[mount.Target] = index
	}

	if !validNetworkMode(profile.Network.Mode, true) {
		return fmt.Errorf("profile.network.mode: invalid value %q; use off, direct, or proxy", profile.Network.Mode)
	}
	if profile.Network.DNS != "" {
		if err := validateDNSAddress("profile.network.dns", profile.Network.DNS); err != nil {
			return err
		}
	}
	if profile.Network.ProxyAlias != "" && !validAlias(profile.Network.ProxyAlias) {
		return fmt.Errorf("profile.network.proxy_alias: invalid alias %q; use 1-64 ASCII letters, digits, '.', '_' or '-' and start with a letter or digit", profile.Network.ProxyAlias)
	}
	if (profile.Network.Mode == NetworkModeOff || profile.Network.Mode == NetworkModeDirect) && profile.Network.ProxyAlias != "" {
		return fmt.Errorf("profile.network.proxy_alias: cannot be used with mode %q; remove the alias or select proxy mode", profile.Network.Mode)
	}
	if profile.Network.Mode == NetworkModeProxy && profile.Network.ProxyAlias == "" {
		return fmt.Errorf("profile.network.proxy_alias: required when profile.network.mode is proxy")
	}
	if err := validatePortRules(profile.Network.Ports); err != nil {
		return err
	}
	return nil
}

// ValidateSettings checks global defaults before they can be combined with a
// profile.
func ValidateSettings(settings Settings) error {
	if settings.Version != SchemaVersion {
		return fmt.Errorf("settings.version: unsupported version %d; use %d", settings.Version, SchemaVersion)
	}
	if !validNetworkMode(settings.Defaults.NetworkMode, false) {
		return fmt.Errorf("settings.defaults.network_mode: invalid value %q; use off, direct, or proxy", settings.Defaults.NetworkMode)
	}
	if settings.Defaults.DNS != "" {
		if err := validateDNSAddress("settings.defaults.dns", settings.Defaults.DNS); err != nil {
			return err
		}
	}
	return nil
}

// ResolveProfile applies settings defaults and enforces requirements that
// depend on the final network mode.
func ResolveProfile(profile Profile, settings Settings) (EffectiveProfile, error) {
	if err := ValidateProfile(profile); err != nil {
		return EffectiveProfile{}, err
	}
	if err := ValidateSettings(settings); err != nil {
		return EffectiveProfile{}, err
	}

	mode := profile.Network.Mode
	if mode == "" {
		mode = settings.Defaults.NetworkMode
	}
	dns := profile.Network.DNS
	if dns == "" {
		dns = settings.Defaults.DNS
	}
	if mode == NetworkModeProxy {
		if profile.Network.ProxyAlias == "" {
			return EffectiveProfile{}, fmt.Errorf("profile.network.proxy_alias: required because the effective network mode is proxy")
		}
		if dns == "" {
			return EffectiveProfile{}, fmt.Errorf("network.dns: required in proxy mode; set profile.network.dns or settings.defaults.dns so DNS cannot fall back to the host")
		}
	}
	return EffectiveProfile{
		Profile:     profile,
		NetworkMode: mode,
		DNSServer:   dns,
	}, nil
}

func profileType() reflect.Type { return reflect.TypeOf(Profile{}) }

func settingsOverrideType() reflect.Type { return reflect.TypeOf(settingsOverride{}) }

func validNetworkMode(mode NetworkMode, allowInherit bool) bool {
	if allowInherit && mode == "" {
		return true
	}
	switch mode {
	case NetworkModeOff, NetworkModeDirect, NetworkModeProxy:
		return true
	default:
		return false
	}
}

func validAlias(alias string) bool {
	if len(alias) == 0 || len(alias) > 64 {
		return false
	}
	for index, char := range alias {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if index > 0 {
			valid = valid || char == '.' || char == '_' || char == '-'
		}
		if !valid {
			return false
		}
	}
	return true
}

func validateAbsolutePath(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s: required; provide an absolute path", field)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s: path contains a NUL byte", field)
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s: must be an absolute host/container path", field)
	}
	if filepath.Clean(value) != value {
		return fmt.Errorf("%s: path must be normalized and must not contain '.' or '..' components", field)
	}
	return nil
}

func validateDNSAddress(field, value string) error {
	if strings.TrimSpace(value) != value || net.ParseIP(value) == nil {
		return fmt.Errorf("%s: must be an IPv4 or IPv6 address without a port", field)
	}
	return nil
}

func validatePortRules(ports []PortRule) error {
	parsed := make([]net.IP, len(ports))
	for index, rule := range ports {
		prefix := fmt.Sprintf("profile.network.port[%d]", index)
		if strings.TrimSpace(rule.HostAddress) != rule.HostAddress {
			return fmt.Errorf("%s.host_address: must be an IPv4 or IPv6 bind address", prefix)
		}
		parsed[index] = net.ParseIP(rule.HostAddress)
		if parsed[index] == nil {
			return fmt.Errorf("%s.host_address: must be an IPv4 or IPv6 bind address", prefix)
		}
		if rule.Port == 0 {
			return fmt.Errorf("%s.port: must be between 1 and 65535", prefix)
		}
		if rule.EndPort != 0 && rule.EndPort < rule.Port {
			return fmt.Errorf("%s.end_port: must be greater than or equal to port", prefix)
		}
		if rule.Protocol != PortProtocolBoth && rule.Protocol != PortProtocolTCP && rule.Protocol != PortProtocolUDP {
			return fmt.Errorf("%s.protocol: invalid value %q; use both, tcp, or udp", prefix, rule.Protocol)
		}
		for previous := 0; previous < index; previous++ {
			if sameBindAddress(parsed[index], parsed[previous]) && protocolsOverlap(rule.Protocol, ports[previous].Protocol) && rangesOverlap(rule, ports[previous]) {
				return fmt.Errorf("%s: overlaps profile.network.port[%d] on the same host address and protocol; combine or adjust the rules", prefix, previous)
			}
		}
	}
	return nil
}

func sameBindAddress(left, right net.IP) bool {
	left4, right4 := left.To4(), right.To4()
	if left4 != nil && right4 != nil {
		return left4.Equal(right4) || left4.IsUnspecified() || right4.IsUnspecified()
	}
	if left4 != nil || right4 != nil {
		return false
	}
	return left.Equal(right) || left.IsUnspecified() || right.IsUnspecified()
}

func protocolsOverlap(left, right PortProtocol) bool {
	return left == PortProtocolBoth || right == PortProtocolBoth || left == right
}

func rangesOverlap(left, right PortRule) bool {
	leftEnd := left.EndPort
	if leftEnd == 0 {
		leftEnd = left.Port
	}
	rightEnd := right.EndPort
	if rightEnd == 0 {
		rightEnd = right.Port
	}
	return left.Port <= rightEnd && right.Port <= leftEnd
}
