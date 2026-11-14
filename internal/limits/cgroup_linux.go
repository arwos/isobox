//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package limits

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/arwos/isobox/internal/profile"
)

const (
	rlimitNoFile       = 7
	cgroupProbeTimeout = 3 * time.Second
)

var optionPattern = regexp.MustCompile(`--[a-zA-Z0-9][a-zA-Z0-9-]*`)

type cgroupMount struct {
	root      string
	mountPath string
}

// CheckHost verifies that the requested resource controls have a user-systemd
// runner, cgroup v2 mount, and the kernel controllers required to enforce them.
func CheckHost(request profile.ResourceLimits) (string, error) {
	if request.OpenFiles != 0 {
		var current syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &current); err != nil {
			return "", fmt.Errorf("cannot inspect the inherited open-file hard limit: %w", err)
		}
		if request.OpenFiles > current.Max {
			return "", fmt.Errorf("profile limit open_files=%d exceeds the inherited hard limit %d; raise the user's hard nofile limit or lower the profile value", request.OpenFiles, current.Max)
		}
	}
	if !NeedsCgroup(request) {
		return "", nil
	}

	runner, err := exec.LookPath("systemd-run")
	if err != nil {
		return "", fmt.Errorf("profile requests CPU, memory, or process limits; install systemd-run with a running user systemd manager, or remove those profile limits")
	}
	ctx, cancel := contextWithTimeout()
	defer cancel()
	output, err := exec.CommandContext(ctx, runner, "--help").CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("systemd-run --help timed out while checking resource-limit support")
	}
	if err != nil {
		return "", fmt.Errorf("cannot inspect systemd-run capabilities: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	options := make(map[string]struct{})
	for _, option := range optionPattern.FindAllString(string(output), -1) {
		options[option] = struct{}{}
	}
	for _, option := range []string{"--user", "--scope", "--slice-inherit", "--property", "--quiet"} {
		if _, ok := options[option]; !ok {
			return "", fmt.Errorf("systemd-run lacks %s required to apply rootless cgroup limits; update systemd or remove CPU, memory, and process limits", option)
		}
	}

	mount, err := findCgroup2Mount()
	if err != nil {
		return "", fmt.Errorf("profile requests CPU, memory, or process limits; enable a unified cgroup v2 hierarchy: %w", err)
	}
	controllersData, err := os.ReadFile(filepath.Join(mount.mountPath, "cgroup.controllers"))
	if err != nil {
		return "", fmt.Errorf("read cgroup v2 controllers: %w", err)
	}
	controllers := make(map[string]bool)
	for _, name := range strings.Fields(string(controllersData)) {
		controllers[name] = true
	}
	var missing []string
	if request.CPUQuotaPercent != 0 && !controllers["cpu"] {
		missing = append(missing, "cpu")
	}
	if request.MemoryBytes != 0 && !controllers["memory"] {
		missing = append(missing, "memory")
	}
	if request.ProcessCount != 0 && !controllers["pids"] {
		missing = append(missing, "pids")
	}
	if len(missing) != 0 {
		return "", fmt.Errorf("cgroup v2 lacks required controller(s) %s; enable them for the user systemd manager or remove the corresponding profile limits", strings.Join(missing, ", "))
	}
	if _, err := ScopeProperties(request); err != nil {
		return "", err
	}
	return runner, nil
}

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cgroupProbeTimeout)
}

// VerifyCgroupPID confirms the requested values were installed on the bwrap
// scope and that no ancestor cgroup is stricter than the requested budget.
func VerifyCgroupPID(pid int, request profile.ResourceLimits) error {
	return verifyCgroupPID(pid, request, 0)
}

// VerifyCgroupPIDWithBootstrapSlack verifies a transient task allowance used
// only while trusted sandbox setup helpers run before the application gate.
func VerifyCgroupPIDWithBootstrapSlack(pid int, request profile.ResourceLimits, bootstrapTaskSlack uint64) error {
	return verifyCgroupPID(pid, request, bootstrapTaskSlack)
}

