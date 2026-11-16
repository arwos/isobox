/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package profile

import (
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"strconv"
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
	DNSServers  []string
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
	if strings.Contains(profile.Network.InterfaceName, "/") && profile.Network.Subnet == "" {
		profile.Network.Subnet = profile.Network.InterfaceName
		profile.Network.InterfaceName = ""
	}
	user, err := decodeAccount(profile.UserArgs, "profile.user", false)
	if err != nil {
		return Profile{}, err
	}
	profile.User = user
	profile.Groups, err = decodeAccounts(profile.GroupArgs, "profile.group")
	if err != nil {
		return Profile{}, err
	}
	profile.UserArgs = nil
	profile.GroupArgs = nil
	ports, err := decodePortValues(profile.Network.PortValues)
	if err != nil {
		return Profile{}, err
	}
	profile.Network.Ports = ports
	profile.Network.PortValues = nil
	if err := decodeProxyPortRanges(profile.Network.Proxy.Allow); err != nil {
		return Profile{}, err
	}
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func decodeAccounts(values []any, field string) ([]Account, error) {
	if len(values) == 0 {
		return nil, nil
	}
	entries, err := positionalEntries(values, field)
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(entries))
	for index, entry := range entries {
		account, err := decodeAccount(entry, fmt.Sprintf("%s[%d]", field, index), true)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

func decodeAccount(values []any, field string, required bool) (Account, error) {
	if len(values) == 0 && !required {
		return Account{}, nil
	}
	if len(values) != 2 {
		return Account{}, fmt.Errorf("%s: expected numeric ID and account name", field)
	}
	id, err := accountID(values[0])
	if err != nil {
		return Account{}, fmt.Errorf("%s: invalid ID: %w", field, err)
	}
	name, ok := values[1].(string)
	if !ok {
		return Account{}, fmt.Errorf("%s: account name must be a string", field)
	}
	return Account{ID: id, Name: name}, nil
}

func positionalEntries(values []any, field string) ([][]any, error) {
	if _, isTuple := values[0].([]any); isTuple {
		entries := make([][]any, 0, len(values))
		for _, value := range values {
			entry, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("%s: each entry must contain an ID and account name", field)
			}
			entries = append(entries, entry)
		}
		return entries, nil
	}
	return [][]any{values}, nil
}

func accountID(value any) (uint32, error) {
	var text string
	switch number := value.(type) {
	case int64:
		text = strconv.FormatInt(number, 10)
	case string:
		text = number
	default:
		return 0, fmt.Errorf("expected an integer from 0 to 4294967294")
	}
	parsed, err := strconv.ParseUint(text, 10, 32)
	if err != nil || parsed > uint64(^uint32(0))-1 {
		return 0, fmt.Errorf("expected an integer from 0 to 4294967294")
	}
	return uint32(parsed), nil
}

func decodeProxyPortRanges(rules []ProxyAllowRule) error {
	for ruleIndex := range rules {
		rule := &rules[ruleIndex]
		field := fmt.Sprintf("profile.network.proxy.allow[%d].port_range", ruleIndex)
		entries := make([][]any, 0, len(rule.PortRangeValues))
		if len(rule.PortRangeValues) > 0 {
			if _, isTuple := rule.PortRangeValues[0].([]any); isTuple {
				for _, value := range rule.PortRangeValues {
					entry, ok := value.([]any)
					if !ok {
						return fmt.Errorf("%s: each entry must contain start and end ports", field)
					}
					entries = append(entries, entry)
				}
			} else {
				entries = append(entries, rule.PortRangeValues)
			}
		}
		for rangeIndex, entry := range entries {
			rangeField := fmt.Sprintf("%s[%d]", field, rangeIndex)
			if len(entry) != 2 {
				return fmt.Errorf("%s: expected start and end ports", rangeField)
			}
			start, err := portNumber(entry[0])
			if err != nil {
				return fmt.Errorf("%s: invalid start port: %w", rangeField, err)
			}
			end, err := portNumber(entry[1])
			if err != nil {
				return fmt.Errorf("%s: invalid end port: %w", rangeField, err)
			}
			rule.PortRanges = append(rule.PortRanges, ProxyPortRange{Start: start, End: end})
		}
		rule.PortRangeValues = nil
	}
	return nil
}

func decodePortValues(values []any) ([]PortRule, error) {
	if len(values) == 0 {
		return nil, nil
	}

	entries := make([][]any, 0, len(values))
	if _, isTuple := values[0].([]any); isTuple {
		for _, value := range values {
			entry, ok := value.([]any)
			if !ok {
				return nil, fmt.Errorf("profile.network.port: each entry must contain host address, port, and protocol")
			}
			entries = append(entries, entry)
		}
	} else {
		entries = append(entries, values)
	}

	ports := make([]PortRule, 0, len(entries))
	for index, entry := range entries {
		field := fmt.Sprintf("profile.network.port[%d]", index)
		if len(entry) < 2 || len(entry) > 4 {
			return nil, fmt.Errorf("%s: expected host address, port, optional protocol, and optional end port", field)
		}
		hostAddress, ok := entry[0].(string)
		if !ok {
			return nil, fmt.Errorf("%s: host address must be a string", field)
		}
		port, err := portNumber(entry[1])
		if err != nil {
			return nil, fmt.Errorf("%s: invalid port: %w", field, err)
		}
		protocol := PortProtocolBoth
		if len(entry) >= 3 {
			protocolName, ok := entry[2].(string)
			if !ok {
				return nil, fmt.Errorf("%s: protocol must be tcp, udp, or both", field)
			}
			protocol = PortProtocol(protocolName)
		}
		rule := PortRule{
			HostAddress: hostAddress,
			Port:        port,
			Protocol:    protocol,
		}
		if len(entry) == 4 {
			rule.EndPort, err = portNumber(entry[3])
			if err != nil {
				return nil, fmt.Errorf("%s: invalid end port: %w", field, err)
			}
		}
		ports = append(ports, rule)
	}
	return ports, nil
}

func portNumber(value any) (uint16, error) {
	var text string
	switch number := value.(type) {
	case int64:
		text = strconv.FormatInt(number, 10)
	case string:
		text = number
	default:
		return 0, fmt.Errorf("expected an integer from 1 to 65535")
	}
	parsed, err := strconv.ParseUint(text, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("expected an integer from 1 to 65535")
	}
	return uint16(parsed), nil
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
	if err := validateEnvironment(profile.Env); err != nil {
		return err
	}
	if err := validateProfileAccounts(profile); err != nil {
		return err
	}
	if len(profile.Command) > 0 && profile.Command[0] == "" {
		return fmt.Errorf("profile.command[0]: executable cannot be empty")
	}
	for index, argument := range profile.Command {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("profile.command[%d]: argument contains a NUL byte", index)
		}
	}
	for index, entry := range profile.Path {
		field := fmt.Sprintf("profile.path[%d]", index)
		if err := validateAbsolutePath(field, entry); err != nil {
			return err
		}
		if strings.ContainsRune(entry, ':') {
			return fmt.Errorf("%s: path entry cannot contain ':'", field)
		}
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
		return fmt.Errorf("profile.network.mode: invalid value %q; use off, direct, proxy, or egress", profile.Network.Mode)
	}
	if profile.Network.InterfaceName != "" && !validInterfaceName(profile.Network.InterfaceName) {
		return fmt.Errorf("profile.network: invalid interface name %q; use 1-15 ASCII letters, digits, '.', '_' or '-'", profile.Network.InterfaceName)
	}
	if profile.Network.Subnet != "" {
		prefix, err := netip.ParsePrefix(profile.Network.Subnet)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() > 30 {
			return fmt.Errorf("profile.network: subnet %q must be an IPv4 CIDR with at least four addresses", profile.Network.Subnet)
		}
	}
	seenDNS := make(map[string]struct{}, len(profile.Network.DNS))
	for index, address := range profile.Network.DNS {
		field := fmt.Sprintf("profile.network.dns[%d]", index)
		if err := validateDNSAddress(field, address); err != nil {
			return err
		}
		if _, exists := seenDNS[address]; exists {
			return fmt.Errorf("%s: duplicate DNS server %q", field, address)
		}
		seenDNS[address] = struct{}{}
	}
	if profile.Network.ProxyAlias != "" && !IsValidProxyAlias(profile.Network.ProxyAlias) {
		return fmt.Errorf("profile.network.proxy_alias: invalid alias %q; use 1-64 ASCII letters, digits, '.', '_' or '-' and start with a letter or digit", profile.Network.ProxyAlias)
	}
	if profile.Network.Mode != "" && profile.Network.Mode != NetworkModeProxy && profile.Network.ProxyAlias != "" {
		return fmt.Errorf("profile.network.proxy_alias: cannot be used with mode %q; remove the alias or select proxy mode", profile.Network.Mode)
	}
	if profile.Network.Mode == NetworkModeProxy && profile.Network.ProxyAlias == "" {
		return fmt.Errorf("profile.network.proxy_alias: required when profile.network.mode is proxy")
	}
	if err := validateProxyAllowRules(profile.Network.Proxy.Allow); err != nil {
		return err
	}
	if err := validateResolverRecords(profile.Network.Resolver.Records); err != nil {
		return err
	}
	if err := validatePortRules(profile.Network.Ports); err != nil {
		return err
	}
	return nil
}

