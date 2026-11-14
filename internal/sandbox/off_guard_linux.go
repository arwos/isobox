//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	capNetAdmin        = 12
	capSysAdmin        = 21
	capNetAdminMask    = uint64(1) << capNetAdmin
	capSysAdminMask    = uint64(1) << capSysAdmin
	capSetupMask       = capNetAdminMask | capSysAdminMask
	capVersion3        = 0x20080522
	prSetNoNewPrivs    = 38
	prCapAmbient       = 47
	prCapAmbientRaise  = 2
	prCapAmbientClear  = 4
	cloneNewNet        = 0x40000000
	capabilitySetsSize = 2
)

type capUserHeader struct {
	version uint32
	pid     int32
}

type capUserData struct {
	effective   uint32
	permitted   uint32
	inheritable uint32
}

// RunOffGuardHelper handles the private bwrap command used to install the
// egress policy before replacing itself with the application.
func RunOffGuardHelper(args []string) (int, bool) {
	if len(args) == 0 || args[0] != internalOffGuardCommand {
		return 0, false
	}
	if len(args) < 8 || !filepath.IsAbs(args[1]) || args[4] != "--gate-path" || args[5] != internalOffGuardGatePath || args[6] != "--" || len(args[7]) == 0 {
		fmt.Fprintln(os.Stderr, "isobox: invalid internal off-network guard arguments")
		return 125, true
	}
	tcpPorts, err := parsePortSet(args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: invalid internal TCP port set: %v\n", err)
		return 125, true
	}
	udpPorts, err := parsePortSet(args[3])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: invalid internal UDP port set: %v\n", err)
		return 125, true
	}
	if len(tcpPorts) == 0 && len(udpPorts) == 0 {
		fmt.Fprintln(os.Stderr, "isobox: refusing to run the firewall helper without an announced host port")
		return 125, true
	}

	// Capabilities, no_new_privs, and seccomp filters are per-thread. Keep the
	// setup goroutine on one thread through the final exec so the application
	// inherits exactly the thread whose privileges were reduced.
	runtime.LockOSThread()
	if err := verifyGuardCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot safely install the off-network egress filter: %v\n", err)
		return 125, true
	}
	if err := setNoNewPrivs(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: set no_new_privs before network setup: %v\n", err)
		return 125, true
	}
	if err := unshareNetworkNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot create the isolated network namespace: %v\n", err)
		return 125, true
	}
	if err := disableIPv6InNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot disable IPv6 in the off-mode network namespace: %v\n", err)
		return 125, true
	}
	if err := enableNftCapabilityInheritance(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot pass firewall capability to nft: %v\n", err)
		return 125, true
	}
	filterErr := installOffFilter(args[1], tcpPorts, udpPorts)
	if err := clearAmbientCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot clear inherited firewall capabilities after nft: %v\n", err)
		return 125, true
	}
	if filterErr != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot install the off-network egress filter: %v\n", filterErr)
		return 125, true
	}

	program, err := exec.LookPath(args[7])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: application executable %q cannot be found inside the sandbox: %v\n", args[7], err)
		return 127, true
	}
	if err := dropCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot drop firewall capabilities before application launch: %v\n", err)
		return 125, true
	}
	gate, err := net.Dial("unix", args[5])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot connect to the host startup gate: %v\n", err)
		return 125, true
	}
	if _, err := io.WriteString(gate, "ready\n"); err != nil {
		_ = gate.Close()
		fmt.Fprintf(os.Stderr, "isobox: cannot announce the isolated network namespace: %v\n", err)
		return 125, true
	}
	if err := installNamespaceSeccomp(); err != nil {
		_ = gate.Close()
		fmt.Fprintf(os.Stderr, "isobox: cannot prevent nested namespace creation before application launch: %v\n", err)
		return 125, true
	}
	if err := waitForStartupGate(gate); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: startup aborted before the network backend was ready: %v\n", err)
		return 125, true
	}
	if err := os.Unsetenv("GOMAXPROCS"); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot remove the internal Go runtime setting before application launch: %v\n", err)
		return 125, true
	}
	if err := syscall.Exec(program, args[7:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot execute application %q: %v\n", args[7], err)
		return 126, true
	}
	return 0, true
}

