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
	"os"
	"strings"

	"github.com/arwos/isobox/internal/integration"
	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/processlist"
	"github.com/arwos/isobox/internal/profile"
	"github.com/arwos/isobox/internal/sandbox"
)

const rootUsage = `Usage:
  isobox run [--profile=PATH] [--app-id=ID] [-- APP [ARGS...]]
  isobox config [--profile=PATH] [--app-id=ID] [-- APP [ARGS...]]
  isobox ps

Commands:
  run          Prepare an isolated application launch
  config       Show launch parameters without starting the application
  ps           List applications running through isobox
  proxy        Manage SOCKS5 proxy aliases
  integration  Manage optional user desktop launchers and systemd units
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
	if effectiveProfile.NetworkMode == profile.NetworkModeProxy {
		if err := inspectAndWarnProxy(effectiveProfile.Network.ProxyAlias, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
			return 2
		}
	}
	capabilities, err := preflight.Check(effectiveProfile)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	network, err := sandbox.PrepareNetwork(capabilities.Network, capabilities.Pasta, capabilities.Slirp4netns)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox run: cannot prepare network: %v\n", err)
		return 2
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
	if network.Backend != preflight.BackendNone || network.PortForwarder != preflight.BackendNone {
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