func validateProfileAccounts(profile Profile) error {
	if profile.User.Name != "" && !validAccountName(profile.User.Name) {
		return fmt.Errorf("profile.user: invalid account name %q; use up to 32 lowercase ASCII letters, digits, '_' or '-', starting with a letter or '_'", profile.User.Name)
	}
	seenIDs := make(map[uint32]int, len(profile.Groups))
	seenNames := make(map[string]int, len(profile.Groups))
	for index, group := range profile.Groups {
		field := fmt.Sprintf("profile.group[%d]", index)
		if !validAccountName(group.Name) {
			return fmt.Errorf("%s: invalid account name %q; use up to 32 lowercase ASCII letters, digits, '_' or '-', starting with a letter or '_'", field, group.Name)
		}
		if previous, exists := seenIDs[group.ID]; exists {
			return fmt.Errorf("%s: duplicate group ID from profile.group[%d]", field, previous)
		}
		if previous, exists := seenNames[group.Name]; exists {
			return fmt.Errorf("%s: duplicate group name from profile.group[%d]", field, previous)
		}
		seenIDs[group.ID] = index
		seenNames[group.Name] = index
	}
	return nil
}

func validAccountName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for index, character := range name {
		valid := character >= 'a' && character <= 'z' || character == '_'
		if index > 0 {
			valid = valid || character >= '0' && character <= '9' || character == '-'
		}
		if !valid {
			return false
		}
	}
	return true
}