// RunIPv4NetworkHelper disables IPv6 inside the bwrap network namespace,
// drops the temporary network capability, and replaces itself with the app.
func RunIPv4NetworkHelper(args []string) (int, bool) {
	if len(args) == 0 || args[0] != internalIPv4NetworkCommand {
		return 0, false
	}
	applicationIndex := 2
	waitForNetwork := false
	if len(args) > 1 && args[1] == "--wait-for-network" {
		waitForNetwork = true
		applicationIndex = 3
	}
	if len(args) <= applicationIndex || args[applicationIndex-1] != "--" || args[applicationIndex] == "" {
		fmt.Fprintln(os.Stderr, "isobox: invalid internal IPv4-network helper arguments")
		return 125, true
	}

	runtime.LockOSThread()
	if err := verifyIPv4SetupCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot disable IPv6 in the sandbox: %v\n", err)
		return 125, true
	}
	if err := setNoNewPrivs(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: set no_new_privs before network setup: %v\n", err)
		return 125, true
	}
	if err := unshareNetworkNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot create the isolated network namespace: %v\n", err)
		return 125, true
	}
	if err := disableIPv6InNamespace(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot disable IPv6 in the sandbox: %v\n", err)
		return 125, true
	}
	if waitForNetwork {
		gate, err := net.Dial("unix", internalOffGuardGatePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "isobox: cannot connect to the network startup gate: %v\n", err)
			return 125, true
		}
		if _, err := io.WriteString(gate, "ready\n"); err != nil {
			_ = gate.Close()
			fmt.Fprintf(os.Stderr, "isobox: cannot announce the isolated network namespace: %v\n", err)
			return 125, true
		}
		if err := waitForStartupGate(gate); err != nil {
			fmt.Fprintf(os.Stderr, "isobox: network startup was aborted: %v\n", err)
			return 125, true
		}
	}
	if err := clearAmbientCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot clear temporary network capabilities: %v\n", err)
		return 125, true
	}
	if err := dropCapabilities(); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot drop temporary network capabilities before application launch: %v\n", err)
		return 125, true
	}
	program, err := exec.LookPath(args[applicationIndex])
	if err != nil {
		fmt.Fprintf(os.Stderr, "isobox: application executable %q cannot be found inside the sandbox: %v\n", args[applicationIndex], err)
		return 127, true
	}
	if err := os.Unsetenv("GOMAXPROCS"); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot remove the internal Go runtime setting before application launch: %v\n", err)
		return 125, true
	}
	if err := syscall.Exec(program, args[applicationIndex:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "isobox: cannot execute application %q: %v\n", args[applicationIndex], err)
		return 126, true
	}
	return 0, true
}

func verifyIPv4SetupCapabilities() error {
	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		return fmt.Errorf("read process capability state: %w", err)
	}
	var effective, permitted, inheritable, ambient string
	for _, line := range strings.Split(string(status), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch name {
		case "CapEff":
			effective = strings.TrimSpace(value)
		case "CapPrm":
			permitted = strings.TrimSpace(value)
		case "CapInh":
			inheritable = strings.TrimSpace(value)
		case "CapAmb":
			ambient = strings.TrimSpace(value)
		}
	}
	if effective == "" || permitted == "" || inheritable == "" || ambient == "" {
		return fmt.Errorf("process capability state is incomplete")
	}
	eff, err := strconv.ParseUint(effective, 16, 64)
	if err != nil {
		return fmt.Errorf("parse effective capabilities: %w", err)
	}
	prm, err := strconv.ParseUint(permitted, 16, 64)
	if err != nil {
		return fmt.Errorf("parse permitted capabilities: %w", err)
	}
	inh, err := strconv.ParseUint(inheritable, 16, 64)
	if err != nil {
		return fmt.Errorf("parse inheritable capabilities: %w", err)
	}
	amb, err := strconv.ParseUint(ambient, 16, 64)
	if err != nil {
		return fmt.Errorf("parse ambient capabilities: %w", err)
	}
	if eff != capSetupMask || prm != capSetupMask || inh&^capSetupMask != 0 || amb&^capSetupMask != 0 || amb&^inh != 0 {
		return fmt.Errorf("expected only CAP_SYS_ADMIN and CAP_NET_ADMIN for IPv6 setup, effective=%s permitted=%s inheritable=%s ambient=%s", effective, permitted, inheritable, ambient)
	}
	return nil
}

