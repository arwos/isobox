//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import "fmt"

type NetworkManagerUnitOptions struct {
	Name       string
	ConfigPath string
}

func InstallAndStartNetworkManager(NetworkManagerUnitOptions) (string, error) {
	return "", fmt.Errorf("network groups are supported only on Linux")
}

func CheckUserSystemdManager() error {
	return fmt.Errorf("network groups are supported only on Linux")
}

func NetworkManagerUnitState(string) (string, error) {
	return "", fmt.Errorf("network groups are supported only on Linux")
}

func RemoveNetworkManagerUnit(string) (string, error) {
	return "", fmt.Errorf("network groups are supported only on Linux")
}