func verifyCgroupPID(pid int, request profile.ResourceLimits, bootstrapTaskSlack uint64) error {
	if !NeedsCgroup(request) {
		return nil
	}
	if pid <= 0 {
		return fmt.Errorf("bwrap reported an invalid process ID for cgroup verification")
	}
	mount, err := findCgroup2Mount()
	if err != nil {
		return err
	}
	cgroupPath, err := processCgroupPath(pid)
	if err != nil {
		return err
	}
	cgroupDir, err := cgroupDirectory(mount, cgroupPath)
	if err != nil {
		return err
	}
	ancestors, err := cgroupAncestors(mount.mountPath, cgroupDir)
	if err != nil {
		return err
	}
	properties, err := scopeProperties(request, bootstrapTaskSlack)
	if err != nil {
		return err
	}
	expectedTasks := uint64(0)
	for _, property := range properties {
		name, value, _ := strings.Cut(property, "=")
		switch name {
		case "CPUQuota":
			percent, err := strconv.ParseUint(strings.TrimSuffix(value, "%"), 10, 64)
			if err != nil {
				return fmt.Errorf("invalid prepared CPU quota %q: %w", value, err)
			}
			if err := verifyOwnCPUQuota(cgroupDir, percent); err != nil {
				return err
			}
			if err := verifyParentCPUQuota(ancestors[1:], percent); err != nil {
				return err
			}
		case "MemoryMax":
			requested, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid prepared memory limit %q: %w", value, err)
			}
			if err := verifyOwnNumericLimit(cgroupDir, "memory.max", requested, "memory_bytes"); err != nil {
				return err
			}
			if err := verifyParentNumericLimits(ancestors[1:], "memory.max", requested, "memory_bytes"); err != nil {
				return err
			}
		case "TasksMax":
			expectedTasks, err = strconv.ParseUint(value, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid prepared task limit %q: %w", value, err)
			}
			if err := verifyOwnNumericLimit(cgroupDir, "pids.max", expectedTasks, "process_count"); err != nil {
				return err
			}
			if err := verifyParentNumericLimits(ancestors[1:], "pids.max", expectedTasks, "process_count plus bwrap supervisors"); err != nil {
				return err
			}
		}
	}
	if expectedTasks != 0 && request.ProcessCount != 0 && expectedTasks != request.ProcessCount+bwrapSupervisorTasks+bootstrapTaskSlack {
		return fmt.Errorf("effective TasksMax=%d does not reserve exactly %d bwrap supervisor tasks and %d bootstrap task(s) for profile process_count=%d", expectedTasks, bwrapSupervisorTasks, bootstrapTaskSlack, request.ProcessCount)
	}
	return nil
}