func disableIPv6InNamespace() error {
	for _, path := range []string{
		"/proc/sys/net/ipv6/conf/default/disable_ipv6",
		"/proc/sys/net/ipv6/conf/all/disable_ipv6",
	} {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		if _, err := io.WriteString(file, "1\n"); err != nil {
			_ = file.Close()
			return fmt.Errorf("write %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s: %w", path, err)
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("verify %s: %w", path, err)
		}
		if strings.TrimSpace(string(value)) != "1" {
			return fmt.Errorf("%s remained %q after setting it to 1", path, strings.TrimSpace(string(value)))
		}
	}
	addresses, err := os.ReadFile("/proc/net/if_inet6")
	if err != nil {
		return fmt.Errorf("verify IPv6 interface addresses: %w", err)
	}
	if strings.TrimSpace(string(addresses)) != "" {
		return fmt.Errorf("IPv6 addresses remain after disabling IPv6: %s", strings.TrimSpace(string(addresses)))
	}
	return nil
}

func verifyGuardCapabilities() error {
	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		return fmt.Errorf("read process capability state: %w", err)
	}
	var effective, permitted, inheritable, ambient string
	for _, line := range strings.Split(string(status), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch name {
		case "CapEff":
			effective = strings.TrimSpace(value)
		case "CapPrm":
			permitted = strings.TrimSpace(value)
		case "CapInh":
			inheritable = strings.TrimSpace(value)
		case "CapAmb":
			ambient = strings.TrimSpace(value)
		}
	}
	if effective == "" || permitted == "" || inheritable == "" || ambient == "" {
		return fmt.Errorf("process capability state is incomplete")
	}
	eff, err := strconv.ParseUint(effective, 16, 64)
	if err != nil {
		return fmt.Errorf("parse effective capabilities: %w", err)
	}
	prm, err := strconv.ParseUint(permitted, 16, 64)
	if err != nil {
		return fmt.Errorf("parse permitted capabilities: %w", err)
	}
	inh, err := strconv.ParseUint(inheritable, 16, 64)
	if err != nil {
		return fmt.Errorf("parse inheritable capabilities: %w", err)
	}
	amb, err := strconv.ParseUint(ambient, 16, 64)
	if err != nil {
		return fmt.Errorf("parse ambient capabilities: %w", err)
	}
	if eff != capSetupMask || prm != capSetupMask || inh&^capSetupMask != 0 || amb&^capSetupMask != 0 || amb&^inh != 0 {
		return fmt.Errorf("expected only CAP_SYS_ADMIN and CAP_NET_ADMIN for namespace setup, effective=%s permitted=%s inheritable=%s ambient=%s", effective, permitted, inheritable, ambient)
	}
	return nil
}

func unshareNetworkNamespace() error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_UNSHARE, cloneNewNet, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// enableNftCapabilityInheritance gives only the nft child CAP_NET_ADMIN after
// no_new_privs has been set. Without an ambient capability, exec would strip
// the helper's namespace capability before nft can install the filter.
func enableNftCapabilityInheritance() error {
	header := capUserHeader{version: capVersion3}
	data := [capabilitySetsSize]capUserData{{
		effective:   uint32(capNetAdminMask),
		permitted:   uint32(capNetAdminMask),
		inheritable: uint32(capNetAdminMask),
	}}
	_, _, errno := syscall.RawSyscall(syscall.SYS_CAPSET,
		uintptr(unsafe.Pointer(&header)),
		uintptr(unsafe.Pointer(&data[0])),
		0,
	)
	runtime.KeepAlive(&header)
	runtime.KeepAlive(&data)
	if errno != 0 {
		return fmt.Errorf("drop CAP_SYS_ADMIN and prepare CAP_NET_ADMIN inheritance: %w", errno)
	}
	if err := clearAmbientCapabilities(); err != nil {
		return fmt.Errorf("clear ambient capabilities before nft: %w", err)
	}
	_, _, errno = syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientRaise, capNetAdmin, 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("raise CAP_NET_ADMIN ambient: %w", errno)
	}
	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		return fmt.Errorf("verify nft capability inheritance: %w", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || (name != "CapEff" && name != "CapPrm" && name != "CapInh" && name != "CapAmb") {
			continue
		}
		caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return fmt.Errorf("parse capability field %s: %w", name, err)
		}
		if caps != capNetAdminMask {
			return fmt.Errorf("expected only CAP_NET_ADMIN in %s after enabling nft inheritance", name)
		}
	}
	return nil
}

