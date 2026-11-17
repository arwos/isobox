/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"

	"github.com/arwos/isobox/internal/integration"
	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/networkmanager"
	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/processlist"
	"github.com/arwos/isobox/internal/profile"
	"github.com/arwos/isobox/internal/sandbox"
)

const rootUsage = `Usage:
  isobox run [--profile=PATH] [--app-id=ID] [-- APP [ARGS...]]
  isobox config [--profile=PATH] [--app-id=ID] [-- APP [ARGS...]]
  isobox ps
  isobox network create --name=NAME --subnet=IPv4-CIDR
  isobox network list
  isobox network remove --name=NAME

Commands:
  run          Prepare an isolated application launch
  config       Show launch parameters without starting the application
  ps           List applications running through isobox
  network      Manage persistent rootless group networks
  proxy        Manage SOCKS5 proxy aliases
  integration  Manage optional user desktop launchers and application units
  help         Show this help
`

const runUsage = `Usage:
  isobox run [--profile=PATH] [--app-id=ID] [-- APP [ARGS...]]

Options:
  --profile=PATH  Path to a UNIC profile (default: app-id profile or default.unic)
  --app-id=ID     Stable application identity for profile selection and managed HOME

Everything after -- is passed to the application unchanged and overrides
the command configured in the profile. If neither is provided, the command
must be added to the profile.
`

// RunOptions contains the parts of a launch request owned by isobox and the
// command line that will eventually be started inside the sandbox.
type RunOptions struct {
	ProfilePath string
	AppID       string
	Command     []string
}

// Execute parses the isobox command and returns its process exit code.
func Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, rootUsage)
		return 2
	}

	switch args[0] {
	case "help", "--help", "-h":
		_, _ = io.WriteString(stdout, rootUsage)
		return 0
	case "run":
		return executeRun(args[1:], stdout, stderr)
	case "config":
		return executeConfig(args[1:], stdout, stderr)
	case "ps":
		return executePS(args[1:], stdout, stderr)
	case "network":
		return executeNetwork(args[1:], stdout, stderr)
	case "proxy":
		return executeProxy(args[1:], stdout, stderr)
	case "integration":
		return executeIntegration(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "isobox: unknown command %q\n\n%s", args[0], rootUsage)
		return 2
	}
}

