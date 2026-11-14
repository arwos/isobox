//go:build !linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"fmt"

	"github.com/arwos/isobox/internal/profile"
)

func resolveGUI(access profile.GUIAccess) (guiSetup, error) {
	if access.Wayland || access.X11 || access.Audio || access.GPU || access.Desktop {
		return guiSetup{}, fmt.Errorf("GUI permissions are supported only on Linux")
	}
	return guiSetup{environment: make(map[string]string)}, nil
}
