package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/profile"
	"github.com/arwos/isobox/internal/sandbox"
)

const rootUsage = `Usage:
  isobox run --profile=PATH [--app-id=ID] -- APP [ARGS...]

Commands:
  run    Prepare an isolated application launch
  help   Show this help
`

const runUsage = `Usage:
  isobox run --profile=PATH [--app-id=ID] -- APP [ARGS...]

Options:
  --profile=PATH  Path to a UNIC profile (required)
  --app-id=ID     Stable application identity for a managed HOME directory

Everything after -- is passed to the application unchanged.
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
	default:
		fmt.Fprintf(stderr, "isobox: unknown command %q\n\n%s", args[0], rootUsage)
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
		fmt.Fprintf(stderr, "isobox run: %v\n\n%s", err, runUsage)
		return 2
	}
	profileData, err := os.ReadFile(options.ProfilePath)
	if err != nil {
		fmt.Fprintf(stderr, "isobox run: cannot read profile %q: %v\n", options.ProfilePath, err)
		return 2
	}
	appProfile, err := profile.DecodeProfile(profileData)
	if err != nil {
		fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	effectiveProfile, err := profile.ResolveProfile(appProfile, profile.DefaultSettings())
	if err != nil {
		fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	capabilities, err := preflight.Check(effectiveProfile)
	if err != nil {
		fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	network, err := sandbox.PrepareNetwork(capabilities.Network, capabilities.Pasta, capabilities.Slirp4netns)
	if err != nil {
		fmt.Fprintf(stderr, "isobox run: cannot prepare network: %v\n", err)
		return 2
	}
	command, err := sandbox.BuildCommand(capabilities.Bubblewrap, capabilities.NFT, effectiveProfile, options.ProfilePath, options.AppID, options.Command, network)
	if err != nil {
		fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr

	var exitCode int
	if network.Backend != preflight.BackendNone || network.PortForwarder != preflight.BackendNone {
		exitCode, err = sandbox.RunWithNetwork(command, network, effectiveProfile.Network.Ports, effectiveProfile.Limits, capabilities.SystemdRun)
	} else if limits.Any(effectiveProfile.Limits) {
		exitCode, err = sandbox.RunWithLimits(command, effectiveProfile.Limits, capabilities.SystemdRun)
	} else {
		exitCode, err = sandbox.Run(command)
	}
	if err != nil {
		fmt.Fprintf(stderr, "isobox run: sandbox launch failed: %v\n", err)
		return 125
	}
	return exitCode
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
	if separator < 0 {
		return RunOptions{}, fmt.Errorf("missing -- before the application command")
	}
	if options.ProfilePath == "" {
		return RunOptions{}, fmt.Errorf("--profile is required")
	}
	if len(command) == 0 || command[0] == "" {
		return RunOptions{}, fmt.Errorf("application command is required after --")
	}
	if appIDWasSet(flagArgs) && !validAppID(options.AppID) {
		return RunOptions{}, fmt.Errorf("--app-id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}

	options.Command = append([]string(nil), command...)
	return options, nil
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
