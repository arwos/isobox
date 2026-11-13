//go:build !linux

package sandbox

func RunOffGuardHelper([]string) (int, bool) {
	return 0, false
}

func RunIPv4NetworkHelper([]string) (int, bool) {
	return 0, false
}

func RunProxyTUNHelper([]string) (int, bool) {
	return 0, false
}
