//go:build !linux

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

func RunWithNetwork(*exec.Cmd, NetworkRuntime, []profile.PortRule) (int, error) {
	return 0, fmt.Errorf("sandbox execution supports only Linux")
}