// ApplyFinalTaskLimit removes bootstrap-only task slack from a transient scope
// and waits until cgroup v2 reports the exact profile budget.
func ApplyFinalTaskLimit(systemctl, unit string, pid int, request profile.ResourceLimits) error {
	if request.ProcessCount == 0 {
		return nil
	}
	if systemctl == "" || unit == "" || pid <= 0 {
		return fmt.Errorf("systemctl, scope unit, and sandbox PID are required to finalize process_count")
	}
	properties, err := ScopeProperties(request)
	if err != nil {
		return err
	}
	var taskProperty string
	for _, property := range properties {
		if strings.HasPrefix(property, "TasksMax=") {
			taskProperty = property
			break
		}
	}
	if taskProperty == "" {
		return fmt.Errorf("process_count did not produce a TasksMax property")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cgroupProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, systemctl, "--user", "set-property", "--runtime", unit, taskProperty).CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("systemctl set-property timed out while applying the final process_count limit")
	}
	if err != nil {
		return fmt.Errorf("cannot apply final process_count limit to %s: %w (%s)", unit, err, strings.TrimSpace(string(output)))
	}
	mount, err := findCgroup2Mount()
	if err != nil {
		return err
	}
	cgroupPath, err := processCgroupPath(pid)
	if err != nil {
		return err
	}
	cgroupDir, err := cgroupDirectory(mount, cgroupPath)
	if err != nil {
		return err
	}
	want, err := strconv.ParseUint(strings.TrimPrefix(taskProperty, "TasksMax="), 10, 64)
	if err != nil {
		return fmt.Errorf("parse final task limit %q: %w", taskProperty, err)
	}
	deadline := time.Now().Add(cgroupProbeTimeout)
	for {
		current, finite, err := readNumericLimit(cgroupDir, "pids.max")
		if err == nil && finite && current == want {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("verify final process_count limit: %w", err)
			}
			return fmt.Errorf("effective pids.max=%d (finite=%t) after requesting %d", current, finite, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type rlimit64 struct {
	current uint64
	maximum uint64
}

// ApplyOpenFilesPID sets both the soft and hard RLIMIT_NOFILE on the sandbox
// child while it is waiting behind bwrap's startup gate.
func ApplyOpenFilesPID(pid int, requested uint64) error {
	if requested == 0 {
		return nil
	}
	if pid <= 0 {
		return fmt.Errorf("bwrap reported an invalid process ID for open-file limit application")
	}
	var old rlimit64
	if err := prlimit64(pid, nil, &old); err != nil {
		return fmt.Errorf("read sandbox process open-file hard limit: %w", err)
	}
	if requested > old.maximum {
		return fmt.Errorf("profile limit open_files=%d exceeds the sandbox process hard limit %d; lower the profile value or raise the user's hard nofile limit", requested, old.maximum)
	}
	updated := rlimit64{current: requested, maximum: requested}
	if err := prlimit64(pid, &updated, nil); err != nil {
		return fmt.Errorf("apply profile open_files=%d to the sandbox process: %w", requested, err)
	}
	var verified rlimit64
	if err := prlimit64(pid, nil, &verified); err != nil {
		return fmt.Errorf("verify sandbox process open-file limit: %w", err)
	}
	if verified.current != requested || verified.maximum != requested {
		return fmt.Errorf("sandbox process open-file limit is soft=%d hard=%d after requesting %d", verified.current, verified.maximum, requested)
	}
	return nil
}

func prlimit64(pid int, newLimit, oldLimit *rlimit64) error {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRLIMIT64,
		uintptr(pid), uintptr(rlimitNoFile),
		uintptr(unsafe.Pointer(newLimit)), uintptr(unsafe.Pointer(oldLimit)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func findCgroup2Mount() (cgroupMount, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return cgroupMount{}, fmt.Errorf("read /proc/self/mountinfo: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || len(fields) <= separator+1 || fields[separator+1] != "cgroup2" {
			continue
		}
		return cgroupMount{
			root:      unescapeMountInfo(fields[3]),
			mountPath: filepath.Clean(unescapeMountInfo(fields[4])),
		}, nil
	}
	return cgroupMount{}, fmt.Errorf("no cgroup2 mount is visible")
}

func unescapeMountInfo(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}

func processCgroupPath(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", fmt.Errorf("read sandbox process cgroup membership: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) == 3 && fields[0] == "0" && fields[1] == "" && filepath.IsAbs(fields[2]) {
			return filepath.Clean(fields[2]), nil
		}
	}
	return "", fmt.Errorf("sandbox process is not in a unified cgroup v2 hierarchy")
}

func cgroupDirectory(mount cgroupMount, processPath string) (string, error) {
	relative, err := filepath.Rel(filepath.Clean(mount.root), filepath.Clean(processPath))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox process cgroup %q is outside cgroup2 mount root %q", processPath, mount.root)
	}
	directory := filepath.Join(mount.mountPath, relative)
	if _, err := os.Stat(directory); err != nil {
		return "", fmt.Errorf("inspect sandbox cgroup %q: %w", directory, err)
	}
	return directory, nil
}

func cgroupAncestors(mountPath, directory string) ([]string, error) {
	mountPath = filepath.Clean(mountPath)
	directory = filepath.Clean(directory)
	relative, err := filepath.Rel(mountPath, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("sandbox cgroup %q is outside cgroup2 mount %q", directory, mountPath)
	}
	ancestors := []string{directory}
	for current := directory; current != mountPath; {
		current = filepath.Dir(current)
		if current == ancestors[len(ancestors)-1] {
			return nil, fmt.Errorf("cannot walk cgroup ancestors from %q", directory)
		}
		ancestors = append(ancestors, current)
	}
	return ancestors, nil
}

func verifyOwnCPUQuota(directory string, requestedPercent uint64) error {
	quota, period, finite, err := readCPUQuota(directory)
	if err != nil {
		return fmt.Errorf("read configured CPU quota from cgroup: %w", err)
	}
	if !finite || !ratioEqualPercent(quota, period, requestedPercent) {
		return fmt.Errorf("cgroup did not apply profile cpu_quota_percent=%d exactly", requestedPercent)
	}
	return nil
}

