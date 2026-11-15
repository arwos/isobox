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

	"github.com/arwos/isobox/internal/integration"
)

const integrationUsage = `Usage:
  isobox integration desktop install --id=ID --name=NAME --profile=PATH --app-id=ID [--icon=ICON] [--terminal] -- APP [ARGS...]
  isobox integration desktop remove --id=ID
  isobox integration systemd install --profile=PATH --app-id=ID
  isobox integration systemd enable --app-id=ID
  isobox integration systemd disable --app-id=ID
  isobox integration systemd remove --app-id=ID

Desktop install creates or updates a launcher in the user's XDG applications directory.
Systemd install creates a user service; the command must be configured in the profile.
Enable/disable toggle autostart without starting or stopping a running service.
`

func executeIntegration(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, integrationUsage)
		return 0
	}
	switch args[0] {
	case "desktop":
		return executeDesktopIntegration(args[1:], stdout, stderr)
	case "systemd":
		return executeSystemdIntegration(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "isobox integration: unknown action %q\n\n%s", args[0], integrationUsage)
		return 2
	}
}

func executeDesktopIntegration(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, integrationUsage)
		return 0
	}
	switch args[0] {
	case "install":
		return executeInstallLauncher(args[1:], stdout, stderr)
	case "remove":
		return executeRemoveLauncher(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "isobox integration desktop: unknown action %q\n\n%s", args[0], integrationUsage)
		return 2
	}
}

func executeInstallLauncher(args []string, stdout, stderr io.Writer) int {
	options, err := parseLauncherInstallArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, integrationUsage)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox integration desktop install: %v\n\n%s", err, integrationUsage)
		return 2
	}
	path, err := integration.InstallLauncher(options)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox integration desktop install: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Installed launcher: %s\n", path)
	return 0
}

func parseLauncherInstallArgs(args []string) (integration.LauncherOptions, error) {
	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return integration.LauncherOptions{}, fmt.Errorf("missing -- before the application command")
	}

	flags := flag.NewFlagSet("isobox integration desktop install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var options integration.LauncherOptions
	flags.StringVar(&options.ID, "id", "", "stable launcher file identity")
	flags.StringVar(&options.Name, "name", "", "launcher display name")
	flags.StringVar(&options.Profile, "profile", "", "path to a UNIC profile")
	flags.StringVar(&options.AppID, "app-id", "", "stable application identity for isolated HOME")
	flags.StringVar(&options.Icon, "icon", "", "optional icon name or absolute path")
	flags.BoolVar(&options.Terminal, "terminal", false, "run the application in a terminal window")
	if err := flags.Parse(args[:separator]); err != nil {
		return integration.LauncherOptions{}, err
	}
	if flags.NArg() != 0 {
		return integration.LauncherOptions{}, fmt.Errorf("unexpected launcher options before --")
	}
	options.Command = append([]string(nil), args[separator+1:]...)
	if options.ID == "" || options.Name == "" || options.Profile == "" || options.AppID == "" {
		return integration.LauncherOptions{}, fmt.Errorf("--id, --name, --profile, and --app-id are required")
	}
	if len(options.Command) == 0 || options.Command[0] == "" {
		return integration.LauncherOptions{}, fmt.Errorf("application command is required after --")
	}
	return options, nil
}

func executeRemoveLauncher(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isobox integration desktop remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var id string
	flags.StringVar(&id, "id", "", "stable launcher file identity")
	if err := flags.Parse(args); err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox integration desktop remove: %v\n\n%s", err, integrationUsage)
		return 2
	}
	if flags.NArg() != 0 || id == "" {
		_, _ = fmt.Fprintf(stderr, "isobox integration desktop remove: --id is required and positional arguments are not accepted\n\n%s", integrationUsage)
		return 2
	}
	path, err := integration.RemoveLauncher(id)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox integration desktop remove: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Removed launcher: %s\n", path)
	return 0
}
