//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"fmt"
	"os/exec"

	"github.com/arwos/isobox/internal/profile"
)

func Run(*exec.Cmd) (int, error) {
	return 0, fmt.Errorf("sandbox execution supports only Linux")
}

func RunWithPasta(*exec.Cmd, string, []profile.PortRule) (int, error) {
	return 0, fmt.Errorf("sandbox execution supports only Linux")
}

func RunWithLimits(*exec.Cmd, profile.ResourceLimits, string) (int, error) {
	return 0, fmt.Errorf("sandbox execution supports only Linux")
}

func RunWithNetwork(*exec.Cmd, NetworkRuntime, []profile.PortRule, profile.ResourceLimits, string) (int, error) {
	return 0, fmt.Errorf("sandbox execution supports only Linux")
}
