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
	"strings"
	"time"

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
	Network              BackendSelection
}

var longOptionPattern = regexp.MustCompile(`--[a-zA-Z0-9][a-zA-Z0-9-]*`)

// Check verifies host prerequisites before any sandbox or application process
// is started. It accumulates independent failures so one run can report the
// full set of host changes the user needs to make.
func Check(config profile.EffectiveProfile) (Capabilities, error) {
	var capabilities Capabilities
	var issues []string
	if runtime.GOOS != "linux" {
		return capabilities, fmt.Errorf("host preflight failed:\n- isobox requires Linux; current operating system is %s", runtime.GOOS)
	}
	if os.Geteuid() == 0 {
		issues = append(issues, "isobox must run as an ordinary unprivileged user; do not launch it as root")
	}

	var err error
	capabilities.Bubblewrap, err = exec.LookPath("bwrap")
	if err != nil {
		issues = append(issues, "bwrap was not found in PATH; install bubblewrap for this distribution")
	} else if err := checkBubblewrap(capabilities.Bubblewrap); err != nil {
		issues = append(issues, fmt.Sprintf("bwrap is not usable: %v; reinstall or repair bubblewrap and retry", err))
	}

	if err := probeUserNamespace(); err != nil {
		issues = append(issues, fmt.Sprintf("an unprivileged user namespace could not be created: %v; enable user namespaces using the distribution's documented setting (some systems use kernel.unprivileged_userns_clone=1); do not run isobox as root to bypass this check", err))
	}

	capabilities.Pasta, _ = exec.LookPath("pasta")
	capabilities.Slirp4netns, _ = exec.LookPath("slirp4netns")
	needsNetworkBackend := config.NetworkMode != profile.NetworkModeOff || len(config.Network.Ports) != 0
	if needsNetworkBackend && capabilities.Pasta == "" && capabilities.Slirp4netns == "" {
		issues = append(issues, "neither pasta nor slirp4netns was found in PATH; install passt (pasta) or slirp4netns and retry")
	}
	if capabilities.Pasta != "" {
		capabilities.PastaOptions, capabilities.PastaInspectionError = inspectOptions(capabilities.Pasta)
	}
	if capabilities.Slirp4netns != "" {
		capabilities.Slirp4netnsOptions, capabilities.SlirpInspectionError = inspectOptions(capabilities.Slirp4netns)
	}
	capabilities.Network, err = SelectBackend(config, capabilities)
	if err != nil {
		issues = append(issues, fmt.Sprintf("network backend cannot preserve the profile: %v", err))
	}
	if config.NetworkMode == profile.NetworkModeOff && len(config.Network.Ports) != 0 {
		capabilities.NFT, err = exec.LookPath("nft")
		if err != nil {
			issues = append(issues, "network mode off with published ports requires nftables (nft) to enforce the egress filter; install nftables and retry")
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
			} else if !hasAllOptions(options, "--cap-add", "--block-fd", "--disable-userns", "--assert-userns-disabled") {
				issues = append(issues, "installed bwrap lacks the options required for the off-mode egress guard; update bubblewrap and retry")
			}
		}
	}

	if config.NetworkMode == profile.NetworkModeProxy {
		if err := probeTUN(); err != nil {
			issues = append(issues, fmt.Sprintf("proxy mode cannot create a TUN interface in a user network namespace: %v; make the kernel TUN driver and /dev/net/tun available, allow the current user to open the device, and permit CAP_NET_ADMIN in the new network namespace", err))
		}
	}
	if config.NetworkMode == profile.NetworkModeDirect && capabilities.Bubblewrap != "" {
		options, inspectErr := inspectOptions(capabilities.Bubblewrap)
		if inspectErr != "" {
			issues = append(issues, fmt.Sprintf("cannot verify bwrap direct-network startup options: %s", inspectErr))
		} else if !hasAllOptions(options, "--block-fd", "--ro-bind-data", "--json-status-fd", "--disable-userns", "--assert-userns-disabled") {
			issues = append(issues, "installed bwrap lacks the startup-gate or resolver-file options required for direct network mode; update bubblewrap and retry")
		}
	}

	if err := checkRequestedLimits(config.Limits); err != nil {
		issues = append(issues, err.Error())
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

func inspectOptions(path string) (map[string]bool, string) {
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
		"--unshare-net",
		"--disable-userns",
		"--assert-userns-disabled",
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

func checkRequestedLimits(limits profile.ResourceLimits) error {
	var requested []string
	if limits.CPUQuotaPercent > 0 {
		requested = append(requested, "cpu_quota_percent")
	}
	if limits.MemoryBytes > 0 {
		requested = append(requested, "memory_bytes")
	}
	if limits.ProcessCount > 0 {
		requested = append(requested, "process_count")
	}
	if limits.OpenFiles > 0 {
		requested = append(requested, "open_files")
	}
	if len(requested) == 0 {
		return nil
	}
	return fmt.Errorf("profile limits (%s) cannot be enforced by this build; remove them or use a build with the F07 limit backend", strings.Join(requested, ", "))
}
