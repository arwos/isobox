/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

// Package preflight checks host capabilities required by an isobox profile.
package preflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/profile"
)

// Capabilities contains discovered host tools and the network plan selected
// after checking that the installed helper can preserve profile semantics.
type Capabilities struct {
	Bubblewrap           string
	NFT                  string
	Pasta                string
	PastaOptions         map[string]bool
	PastaInspectionError string
	Slirp4netns          string
	Slirp4netnsOptions   map[string]bool
	SlirpInspectionError string
	SystemdRun           string
	Network              BackendSelection
}

var longOptionPattern = regexp.MustCompile(`--[a-zA-Z0-9][a-zA-Z0-9-]*`)

type optionProbe struct {
	option string
	args   []string
}

// Some pasta releases cap --help at 4096 bytes, hiding options that their
// parser still supports. Probe options missing from help with --help last so
// the parser validates the flag without starting a network process.
var pastaOptionProbes = []optionProbe{
	{option: "--config-net", args: []string{"--config-net"}},
	{option: "--address", args: []string{"--address=10.89.1.2"}},
	{option: "--netmask", args: []string{"--netmask=24"}},
	{option: "--gateway", args: []string{"--gateway=10.89.1.1"}},
	{option: "--udp-ports", args: []string{"--udp-ports=none"}},
	{option: "--tcp-ns", args: []string{"--tcp-ns=none"}},
	{option: "--udp-ns", args: []string{"--udp-ns=none"}},
	{option: "--ns-ifname", args: []string{"--ns-ifname=isoboxp0"}},
	{option: "--no-splice", args: []string{"--no-splice"}},
	{option: "--no-dhcp", args: []string{"--no-dhcp"}},
	{option: "--no-dhcpv6", args: []string{"--no-dhcpv6"}},
	{option: "--no-ndp", args: []string{"--no-ndp"}},
	{option: "--no-ra", args: []string{"--no-ra"}},
	{option: "--no-tcp", args: []string{"--no-tcp"}},
	{option: "--no-udp", args: []string{"--no-udp"}},
	{option: "--ipv4-only", args: []string{"--ipv4-only"}},
	{option: "--pid", args: []string{"--pid=/dev/null"}},
	{option: "--dns-forward", args: []string{"--dns-forward=10.0.2.3"}},
	{option: "--dns-host", args: []string{"--dns-host=127.0.0.1"}},
}

// Check verifies host prerequisites before any sandbox or application process
// is started. It accumulates independent failures so one run can report the
// full set of host changes the user needs to make.
func Check(config profile.EffectiveProfile) (Capabilities, error) {
	return check(config, false)
}

// CheckInRootlessUserNamespace is used only by grouped launches started by the
// rootless network manager. The current process must be UID 0 mapped to the
// ordinary host user; host-root execution remains rejected.
func CheckInRootlessUserNamespace(config profile.EffectiveProfile) (Capabilities, error) {
	if !isMappedRootlessUser() {
		return Capabilities{}, fmt.Errorf("grouped launch preflight requires the manager's rootless user namespace")
	}
	return check(config, true)
}

