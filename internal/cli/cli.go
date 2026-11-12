package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/arwos/isobox/internal/profile"
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
	if _, err = profile.ResolveProfile(appProfile, profile.DefaultSettings()); err != nil {
		fmt.Fprintf(stderr, "isobox run: %v\n", err)
		return 2
	}

	// Never run the requested program directly. The sandbox runtime is added by
	// later roadmap tasks, so this command only checks the profile and fails
	// closed until the sandbox runtime is available.
	fmt.Fprintf(stderr, "isobox: sandbox runtime is not available yet; refusing to launch %q outside isolation\n", options.Command[0])
	return 2
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