func executeRun(args []string, stdout, stderr io.Writer) int {
	options, err := parseRunArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, runUsage)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n\n%s", err, runUsage)
		return 2
	}
	if options.ProfilePath == "" {
		options.ProfilePath, err = integration.EnsureProfileForApp(options.AppID)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "isobox run: cannot select profile: %v\n", err)
			return 2
		}
	}
	profileData, err := os.ReadFile(options.ProfilePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: cannot read profile %q: %v\n", options.ProfilePath, err)
		return 2
	}
	appProfile, err := profile.DecodeProfile(profileData)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	options.Command, err = selectApplicationCommand(options.Command, appProfile.Command)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	effectiveProfile, err := profile.ResolveProfile(appProfile, profile.DefaultSettings())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	memberContext, attachments, hostUID, hostGID, err := networkmanager.MemberRuntime()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 125
	}
	if len(effectiveProfile.Network.Groups) != 0 {
		if options.AppID == "" {
			_, _ = fmt.Fprintln(stderr, "isobox run: --app-id is required when network.group is configured because app-id is published in group DNS")
			return 2
		}
		if effectiveProfile.Network.InterfaceName != "" || effectiveProfile.Network.Subnet != "" {
			_, _ = fmt.Fprintln(stderr, "isobox run: network.interface and network.subnet cannot be combined with named group networks")
			return 2
		}
		if len(effectiveProfile.Network.Ports) != 0 {
			_, _ = fmt.Fprintln(stderr, "isobox run: named group networks do not yet support published ports")
			return 2
		}
		if !memberContext {
			stdin := os.Stdin
			stdoutFile, stdoutOK := stdout.(*os.File)
			stderrFile, stderrOK := stderr.(*os.File)
			if !stdoutOK || !stderrOK {
				_, _ = fmt.Fprintln(stderr, "isobox run: grouped launches require file-backed standard streams")
				return 2
			}
			var startTunnel networkmanager.MemberTunnelStarter
			if effectiveProfile.NetworkMode == profile.NetworkModeProxy || effectiveProfile.NetworkMode == profile.NetworkModeEgress || effectiveProfile.NetworkMode == profile.NetworkModeDirect {
				if effectiveProfile.NetworkMode == profile.NetworkModeProxy {
					if err := inspectAndWarnProxy(effectiveProfile.Network.ProxyAlias, stderr); err != nil {
						_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
						return 2
					}
				}
				capabilities, preflightErr := preflight.Check(effectiveProfile)
				if preflightErr != nil {
					_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", preflightErr)
					return 2
				}
				network, prepareErr := sandbox.PrepareNetwork(capabilities.Network, capabilities.Pasta, capabilities.Slirp4netns, effectiveProfile.Network)
				if prepareErr != nil {
					_, _ = fmt.Fprintf(stderr, "isobox run: cannot prepare network: %v\n", prepareErr)
					return 2
				}
				network.SharedNamespace = true
				startTunnel = func(tunFile *os.File, info networkmanager.MemberTUNInfo) (networkmanager.MemberTunnel, error) {
					if effectiveProfile.NetworkMode == profile.NetworkModeDirect {
						return sandbox.StartSharedDirectBackend(network, info)
					}
					return sandbox.StartSharedTUNDataPlane(tunFile, network, info, options.AppID)
				}
			}
			exitCode, runErr := networkmanager.RunJoined(effectiveProfile.Network.Groups, effectiveProfile.Network.Aliases, options.AppID, args, os.Environ(), stdin, stdoutFile, stderrFile, startTunnel)
			if runErr != nil {
				_, _ = fmt.Fprintf(stderr, "isobox run: grouped launch failed: %v\n", runErr)
				return 125
			}
			return exitCode
		}
		if !sameNetworkGroups(effectiveProfile.Network.Groups, attachments) {
			_, _ = fmt.Fprintln(stderr, "isobox run: manager group assignments do not match the profile")
			return 125
		}
	} else if memberContext {
		_, _ = fmt.Fprintln(stderr, "isobox run: manager supplied group interfaces for a profile without network.group")
		return 125
	}
	if effectiveProfile.NetworkMode == profile.NetworkModeProxy && !memberContext {
		if err := inspectAndWarnProxy(effectiveProfile.Network.ProxyAlias, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
			return 2
		}
	}
	var capabilities preflight.Capabilities
	if memberContext {
		capabilities, err = preflight.CheckInRootlessUserNamespace(effectiveProfile)
	} else {
		capabilities, err = preflight.Check(effectiveProfile)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	network, err := sandbox.PrepareNetwork(capabilities.Network, capabilities.Pasta, capabilities.Slirp4netns, effectiveProfile.Network)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: cannot prepare network: %v\n", err)
		return 2
	}
	if memberContext {
		network.SharedNamespace = true
		network.BackendNamespacePID = attachments[0].NamespacePID
		network.HostUID, network.HostGID, network.HasHostIdentity = hostUID, hostGID, true
		for _, attachment := range attachments {
			address, parseErr := netip.ParseAddr(attachment.Address)
			if parseErr != nil || !address.Is4() {
				_, _ = fmt.Fprintf(stderr, "isobox run: invalid group address for %q: %q\n", attachment.Group, attachment.Address)
				return 125
			}
			network.GroupSubnets = append(network.GroupSubnets, netip.PrefixFrom(address, attachment.Prefix).Masked().String())
		}
		switch effectiveProfile.NetworkMode {
		case profile.NetworkModeProxy, profile.NetworkModeEgress:
			tunFile, tunInfo, tunErr := sandbox.ConfigureSharedTUN(&network)
			if tunErr == nil {
				tunErr = networkmanager.SubmitMemberTUN(tunFile, tunInfo)
				_ = tunFile.Close()
			}
			if tunErr != nil {
				err = tunErr
			}
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "isobox run: cannot prepare grouped %s policy tunnel: %v\n", effectiveProfile.NetworkMode, err)
				return 125
			}
			network.SharedTunnel = true
		case profile.NetworkModeDirect:
			if err := networkmanager.SubmitMemberBackend(networkmanager.MemberTUNInfo{BackendNamespacePID: network.BackendNamespacePID}); err != nil {
				_, _ = fmt.Fprintf(stderr, "isobox run: cannot start grouped direct network backend: %v\n", err)
				return 125
			}
			network.SharedTunnel = true
		}
		resolverEndpoint := ""
		switch effectiveProfile.NetworkMode {
		case profile.NetworkModeProxy, profile.NetworkModeEgress:
			resolverEndpoint = network.ResolverEndpoint
		}
		stopDNS, dnsErr := networkmanager.StartMemberDNS(networkmanager.MemberDNSConfig{
			Mode: effectiveProfile.NetworkMode, Upstreams: network.DNSAddresses, ResolverEndpoint: resolverEndpoint,
		})
		if dnsErr != nil {
			_, _ = fmt.Fprintf(stderr, "isobox run: cannot start group DNS resolver: %v\n", dnsErr)
			return 125
		}
		defer stopDNS()
	}
	command, err := sandbox.BuildCommand(capabilities.Bubblewrap, capabilities.NFT, effectiveProfile, options.ProfilePath, options.AppID, options.Command, network)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	workingDirectory, _ := os.Getwd()
	command.Env = setEnvironmentValue(os.Environ(), processlist.EnvAppID, options.AppID)
	command.Env = setEnvironmentValue(command.Env, processlist.EnvCWD, workingDirectory)
	command.Env = setEnvironmentValue(command.Env, processlist.EnvNetwork, string(effectiveProfile.NetworkMode))

	var exitCode int
	if (network.Backend != preflight.BackendNone || network.PortForwarder != preflight.BackendNone) && !network.SharedTunnel {
		exitCode, err = sandbox.RunWithNetwork(command, network, effectiveProfile.Network.Ports, effectiveProfile.Limits, capabilities.SystemdRun)
	} else if limits.Any(effectiveProfile.Limits) {
		exitCode, err = sandbox.RunWithLimits(command, effectiveProfile.Limits, capabilities.SystemdRun)
	} else {
		exitCode, err = sandbox.Run(command)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: sandbox launch failed: %v\n", err)
		return 125
	}
	return exitCode
}

