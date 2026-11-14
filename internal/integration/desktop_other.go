//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

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