func validateEnvironment(environment map[string]string) error {
	for name, value := range environment {
		if name == "" {
			return fmt.Errorf("profile.env: variable name cannot be empty")
		}
		for index, character := range name {
			valid := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_'
			if index > 0 {
				valid = valid || character >= '0' && character <= '9'
			}
			if !valid {
				return fmt.Errorf("profile.env.%s: invalid environment variable name", name)
			}
		}
		switch name {
		case "HOME", "PATH", "PWD", "TMPDIR", "GOMAXPROCS":
			return fmt.Errorf("profile.env.%s: reserved by isobox; remove it", name)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("profile.env.%s: value contains a NUL byte", name)
		}
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
		return fmt.Errorf("settings.defaults.network_mode: invalid value %q; use off, direct, proxy, or egress", settings.Defaults.NetworkMode)
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
	dnsServers := append([]string(nil), profile.Network.DNS...)
	if len(dnsServers) == 0 && settings.Defaults.DNS != "" {
		dnsServers = []string{settings.Defaults.DNS}
	}
	dnsServer := ""
	if len(dnsServers) != 0 {
		dnsServer = dnsServers[0]
	}
	if mode == NetworkModeProxy {
		if profile.Network.ProxyAlias == "" {
			return EffectiveProfile{}, fmt.Errorf("profile.network.proxy_alias: required because the effective network mode is proxy")
		}
	}
	if (profile.Network.InterfaceName != "" || profile.Network.Subnet != "") && mode == NetworkModeOff {
		return EffectiveProfile{}, fmt.Errorf("profile.network: interface and subnet are only supported with network mode direct, proxy, or egress")
	}
	if profile.Network.Subnet != "" && (mode == NetworkModeProxy || mode == NetworkModeEgress) {
		prefix, _ := netip.ParsePrefix(profile.Network.Subnet)
		if prefix.Bits() > 29 {
			return EffectiveProfile{}, fmt.Errorf("profile.network: subnet %q needs at least three usable IPv4 addresses for the sandbox, resolver, and host gateway", profile.Network.Subnet)
		}
	}
	if mode != NetworkModeProxy && profile.Network.ProxyAlias != "" {
		return EffectiveProfile{}, fmt.Errorf("profile.network.proxy_alias: only valid when the effective network mode is proxy")
	}
	if len(profile.Network.Proxy.Allow) != 0 && mode != NetworkModeProxy && mode != NetworkModeEgress {
		return EffectiveProfile{}, fmt.Errorf("profile.network.proxy is only supported with network mode proxy or egress")
	}
	if len(profile.Network.Resolver.Records) != 0 && mode != NetworkModeEgress {
		return EffectiveProfile{}, fmt.Errorf("profile.network.resolver is only supported with network mode egress")
	}
	return EffectiveProfile{
		Profile:     profile,
		NetworkMode: mode,
		DNSServer:   dnsServer,
		DNSServers:  dnsServers,
	}, nil
}

func validInterfaceName(name string) bool {
	if len(name) == 0 || len(name) > 15 || name == "." || name == ".." || name == "lo" {
		return false
	}
	for _, character := range name {
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-'
		if !valid {
			return false
		}
	}
	return true
}

func profileType() reflect.Type { return reflect.TypeOf(Profile{}) }

func settingsOverrideType() reflect.Type { return reflect.TypeOf(settingsOverride{}) }

func validNetworkMode(mode NetworkMode, allowInherit bool) bool {
	if allowInherit && mode == "" {
		return true
	}
	switch mode {
	case NetworkModeOff, NetworkModeDirect, NetworkModeProxy, NetworkModeEgress:
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

// IsValidProxyAlias reports whether alias follows the profile proxy alias
// syntax. Proxy storage uses the same contract as profile validation.
func IsValidProxyAlias(alias string) bool { return validAlias(alias) }

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
	address := net.ParseIP(value)
	if strings.TrimSpace(value) != value || address == nil || address.To4() == nil || strings.Contains(value, ":") {
		return fmt.Errorf("%s: must be an IPv4 address without a port; IPv6 is disabled", field)
	}
	return nil
}

func validateProxyAllowRules(rules []ProxyAllowRule) error {
	for index, rule := range rules {
		field := fmt.Sprintf("profile.network.proxy.allow[%d]", index)
		if len(rule.IPs) == 0 {
			return fmt.Errorf("%s.ip: at least one IPv4 address or CIDR is required", field)
		}
		for ipIndex, ip := range rule.IPs {
			ipField := fmt.Sprintf("%s.ip[%d]", field, ipIndex)
			if strings.TrimSpace(ip) != ip || ip == "" {
				return fmt.Errorf("%s: must be an IPv4 address or CIDR", ipField)
			}
			if strings.Contains(ip, "/") {
				prefix, err := netip.ParsePrefix(ip)
				if err != nil || !prefix.Addr().Is4() {
					return fmt.Errorf("%s: must be an IPv4 address or CIDR; IPv6 is disabled", ipField)
				}
			} else {
				address, err := netip.ParseAddr(ip)
				if err != nil || !address.Is4() {
					return fmt.Errorf("%s: must be an IPv4 address or CIDR; IPv6 is disabled", ipField)
				}
			}
		}
		if rule.Protocol != "" && rule.Protocol != PortProtocolBoth && rule.Protocol != PortProtocolTCP && rule.Protocol != PortProtocolUDP {
			return fmt.Errorf("%s.protocol: invalid value %q; use both, tcp, or udp", field, rule.Protocol)
		}
		for portIndex, port := range rule.Ports {
			if port == 0 {
				return fmt.Errorf("%s.port[%d]: must be between 1 and 65535", field, portIndex)
			}
		}
		for rangeIndex, portRange := range rule.PortRanges {
			rangeField := fmt.Sprintf("%s.port_range[%d]", field, rangeIndex)
			if portRange.Start == 0 || portRange.End == 0 {
				return fmt.Errorf("%s: start and end must be between 1 and 65535", rangeField)
			}
			if portRange.End < portRange.Start {
				return fmt.Errorf("%s.end: must be greater than or equal to start", rangeField)
			}
		}
	}
	return nil
}

func validateResolverRecords(records []ResolverRecord) error {
	seen := make(map[string]map[netip.Addr]struct{}, len(records))
	counts := make(map[string]int, len(records))
	for index, record := range records {
		field := fmt.Sprintf("profile.network.resolver.record[%d]", index)
		domain, err := NormalizeDomain(record.Domain)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		if domain == "host.gateway.internal" {
			return fmt.Errorf("%s: domain %q is reserved by isobox", field, domain)
		}
		if len(record.IPs) == 0 {
			return fmt.Errorf("%s: at least one IPv4 ip is required", field)
		}
		for ipIndex, value := range record.IPs {
			ipField := fmt.Sprintf("%s.ip[%d]", field, ipIndex)
			address, err := netip.ParseAddr(value)
			if err != nil || !address.Is4() {
				return fmt.Errorf("%s: must be an IPv4 address; IPv6 is disabled", ipField)
			}
			if seen[domain] == nil {
				seen[domain] = make(map[netip.Addr]struct{})
			}
			if _, exists := seen[domain][address]; exists {
				return fmt.Errorf("%s: duplicate IPv4 address %s for domain %q", ipField, address, domain)
			}
			seen[domain][address] = struct{}{}
			counts[domain]++
			if counts[domain] > maxResolverAddressesPerDomain {
				return fmt.Errorf("%s: domain %q has more than %d IPv4 records", ipField, domain, maxResolverAddressesPerDomain)
			}
		}
	}
	return nil
}

const maxResolverAddressesPerDomain = 16

// NormalizeDomain validates and normalizes one exact DNS domain name.
// A final root dot is optional; wildcard and non-ASCII names are not accepted.
func NormalizeDomain(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", fmt.Errorf("must be an exact domain name without surrounding whitespace")
	}
	name := strings.TrimSuffix(value, ".")
	if len(name) == 0 || len(name) > 253 {
		return "", fmt.Errorf("domain name length must be between 1 and 253 bytes")
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("domain name contains an invalid label")
		}
		for _, character := range label {
			valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-'
			if !valid {
				return "", fmt.Errorf("domain name must use ASCII letters, digits, dots, and hyphens only")
			}
		}
	}
	return strings.ToLower(name), nil
}

func validatePortRules(ports []PortRule) error {
	parsed := make([]net.IP, len(ports))
	for index, rule := range ports {
		prefix := fmt.Sprintf("profile.network.port[%d]", index)
		if strings.TrimSpace(rule.HostAddress) != rule.HostAddress {
			return fmt.Errorf("%s.host_address: must be an IPv4 bind address; IPv6 is disabled", prefix)
		}
		parsed[index] = net.ParseIP(rule.HostAddress)
		if parsed[index] == nil || parsed[index].To4() == nil || strings.Contains(rule.HostAddress, ":") {
			return fmt.Errorf("%s.host_address: must be an IPv4 bind address; IPv6 is disabled", prefix)
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
