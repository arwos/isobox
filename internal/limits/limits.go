/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

// Package limits prepares fail-closed resource limits for a sandbox process.
package limits

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/arwos/isobox/internal/profile"
)

const (
	bwrapSupervisorTasks        = 2
	offGuardBootstrapTaskBudget = 7
)

// OffGuardBootstrapSlack reserves enough transient tasks for the trusted Go
// network-guard helper and nft process, then is removed before the app gate opens.
func OffGuardBootstrapSlack(request profile.ResourceLimits) uint64 {
	if request.ProcessCount == 0 || request.ProcessCount > ^uint64(0)-bwrapSupervisorTasks {
		return 0
	}
	finalBudget := request.ProcessCount + bwrapSupervisorTasks
	if finalBudget >= offGuardBootstrapTaskBudget {
		return 0
	}
	return offGuardBootstrapTaskBudget - finalBudget
}

// Any reports whether a profile asks isobox to enforce at least one limit.
func Any(request profile.ResourceLimits) bool {
	return NeedsCgroup(request) || request.OpenFiles != 0
}

// NeedsCgroup reports whether the profile requests a cgroup v2 controller.
func NeedsCgroup(request profile.ResourceLimits) bool {
	return request.CPUQuotaPercent != 0 || request.MemoryBytes != 0 || request.ProcessCount != 0
}

// ScopeProperties returns systemd resource-control assignments for a scope.
// systemd's task counter includes the outer bwrap monitor and the inner bwrap
// process that owns the PID namespace. Reserve both while keeping the requested
// budget for application processes and threads.
func ScopeProperties(request profile.ResourceLimits) ([]string, error) {
	return scopeProperties(request, 0)
}

func scopeProperties(request profile.ResourceLimits, bootstrapTaskSlack uint64) ([]string, error) {
	var properties []string
	if request.CPUQuotaPercent != 0 {
		properties = append(properties, "CPUQuota="+strconv.FormatUint(request.CPUQuotaPercent, 10)+"%")
	}
	if request.MemoryBytes != 0 {
		properties = append(properties, "MemoryMax="+strconv.FormatUint(request.MemoryBytes, 10))
	}
	if request.ProcessCount != 0 {
		if bootstrapTaskSlack > ^uint64(0)-bwrapSupervisorTasks || request.ProcessCount > ^uint64(0)-bwrapSupervisorTasks-bootstrapTaskSlack {
			return nil, fmt.Errorf("profile limit process_count is too large to reserve %d bwrap supervisor tasks and %d bootstrap task(s)", bwrapSupervisorTasks, bootstrapTaskSlack)
		}
		properties = append(properties, "TasksMax="+strconv.FormatUint(request.ProcessCount+bwrapSupervisorTasks+bootstrapTaskSlack, 10))
	}
	return properties, nil
}

// ScopeCommand runs the bwrap command in a transient user scope. Standard
// streams, environment, working directory, and inherited control FDs are
// forwarded so the existing startup gate can verify the scope before release.
func ScopeCommand(command *exec.Cmd, systemdRun string, request profile.ResourceLimits, bootstrapTaskSlack uint64) (*exec.Cmd, string, error) {
	if command == nil {
		return nil, "", fmt.Errorf("sandbox command is required")
	}
	if systemdRun == "" {
		return nil, "", fmt.Errorf("systemd-run is required for CPU, memory, or process limits")
	}
	properties, err := scopeProperties(request, bootstrapTaskSlack)
	if err != nil {
		return nil, "", err
	}
	unit := "isobox-" + strconv.FormatInt(time.Now().UnixNano(), 10) + ".scope"
	args := []string{"--user", "--scope", "--slice-inherit", "--quiet"}
	args = append(args, "--unit="+unit)
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
	return scoped, unit, nil
}
