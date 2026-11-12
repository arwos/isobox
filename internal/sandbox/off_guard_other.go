//go:build !linux

package sandbox

func RunOffGuardHelper([]string) (int, bool) {
	return 0, false
}
