//go:build !linux

package sandbox

import (
	"fmt"
	"os/exec"
)

func Run(*exec.Cmd) (int, error) {
	return 0, fmt.Errorf("sandbox execution supports only Linux")
}
