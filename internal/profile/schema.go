/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

// Package profile defines the typed UNIC documents used by isobox.
package profile

// SchemaVersion is the current version for both profile and settings documents.
const SchemaVersion = 1

// NetworkMode identifies how the application network namespace is connected.
type NetworkMode string

const (
	NetworkModeOff    NetworkMode = "off"
	NetworkModeDirect NetworkMode = "direct"
	NetworkModeProxy  NetworkMode = "proxy"
	NetworkModeEgress NetworkMode = "egress"
)

// PortProtocol selects which transport protocols a published port rule exposes.
type PortProtocol string

const (
	PortProtocolBoth PortProtocol = "both"
	PortProtocolTCP  PortProtocol = "tcp"
	PortProtocolUDP  PortProtocol = "udp"
)

// Profile describes sandbox permissions and defaults for an application.
type Profile struct {
	Version   int               `unic:"version,default=1"`
	User      Account           `unic:"-"`
	Groups    []Account         `unic:"-"`
	UserArgs  []any             `unic:"user,omitempty"`
	GroupArgs []any             `unic:"group,omitempty"`
	HomePath  string            `unic:"home_path,omitempty"`
	Env       map[string]string `unic:"env,omitempty"`
	Path      []string          `unic:"path,omitempty"`
	Command   []string          `unic:"command,omitempty"`
	Mounts    []Mount           `unic:"mount,omitempty"`
	Network   NetworkConfig     `unic:"network"`
	GUI       GUIAccess         `unic:"gui"`
	Limits    ResourceLimits    `unic:"limits"`
}

// Account identifies a user or group inside the sandbox account databases.
// The primary user and group IDs are also used as the process credentials.
type Account struct {
	ID   uint32
	Name string
}

// Mount grants access to one host path at one path inside the sandbox.
type Mount struct {
	Source   string `unic:"source"`
	Target   string `unic:"target"`
	ReadOnly bool   `unic:"read_only,default=true"`
}

// NetworkConfig sets the network policy and optional published ports.
// An empty Mode inherits the user's isobox default; the built-in default is off.
type NetworkConfig struct {
	// InterfaceName and Subnet are positional attributes on the network block.
	// A CIDR may be supplied as the first attribute without an interface name.
	InterfaceName string      `unic:"interface,attr=1"`
	Subnet        string      `unic:"subnet,attr=2"`
	Groups        []string    `unic:"group,omitempty"`
	Aliases       []string    `unic:"alias,omitempty"`
	Mode          NetworkMode `unic:"mode,omitempty"`
	DNS           []string    `unic:"dns,omitempty"`
	ProxyAlias    string      `unic:"proxy_alias,omitempty"`
	Proxy         ProxyPolicy `unic:"proxy"`
	Resolver      Resolver    `unic:"resolver"`
	Ports         []PortRule  `unic:"-"`              // Normalized host forwards used by runtime code.
	PortValues    []any       `unic:"port,omitempty"` // Raw positional UNIC values, normalized by DecodeProfile.
}

// ProxyPolicy contains explicit destination rules for egress and optional
// filtering when used with an external SOCKS5 proxy.
type ProxyPolicy struct {
	Allow []ProxyAllowRule `unic:"allow,omitempty"`
}

// ProxyAllowRule grants one or more IPv4 destinations or prefixes, with
// optional transport and destination port limits. Empty port sets mean any
// port.
type ProxyAllowRule struct {
	IPs             []string         `unic:"ip"`
	Ports           []uint16         `unic:"port,omitempty"`
	PortRanges      []ProxyPortRange `unic:"-"`                    // Normalized inclusive port ranges.
	PortRangeValues []any            `unic:"port_range,omitempty"` // Positional start/end values, normalized by DecodeProfile.
	Protocol        PortProtocol     `unic:"protocol,default=both"`
}

// ProxyPortRange is an inclusive destination port range.
type ProxyPortRange struct {
	Start uint16
	End   uint16
}

// Resolver contains static IPv4 DNS records used by egress mode.
type Resolver struct {
	Records []ResolverRecord `unic:"record,omitempty"`
}

// ResolverRecord maps one exact domain name to one or more IPv4 addresses.
type ResolverRecord struct {
	Domain string   `unic:"domain,attr=1"`
	IPs    []string `unic:"ip,omitempty"`
}

// PortRule publishes the same port number on the host and in the sandbox.
// EndPort equal to zero means Port is a single port; otherwise the inclusive
// range Port..EndPort is published.
type PortRule struct {
	HostAddress string       `unic:"host_address"`
	Port        uint16       `unic:"port"`
	EndPort     uint16       `unic:"end_port,omitempty"`
	Protocol    PortProtocol `unic:"protocol,default=both"`
}

// GUIAccess contains explicit grants for desktop integration surfaces.
// Desktop grants the host user's session D-Bus; it does not imply portal
// handling or access to the system bus.
type GUIAccess struct {
	Wayland bool `unic:"wayland,default=false"`
	X11     bool `unic:"x11,default=false"`
	Audio   bool `unic:"audio,default=false"`
	GPU     bool `unic:"gpu,default=false"`
	Desktop bool `unic:"desktop,default=false"`
}

// ResourceLimits uses zero to mean that a particular limit was not requested.
// CPUQuotaPercent is relative to one logical CPU: 100 means one CPU, 50 half,
// and 200 two CPUs. MemoryBytes is measured in bytes.
type ResourceLimits struct {
	CPUQuotaPercent uint64 `unic:"cpu_quota_percent,omitempty"`
	MemoryBytes     uint64 `unic:"memory_bytes,omitempty"`
	ProcessCount    uint64 `unic:"process_count,omitempty"`
	OpenFiles       uint64 `unic:"open_files,omitempty"`
}

// Settings contains global defaults shared by profiles after system/user merge.
type Settings struct {
	Version  int             `unic:"version,default=1"`
	Defaults SettingsDefault `unic:"defaults"`
}

// settingsOverride represents only values explicitly provided in one settings
// layer. Sentinel values keep absent settings distinguishable from empty
// strings, while the version field retains its ordinary UNIC default.
type settingsOverride struct {
	Version  int                     `unic:"version,default=1"`
	Defaults settingsDefaultOverride `unic:"defaults"`
}

type settingsDefaultOverride struct {
	NetworkMode NetworkMode `unic:"network_mode"`
	DNS         string      `unic:"dns"`
}

const (
	unsetNetworkMode NetworkMode = "\x00isobox-unset"
)

const unsetDNS = "\x00isobox-unset"

// SettingsDefault contains values profiles can override.
type SettingsDefault struct {
	NetworkMode NetworkMode `unic:"network_mode,default=off"`
	DNS         string      `unic:"dns,omitempty"`
}

// DefaultSettings returns isobox's built-in safe defaults.
func DefaultSettings() Settings {
	return Settings{
		Version: SchemaVersion,
		Defaults: SettingsDefault{
			NetworkMode: NetworkModeOff,
		},
	}
}
