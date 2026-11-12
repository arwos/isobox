//go:build !linux

package preflight

import "fmt"

func probeUserNamespace() error {
	return fmt.Errorf("user namespace probes are supported only on Linux")
}

func probeTUN() error {
	return fmt.Errorf("TUN probes are supported only on Linux")
}

func RunProbeHelper(args []string) (int, bool) {
	return 0, false
}
