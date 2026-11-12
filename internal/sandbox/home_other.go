//go:build !linux

package sandbox

import "fmt"

func resolveHome(string, string, string) (string, error) {
	return "", fmt.Errorf("sandbox HOME resolution supports only Linux")
}
