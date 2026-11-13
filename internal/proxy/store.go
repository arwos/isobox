// Package proxy owns SOCKS5 proxy aliases and their credentials. The public
// management API intentionally returns metadata only; runtime code in this
// package can load credentials through the unexported loader.
package proxy

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/arwos/isobox/internal/profile"
)

// AuthMode selects the SOCKS5 authentication method used by a proxy.
type AuthMode string

const (
	AuthNone     AuthMode = "none"
	AuthUserPass AuthMode = "userpass"
)

// Info contains proxy metadata that is safe to display in CLI output. It never
// contains a username or password.
type Info struct {
	Alias  string
	Server string
	Auth   AuthMode
}

// Validate checks the non-secret parts of a proxy definition.
func Validate(alias, server string, auth AuthMode) error {
	if !profile.IsValidProxyAlias(alias) {
		return fmt.Errorf("proxy alias is invalid; use 1-64 ASCII letters, digits, '.', '_' or '-' and start with a letter or digit")
	}
	if _, err := normalizeServer(server); err != nil {
		return err
	}
	if auth != AuthNone && auth != AuthUserPass {
		return fmt.Errorf("proxy authentication must be none or userpass")
	}
	return nil
}

func normalizeServer(server string) (string, error) {
	if strings.TrimSpace(server) != server || strings.ContainsRune(server, '\x00') {
		return "", fmt.Errorf("proxy server must be an IPv4 address or DNS name followed by a port")
	}
	host, portText, err := net.SplitHostPort(server)
	if err != nil || host == "" {
		return "", fmt.Errorf("proxy server must be an IPv4 address or DNS name followed by a port")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return "", fmt.Errorf("proxy server port must be between 1 and 65535")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil || strings.Contains(host, ":") {
			return "", fmt.Errorf("proxy server must use IPv4; IPv6 is disabled")
		}
	} else if !validHostname(host) {
		return "", fmt.Errorf("proxy server host must be an IPv4 address or DNS name")
	}
	return net.JoinHostPort(host, strconv.FormatUint(port, 10)), nil
}

func validHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.TrimSpace(host) != host {
		return false
	}
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
	}
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}