func sameNetworkGroups(groups []string, attachments []networkmanager.MemberAttachment) bool {
	if len(groups) != len(attachments) {
		return false
	}
	for index, group := range groups {
		if attachments[index].Group != group {
			return false
		}
	}
	return true
}

func setEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}

func parseRunArgs(args []string) (RunOptions, error) {
	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}

	flagArgs := args
	var command []string
	if separator >= 0 {
		flagArgs = args[:separator]
		command = args[separator+1:]
	}

	flags := flag.NewFlagSet("isobox run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options RunOptions
	flags.StringVar(&options.ProfilePath, "profile", "", "path to a UNIC profile")
	flags.StringVar(&options.AppID, "app-id", "", "stable application identity")
	if err := flags.Parse(flagArgs); err != nil {
		return RunOptions{}, err
	}
	if flags.NArg() > 0 {
		return RunOptions{}, fmt.Errorf("application command must follow --")
	}
	if appIDWasSet(flagArgs) && !validAppID(options.AppID) {
		return RunOptions{}, fmt.Errorf("--app-id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}

	options.Command = append([]string(nil), command...)
	return options, nil
}

func selectApplicationCommand(requested, configured []string) ([]string, error) {
	selected := requested
	if len(selected) == 0 {
		selected = configured
	}
	if len(selected) == 0 || selected[0] == "" {
		return nil, fmt.Errorf("application command is required: pass it after -- or set profile.command")
	}
	return append([]string(nil), selected...), nil
}

func appIDWasSet(args []string) bool {
	for _, arg := range args {
		if arg == "--app-id" || arg == "-app-id" || strings.HasPrefix(arg, "--app-id=") || strings.HasPrefix(arg, "-app-id=") {
			return true
		}
	}
	return false
}

func validAppID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if index > 0 {
			valid = valid || char == '.' || char == '_' || char == '-'
		}
		if !valid {
			return false
		}
	}
	return true
}
