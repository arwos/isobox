//go:build !linux

package cli

import "fmt"

func readProxyCredentials() (string, string, error) {
	return "", "", fmt.Errorf("interactive SOCKS5 credentials are supported only on Linux")
}
