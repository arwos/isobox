//go:build !linux

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