func check(config profile.EffectiveProfile, allowMappedRoot bool) (Capabilities, error) {
	var capabilities Capabilities
	var issues []string
	if runtime.GOOS != "linux" {
		return capabilities, fmt.Errorf("host preflight failed:\n- isobox requires Linux; current operating system is %s", runtime.GOOS)
	}
	if os.Geteuid() == 0 && !allowMappedRoot {
		issues = append(issues, "isobox must run as an ordinary unprivileged user; do not launch it as root")
	}

	var err error
	capabilities.Bubblewrap, err = exec.LookPath("bwrap")
	if err != nil {
		issues = append(issues, "bwrap was not found in PATH; install bubblewrap for this distribution")
	} else if err := checkBubblewrap(capabilities.Bubblewrap); err != nil {
		issues = append(issues, fmt.Sprintf("bwrap is not usable: %v; %s", err, bubblewrapFailureAdvice(err)))
	}

	if err := probeUserNamespace(); err != nil {
		issues = append(issues, fmt.Sprintf("an unprivileged user namespace could not be created: %v; enable user namespaces using the distribution's documented setting (some systems use kernel.unprivileged_userns_clone=1); do not run isobox as root to bypass this check", err))
	}

	capabilities.Pasta, _ = exec.LookPath("pasta")
	capabilities.Slirp4netns, _ = exec.LookPath("slirp4netns")
	needsNetworkBackend := config.NetworkMode == profile.NetworkModeDirect || len(config.Network.Ports) != 0
	if needsNetworkBackend && capabilities.Pasta == "" && capabilities.Slirp4netns == "" {
		issues = append(issues, "neither pasta nor slirp4netns was found in PATH; install passt (pasta) or slirp4netns and retry")
	}
	if capabilities.Pasta != "" {
		capabilities.PastaOptions, capabilities.PastaInspectionError = inspectOptions(capabilities.Pasta, pastaOptionProbes...)
	}
	if capabilities.Slirp4netns != "" {
		capabilities.Slirp4netnsOptions, capabilities.SlirpInspectionError = inspectOptions(capabilities.Slirp4netns)
	}
	capabilities.Network, err = SelectBackend(config, capabilities)
	if err != nil {
		issues = append(issues, fmt.Sprintf("network backend cannot preserve the profile: %v", err))
	}
	requiresIngressGuard := (config.NetworkMode == profile.NetworkModeOff || config.NetworkMode == profile.NetworkModeProxy || config.NetworkMode == profile.NetworkModeEgress) && len(config.Network.Ports) != 0
	if requiresIngressGuard {
		capabilities.NFT, err = exec.LookPath("nft")
		if err != nil {
			issues = append(issues, fmt.Sprintf("network mode %s with published ports requires nftables (nft) to enforce the egress filter; install nftables and retry", config.NetworkMode))
		} else if nftPath, resolveErr := filepath.EvalSymlinks(capabilities.NFT); resolveErr != nil {
			issues = append(issues, fmt.Sprintf("cannot resolve nft executable %q: %v", capabilities.NFT, resolveErr))
		} else if !isSandboxSystemPath(nftPath) {
			issues = append(issues, fmt.Sprintf("nft executable %q is outside the read-only system paths mounted into the sandbox; install nftables under /usr, /bin, or /sbin", nftPath))
		} else {
			capabilities.NFT = nftPath
		}
		if capabilities.Bubblewrap != "" {
			options, inspectErr := inspectOptions(capabilities.Bubblewrap)
			if inspectErr != "" {
				issues = append(issues, fmt.Sprintf("cannot verify bwrap network-guard options: %s", inspectErr))
			} else if config.NetworkMode == profile.NetworkModeOff && !hasAllOptions(options, "--cap-add", "--block-fd", "--disable-userns", "--assert-userns-disabled") {
				issues = append(issues, "installed bwrap lacks the options required for the off-mode egress guard; update bubblewrap and retry")
			}
		}
	}

	if config.NetworkMode == profile.NetworkModeProxy || config.NetworkMode == profile.NetworkModeEgress {
		modeLabel := string(config.NetworkMode)
		if err := probeTUN(); err != nil {
			issues = append(issues, fmt.Sprintf("%s mode cannot create a TUN interface in a user network namespace: %v; make the kernel TUN driver and /dev/net/tun available, allow the current user to open the device, and permit CAP_NET_ADMIN in the new network namespace", modeLabel, err))
		}
		if capabilities.Bubblewrap != "" {
			options, inspectErr := inspectOptions(capabilities.Bubblewrap)
			if inspectErr != "" {
				issues = append(issues, fmt.Sprintf("cannot verify bwrap proxy TUN setup options: %s", inspectErr))
			} else if !hasAllOptions(options, "--cap-add", "--dev-bind", "--ro-bind-data", "--json-status-fd") {
				issues = append(issues, "installed bwrap lacks options required to create the proxy TUN interface and hold application startup until the network data plane is ready; update bubblewrap and retry")
			}
		}
		if len(config.Network.Ports) != 0 && capabilities.Bubblewrap != "" {
			options, inspectErr := inspectOptions(capabilities.Bubblewrap)
			if inspectErr != "" {
				issues = append(issues, fmt.Sprintf("cannot verify bwrap proxy ingress-guard options: %s", inspectErr))
			} else if !hasAllOptions(options, "--cap-add") {
				issues = append(issues, "installed bwrap lacks options required for the proxy ingress egress guard; update bubblewrap and retry")
			}
		}
	}
	if config.NetworkMode == profile.NetworkModeDirect && capabilities.Bubblewrap != "" {
		options, inspectErr := inspectOptions(capabilities.Bubblewrap)
		if inspectErr != "" {
			issues = append(issues, fmt.Sprintf("cannot verify bwrap direct-network startup options: %s", inspectErr))
		} else if !hasAllOptions(options, "--ro-bind-data", "--json-status-fd", "--disable-userns", "--assert-userns-disabled", "--cap-add") {
			issues = append(issues, "installed bwrap lacks the resolver, startup-status, or capability options required for direct IPv4 network mode; update bubblewrap and retry")
		}
	}
	if config.NetworkMode == profile.NetworkModeOff && len(config.Network.Ports) == 0 && capabilities.Bubblewrap != "" {
		options, inspectErr := inspectOptions(capabilities.Bubblewrap)
		if inspectErr != "" {
			issues = append(issues, fmt.Sprintf("cannot verify bwrap IPv4-only setup options: %s", inspectErr))
		} else if !hasAllOptions(options, "--cap-add") {
			issues = append(issues, "installed bwrap lacks the capability option required to disable IPv6 before the application starts; update bubblewrap and retry")
		}
	}
	if (config.NetworkMode == profile.NetworkModeOff || config.NetworkMode == profile.NetworkModeProxy || config.NetworkMode == profile.NetworkModeEgress) && len(config.Network.Ports) != 0 && config.Limits.ProcessCount != 0 {
		if _, err := exec.LookPath("systemctl"); err != nil {
			issues = append(issues, fmt.Sprintf("network mode %s with published ports and process_count requires systemctl to remove the temporary firewall-setup task allowance before application startup; install systemd or remove process_count", config.NetworkMode))
		}
	}

	capabilities.SystemdRun, err = limits.CheckHost(config.Limits)
	if err != nil {
		issues = append(issues, err.Error())
	}
	if limits.Any(config.Limits) && capabilities.Bubblewrap != "" {
		options, inspectErr := inspectOptions(capabilities.Bubblewrap)
		if inspectErr != "" {
			issues = append(issues, fmt.Sprintf("cannot verify bwrap resource-limit startup gate: %s", inspectErr))
		} else if !hasAllOptions(options, "--block-fd", "--json-status-fd") {
			issues = append(issues, "installed bwrap lacks the startup-gate or status options required to apply resource limits before the application starts; update bubblewrap and retry")
		}
	}

	if len(issues) > 0 {
		var message strings.Builder
		message.WriteString("host preflight failed:")
		for _, issue := range issues {
			message.WriteString("\n- ")
			message.WriteString(issue)
		}
		return capabilities, fmt.Errorf("%s", message.String())
	}
	return capabilities, nil
}

