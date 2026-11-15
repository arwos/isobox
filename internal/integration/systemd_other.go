//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import "fmt"

// InstallSystemdUnit is available on Linux only.
func InstallSystemdUnit(SystemdUnitOptions) (string, error) {
	return "", fmt.Errorf("systemd user services are supported only on Linux")
}

// EnableSystemdUnit is available on Linux only.
func EnableSystemdUnit(string) (string, error) {
	return "", fmt.Errorf("systemd user services are supported only on Linux")
}

// DisableSystemdUnit is available on Linux only.
func DisableSystemdUnit(string) (string, error) {
	return "", fmt.Errorf("systemd user services are supported only on Linux")
}

// RemoveSystemdUnit is available on Linux only.
func RemoveSystemdUnit(string) (string, error) {
	return "", fmt.Errorf("systemd user services are supported only on Linux")
}