func waitForStartupGate(gate net.Conn) error {
	defer gate.Close()
	var signal [1]byte
	if _, err := io.ReadFull(gate, signal[:]); err != nil {
		return err
	}
	if signal[0] != 1 {
		return fmt.Errorf("invalid startup gate signal")
	}
	return nil
}

func clearAmbientCapabilities() error {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prCapAmbient, prCapAmbientClear, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		return fmt.Errorf("verify cleared ambient capabilities: %w", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if !strings.HasPrefix(line, "CapAmb:") {
			continue
		}
		caps, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "CapAmb:")), 16, 64)
		if err != nil {
			return fmt.Errorf("parse ambient capabilities after nft: %w", err)
		}
		if caps != 0 {
			return fmt.Errorf("ambient capabilities remain after nft")
		}
		return nil
	}
	return fmt.Errorf("ambient capability state is missing after nft")
}

func parsePortSet(value string) ([]string, error) {
	if value == "-" {
		return nil, nil
	}
	if value == "" {
		return nil, fmt.Errorf("empty port set")
	}
	seen := make(map[string]struct{})
	var ports []string
	for _, item := range strings.Split(value, ",") {
		startText, endText, hasRange := strings.Cut(item, "-")
		start, err := strconv.Atoi(startText)
		if err != nil || start < 1 || start > 65535 {
			return nil, fmt.Errorf("invalid port or range %q", item)
		}
		end := start
		if hasRange {
			end, err = strconv.Atoi(endText)
			if err != nil || end < start || end > 65535 {
				return nil, fmt.Errorf("invalid port or range %q", item)
			}
		}
		canonical := strconv.Itoa(start)
		if end != start {
			canonical += "-" + strconv.Itoa(end)
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		ports = append(ports, canonical)
	}
	return ports, nil
}

func installOffFilter(nftPath string, tcpPorts, udpPorts []string) error {
	command := exec.Command(nftPath, "-f", "-")
	command.Stdin = bytes.NewBufferString(buildOffFilterRules(tcpPorts, udpPorts))
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft -f - failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func buildOffFilterRules(tcpPorts, udpPorts []string) string {
	var rules strings.Builder
	rules.WriteString("table inet isobox {\n")
	rules.WriteString(" chain input { type filter hook input priority -150; policy drop;\n")
	rules.WriteString("  iifname \"lo\" accept\n")
	rules.WriteString("  ct state established accept\n")
	if len(tcpPorts) != 0 {
		rules.WriteString("  tcp dport { " + strings.Join(tcpPorts, ", ") + " } ct state new accept\n")
	}
	if len(udpPorts) != 0 {
		rules.WriteString("  udp dport { " + strings.Join(udpPorts, ", ") + " } ct state new accept\n")
	}
	rules.WriteString(" }\n")
	rules.WriteString(" chain output { type filter hook output priority -150; policy drop;\n")
	rules.WriteString("  oifname \"lo\" accept\n")
	rules.WriteString("  ct state established accept\n")
	rules.WriteString(" }\n")
	rules.WriteString(" chain forward { type filter hook forward priority -150; policy drop; }\n")
	rules.WriteString("}\n")
	return rules.String()
}

func dropCapabilities() error {
	header := capUserHeader{version: capVersion3}
	data := [capabilitySetsSize]capUserData{}
	_, _, errno := syscall.RawSyscall(syscall.SYS_CAPSET,
		uintptr(unsafe.Pointer(&header)),
		uintptr(unsafe.Pointer(&data[0])),
		0,
	)
	runtime.KeepAlive(&header)
	runtime.KeepAlive(&data)
	if errno != 0 {
		return errno
	}

	status, err := os.ReadFile("/proc/thread-self/status")
	if err != nil {
		return fmt.Errorf("verify dropped capabilities: %w", err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || (name != "CapEff" && name != "CapPrm" && name != "CapInh" && name != "CapAmb") {
			continue
		}
		caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
		if err != nil {
			return fmt.Errorf("parse dropped capability field %s: %w", name, err)
		}
		if caps != 0 {
			return fmt.Errorf("%s remains non-zero after capability drop", name)
		}
	}
	return nil
}

func setNoNewPrivs() error {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

type sockFilter struct {
	code uint16
	jt   uint8
	jf   uint8
	k    uint32
}

type sockFprog struct {
	length uint16
	filter *sockFilter
}

func installNamespaceSeccomp() error {
	arch, cloneSyscall, unshareSyscall, setnsSyscall := namespaceSyscallNumbers()
	if arch == 0 {
		return fmt.Errorf("unsupported Linux architecture %q", runtime.GOARCH)
	}
	const (
		bpfLoadWordAbsolute = 0x20
		bpfJumpEqual        = 0x15
		bpfJumpSet          = 0x45
		bpfReturn           = 0x06
		seccompAllow        = 0x7fff0000
		seccompErrno        = 0x00050000
		cloneNewUser        = 0x10000000
		x32SyscallBit       = 0x40000000
		errnoPermission     = 1
		errnoNotImplemented = 38
		prSetSeccomp        = 22
		seccompModeFilter   = 2
	)
	clone3Syscall := uint32(435)
	filter := []sockFilter{
		{code: bpfLoadWordAbsolute, k: 4},
		{code: bpfJumpEqual, jt: 1, k: arch},
		{code: bpfReturn, k: seccompErrno | errnoPermission},
		{code: bpfLoadWordAbsolute, k: 0},
		{code: bpfJumpEqual, jf: 4, k: cloneSyscall},
		{code: bpfLoadWordAbsolute, k: 16},
		{code: bpfJumpSet, jf: 1, k: cloneNewUser},
		{code: bpfReturn, k: seccompErrno | errnoPermission},
		{code: bpfReturn, k: seccompAllow},
		{code: bpfLoadWordAbsolute, k: 0},
		{code: bpfJumpEqual, jf: 1, k: unshareSyscall},
		{code: bpfReturn, k: seccompErrno | errnoPermission},
		{code: bpfLoadWordAbsolute, k: 0},
		{code: bpfJumpEqual, jf: 1, k: setnsSyscall},
		{code: bpfReturn, k: seccompErrno | errnoPermission},
		{code: bpfLoadWordAbsolute, k: 0},
		{code: bpfJumpEqual, jf: 1, k: clone3Syscall},
		{code: bpfReturn, k: seccompErrno | errnoNotImplemented},
		{code: bpfLoadWordAbsolute, k: 0},
		{code: bpfJumpSet, jf: 1, k: x32SyscallBit},
		{code: bpfReturn, k: seccompErrno | errnoPermission},
		{code: bpfReturn, k: seccompAllow},
	}
	program := sockFprog{length: uint16(len(filter)), filter: &filter[0]}
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetSeccomp, seccompModeFilter,
		uintptr(unsafe.Pointer(&program)), 0, 0, 0)
	runtime.KeepAlive(&filter)
	runtime.KeepAlive(&program)
	if errno != 0 {
		return errno
	}
	return nil
}

func namespaceSyscallNumbers() (arch, clone, unshare, setns uint32) {
	clone = uint32(syscall.SYS_CLONE)
	unshare = uint32(syscall.SYS_UNSHARE)
	switch runtime.GOARCH {
	case "amd64":
		arch = 0xc000003e
		setns = 308
	case "arm64":
		arch = 0xc00000b7
		setns = 268
	case "386":
		arch = 0x40000003
		setns = 346
	case "arm":
		arch = 0x40000028
		setns = 375
	case "ppc64":
		arch = 0x80000015
		setns = 350
	case "ppc64le":
		arch = 0xc0000015
		setns = 350
	case "s390x":
		arch = 0x80000016
		setns = 339
	case "riscv64":
		arch = 0xc00000f3
		setns = 268
	}
	return arch, clone, unshare, setns
}