func isMappedRootlessUser() bool {
	if os.Geteuid() != 0 {
		return false
	}
	data, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return false
	}
	inside, errInside := strconv.Atoi(fields[0])
	outside, errOutside := strconv.Atoi(fields[1])
	count, errCount := strconv.Atoi(fields[2])
	return errInside == nil && errOutside == nil && errCount == nil && inside == 0 && outside > 0 && count == 1
}

func bubblewrapFailureAdvice(err error) string {
	message := "verify the bubblewrap installation and version, then repair or update it"
	if err != nil && strings.Contains(err.Error(), "NETLINK_ROUTE") {
		return "allow rootless bwrap to configure loopback in its network namespace; check host/container policy that restricts NETLINK_ROUTE access"
	}
	return message
}

func inspectOptions(path string, probes ...optionProbe) (map[string]bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--help").CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Sprintf("--help timed out after %s", 3*time.Second)
	}
	if err != nil {
		return nil, fmt.Sprintf("--help returned an error: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	options := make(map[string]bool)
	for _, option := range longOptionPattern.FindAllString(string(output), -1) {
		options[option] = true
	}
	for _, probe := range probes {
		if options[probe.option] {
			continue
		}
		args := make([]string, 0, len(probe.args)+1)
		args = append(args, probe.args...)
		args = append(args, "--help")
		probeOutput, probeErr := exec.CommandContext(ctx, path, args...).CombinedOutput()
		if ctx.Err() != nil {
			return nil, fmt.Sprintf("option probe for %s timed out after %s", probe.option, 3*time.Second)
		}
		if probeErr == nil {
			options[probe.option] = true
			for _, option := range longOptionPattern.FindAllString(string(probeOutput), -1) {
				options[option] = true
			}
		}
	}
	if len(options) == 0 {
		return nil, "--help returned no long options"
	}
	return options, ""
}

func checkBubblewrap(path string) error {
	versionCtx, cancelVersion := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelVersion()
	output, err := exec.CommandContext(versionCtx, path, "--version").CombinedOutput()
	if err != nil {
		if versionCtx.Err() != nil {
			return fmt.Errorf("--version timed out after %s", 3*time.Second)
		}
		return fmt.Errorf("--version returned an error: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	if strings.TrimSpace(string(output)) == "" {
		return fmt.Errorf("--version returned no version information")
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelProbe()
	output, err = exec.CommandContext(probeCtx, path,
		"--unshare-user",
		"--unshare-cgroup",
		"--unshare-net",
		"--ro-bind", string(filepath.Separator), string(filepath.Separator),
		"--clearenv",
		"--", "/bin/true",
	).CombinedOutput()
	if err != nil {
		if probeCtx.Err() != nil {
			return fmt.Errorf("minimal rootless sandbox probe timed out after %s", 5*time.Second)
		}
		return fmt.Errorf("minimal rootless sandbox probe failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func isSandboxSystemPath(path string) bool {
	for _, root := range []string{"/usr", "/bin", "/sbin"} {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
