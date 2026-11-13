//go:build linux

package limits

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arwos/isobox/internal/profile"
)

func TestScopeProperties(t *testing.T) {
	tests := []struct {
		name    string
		request profile.ResourceLimits
		want    []string
	}{
		{name: "none"},
		{
			name:    "all controllers",
			request: profile.ResourceLimits{CPUQuotaPercent: 125, MemoryBytes: 1 << 20, ProcessCount: 8},
			want:    []string{"CPUQuota=125%", "MemoryMax=1048576", "TasksMax=10"},
		},
		{name: "process count reserves both bwrap tasks", request: profile.ResourceLimits{ProcessCount: 1}, want: []string{"TasksMax=3"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ScopeProperties(test.request)
			if err != nil {
				t.Fatalf("ScopeProperties() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ScopeProperties() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestScopePropertiesRejectsProcessCountOverflow(t *testing.T) {
	for _, count := range []uint64{^uint64(0) - 1, ^uint64(0)} {
		_, err := ScopeProperties(profile.ResourceLimits{ProcessCount: count})
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("ScopeProperties(process_count=%d) error = %v, want process-count overflow", count, err)
		}
	}
}

func TestOffGuardBootstrapSlackLeavesExactApplicationBudget(t *testing.T) {
	tests := []struct {
		processCount uint64
		wantSlack    uint64
	}{
		{processCount: 0, wantSlack: 0},
		{processCount: 1, wantSlack: 4},
		{processCount: 4, wantSlack: 1},
		{processCount: 5, wantSlack: 0},
	}
	for _, test := range tests {
		request := profile.ResourceLimits{ProcessCount: test.processCount}
		if got := OffGuardBootstrapSlack(request); got != test.wantSlack {
			t.Errorf("OffGuardBootstrapSlack(%d) = %d, want %d", test.processCount, got, test.wantSlack)
		}
	}
	properties, err := scopeProperties(profile.ResourceLimits{ProcessCount: 4}, OffGuardBootstrapSlack(profile.ResourceLimits{ProcessCount: 4}))
	if err != nil || !reflect.DeepEqual(properties, []string{"TasksMax=7"}) {
		t.Fatalf("bootstrap scope properties = %#v, %v; want temporary TasksMax=7", properties, err)
	}
}

func TestScopeCommandSetsUniqueUnitAndBootstrapSlack(t *testing.T) {
	request := profile.ResourceLimits{ProcessCount: 4}
	command, unit, err := ScopeCommand(exec.Command("/usr/bin/bwrap"), "/usr/bin/systemd-run", request, 1)
	if err != nil {
		t.Fatalf("ScopeCommand() error = %v", err)
	}
	if !strings.HasSuffix(unit, ".scope") {
		t.Fatalf("ScopeCommand() unit = %q, want a scope unit", unit)
	}
	want := []string{"--unit=" + unit, "--property=TasksMax=7"}
	for _, value := range want {
		found := false
		for _, arg := range command.Args {
			if arg == value {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ScopeCommand() arguments omit %q: %#v", value, command.Args)
		}
	}
}

func TestReadCPUQuota(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		quota    uint64
		period   uint64
		finite   bool
		wantErr  bool
	}{
		{name: "finite", contents: "125000 100000\n", quota: 125000, period: 100000, finite: true},
		{name: "unlimited", contents: "max 100000\n", period: 100000},
		{name: "zero period", contents: "1000 0\n", wantErr: true},
		{name: "invalid fields", contents: "1000\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeLimitFile(t, directory, "cpu.max", test.contents)
			quota, period, finite, err := readCPUQuota(directory)
			if (err != nil) != test.wantErr {
				t.Fatalf("readCPUQuota() error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && (quota != test.quota || period != test.period || finite != test.finite) {
				t.Fatalf("readCPUQuota() = (%d, %d, %t), want (%d, %d, %t)", quota, period, finite, test.quota, test.period, test.finite)
			}
		})
	}
}

func TestReadNumericLimit(t *testing.T) {
	directory := t.TempDir()
	writeLimitFile(t, directory, "memory.max", "1048576\n")
	value, finite, err := readNumericLimit(directory, "memory.max")
	if err != nil || !finite || value != 1048576 {
		t.Fatalf("readNumericLimit(finite) = (%d, %t, %v), want (1048576, true, nil)", value, finite, err)
	}
	writeLimitFile(t, directory, "pids.max", "max\n")
	value, finite, err = readNumericLimit(directory, "pids.max")
	if err != nil || finite || value != 0 {
		t.Fatalf("readNumericLimit(unlimited) = (%d, %t, %v), want (0, false, nil)", value, finite, err)
	}
}

func TestReadCPUSetCount(t *testing.T) {
	tests := []struct {
		value   string
		want    uint64
		wantErr bool
	}{
		{value: "0-2,4,7-8", want: 6},
		{value: "", want: 0},
		{value: "2-1", wantErr: true},
		{value: "0-x", wantErr: true},
	}
	for _, test := range tests {
		got, err := parseCPUSetCount(test.value)
		if (err != nil) != test.wantErr {
			t.Fatalf("parseCPUSetCount(%q) error = %v, wantErr %v", test.value, err, test.wantErr)
		}
		if err == nil && got != test.want {
			t.Fatalf("parseCPUSetCount(%q) = %d, want %d", test.value, got, test.want)
		}
	}
}

func TestParentLimitVerificationFailsClosed(t *testing.T) {
	parent := t.TempDir()
	writeLimitFile(t, parent, "memory.max", "1024\n")
	if err := verifyParentNumericLimits([]string{parent}, "memory.max", 2048, "memory_bytes"); err == nil {
		t.Fatal("verifyParentNumericLimits() accepted a stricter parent memory.max")
	}
	writeLimitFile(t, parent, "pids.max", "max\n")
	if err := verifyParentNumericLimits([]string{parent}, "pids.max", 8, "process_count"); err != nil {
		t.Fatalf("verifyParentNumericLimits() rejected unlimited parent: %v", err)
	}
	writeLimitFile(t, parent, "cpu.max", "200000 100000\n")
	writeLimitFile(t, parent, "cpuset.cpus.effective", "0-1\n")
	if err := verifyParentCPUQuota([]string{parent}, 150); err != nil {
		t.Fatalf("verifyParentCPUQuota() rejected sufficient parent: %v", err)
	}
	writeLimitFile(t, parent, "cpuset.cpus.effective", "0\n")
	if err := verifyParentCPUQuota([]string{parent}, 150); err == nil {
		t.Fatal("verifyParentCPUQuota() accepted a parent cpuset below the requested quota")
	}
}

func TestCgroupAncestors(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "user.slice", "scope.slice")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := cgroupAncestors(root, child)
	if err != nil {
		t.Fatalf("cgroupAncestors() error = %v", err)
	}
	want := []string{child, filepath.Join(root, "user.slice"), root}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cgroupAncestors() = %#v, want %#v", got, want)
	}
	if _, err := cgroupAncestors(root, filepath.Join(root, "..", "outside")); err == nil {
		t.Fatal("cgroupAncestors() accepted a directory outside the mount")
	}
}

func writeLimitFile(t *testing.T, directory, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