func verifyParentCPUQuota(ancestors []string, requestedPercent uint64) error {
	for _, directory := range ancestors {
		quota, period, finite, err := readCPUQuota(directory)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read parent CPU quota from %q: %w", directory, err)
		}
		if finite && ratioLessPercent(quota, period, requestedPercent) {
			return fmt.Errorf("parent cgroup %q CPU quota is lower than requested cpu_quota_percent=%d", directory, requestedPercent)
		}
		cpuset, err := readCPUSetCount(directory)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read parent CPU set from %q: %w", directory, err)
		}
		if cpuset != 0 && cpuset*100 < requestedPercent {
			return fmt.Errorf("parent cgroup %q allows only %d CPU(s), below requested cpu_quota_percent=%d", directory, cpuset, requestedPercent)
		}
	}
	return nil
}

func verifyOwnNumericLimit(directory, name string, requested uint64, profileName string) error {
	value, finite, err := readNumericLimit(directory, name)
	if err != nil {
		return fmt.Errorf("read cgroup %s: %w", name, err)
	}
	if !finite || value != requested {
		return fmt.Errorf("cgroup %s is %s after requesting profile %s=%d", name, formatLimit(value, finite), profileName, requested)
	}
	return nil
}

func verifyParentNumericLimits(ancestors []string, name string, requested uint64, profileName string) error {
	for _, directory := range ancestors {
		value, finite, err := readNumericLimit(directory, name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read parent cgroup %s at %q: %w", name, directory, err)
		}
		if finite && value < requested {
			return fmt.Errorf("parent cgroup %q %s=%d is lower than requested %s=%d", directory, name, value, profileName, requested)
		}
	}
	return nil
}

func readCPUQuota(directory string) (quota, period uint64, finite bool, err error) {
	data, err := os.ReadFile(filepath.Join(directory, "cpu.max"))
	if err != nil {
		return 0, 0, false, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0, false, fmt.Errorf("invalid cpu.max value %q", strings.TrimSpace(string(data)))
	}
	period, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil || period == 0 {
		return 0, 0, false, fmt.Errorf("invalid cpu.max period %q", fields[1])
	}
	if fields[0] == "max" {
		return 0, period, false, nil
	}
	quota, err = strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, false, fmt.Errorf("invalid cpu.max quota %q", fields[0])
	}
	return quota, period, true, nil
}

func readNumericLimit(directory, name string) (uint64, bool, error) {
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		return 0, false, err
	}
	value := strings.TrimSpace(string(data))
	if value == "max" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid %s value %q", name, value)
	}
	return parsed, true, nil
}

func readCPUSetCount(directory string) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(directory, "cpuset.cpus.effective"))
	if err != nil {
		return 0, err
	}
	return parseCPUSetCount(strings.TrimSpace(string(data)))
}

func parseCPUSetCount(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	var count uint64
	for _, item := range strings.Split(value, ",") {
		firstText, lastText, hasRange := strings.Cut(item, "-")
		first, err := strconv.ParseUint(firstText, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid CPU set %q", value)
		}
		last := first
		if hasRange {
			last, err = strconv.ParseUint(lastText, 10, 64)
			if err != nil || last < first {
				return 0, fmt.Errorf("invalid CPU set %q", value)
			}
		}
		count += last - first + 1
	}
	return count, nil
}

func ratioEqualPercent(quota, period, percent uint64) bool {
	left := new(big.Int).Mul(new(big.Int).SetUint64(quota), big.NewInt(100))
	right := new(big.Int).Mul(new(big.Int).SetUint64(period), new(big.Int).SetUint64(percent))
	return left.Cmp(right) == 0
}

func ratioLessPercent(quota, period, percent uint64) bool {
	left := new(big.Int).Mul(new(big.Int).SetUint64(quota), big.NewInt(100))
	right := new(big.Int).Mul(new(big.Int).SetUint64(period), new(big.Int).SetUint64(percent))
	return left.Cmp(right) < 0
}

func formatLimit(value uint64, finite bool) string {
	if !finite {
		return "unlimited"
	}
	return strconv.FormatUint(value, 10)
}
