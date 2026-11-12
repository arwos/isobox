//go:build !linux

package integration

import "fmt"

// InstallLauncher is available on Linux desktop environments only.
func InstallLauncher(LauncherOptions) (string, error) {
	return "", fmt.Errorf("desktop launchers are supported only on Linux")
}

// RemoveLauncher is available on Linux desktop environments only.
func RemoveLauncher(string) (string, error) {
	return "", fmt.Errorf("desktop launchers are supported only on Linux")
}
