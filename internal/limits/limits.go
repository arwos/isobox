// Package limits prepares fail-closed resource limits for a sandbox process.
package limits

import (
	"fmt"
	"os/exec"
	"strconv"

	"github.com/arwos/isobox/internal/profile"
)

// Any reports whether a profile asks isobox to enforce at least one limit.
func Any(request profile.ResourceLimits) bool {
	return NeedsCgroup(request) || request.OpenFiles != 0
}

// NeedsCgroup reports whether the profile requests a cgroup v2 controller.
func NeedsCgroup(request profile.ResourceLimits) bool {
	return request.CPUQuotaPercent != 0 || request.MemoryBytes != 0 || request.ProcessCount != 0
}

// ScopeProperties returns systemd resource-control assignments for a scope.
// systemd's task counter also includes bwrap's namespace supervisor, so reserve
// one task for that process while keeping the requested budget for the app.
func ScopeProperties(request profile.ResourceLimits) ([]string, error) {
	var properties []string
	if request.CPUQuotaPercent != 0 {
		properties = append(properties, "CPUQuota="+strconv.FormatUint(request.CPUQuotaPercent, 10)+"%")
	}
	if request.MemoryBytes != 0 {
		properties = append(properties, "MemoryMax="+strconv.FormatUint(request.MemoryBytes, 10))
	}
	if request.ProcessCount != 0 {
		if request.ProcessCount == ^uint64(0) {
			return nil, fmt.Errorf("profile limit process_count is too large to reserve a task for the bwrap supervisor")
		}
		properties = append(properties, "TasksMax="+strconv.FormatUint(request.ProcessCount+1, 10))
	}
	return properties, nil
}

// ScopeCommand runs the bwrap command in a transient user scope. Standard
// streams, environment, working directory, and inherited control FDs are
// forwarded so the existing startup gate can verify the scope before release.
func ScopeCommand(command *exec.Cmd, systemdRun string, request profile.ResourceLimits) (*exec.Cmd, error) {
	if command == nil {
		return nil, fmt.Errorf("sandbox command is required")
	}
	if systemdRun == "" {
		return nil, fmt.Errorf("systemd-run is required for CPU, memory, or process limits")
	}
	properties, err := ScopeProperties(request)
	if err != nil {
		return nil, err
	}
	args := []string{"--user", "--scope", "--slice-inherit", "--quiet"}
	for _, property := range properties {
		args = append(args, "--property="+property)
	}
	args = append(args, "--", command.Path)
	if len(command.Args) > 1 {
		args = append(args, command.Args[1:]...)
	}
	scoped := exec.Command(systemdRun, args...)
	scoped.Stdin = command.Stdin
	scoped.Stdout = command.Stdout
	scoped.Stderr = command.Stderr
	scoped.Env = command.Env
	scoped.Dir = command.Dir
	scoped.ExtraFiles = command.ExtraFiles
	return scoped, nil
}
