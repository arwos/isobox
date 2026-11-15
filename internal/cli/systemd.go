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

const systemdIntegrationUsage = `Usage:
  isobox integration systemd install --profile=PATH --app-id=ID
  isobox integration systemd enable --app-id=ID
  isobox integration systemd disable --app-id=ID
  isobox integration systemd remove --app-id=ID

Install writes a user service unit and reloads the user systemd manager.
Enable/disable configure automatic start for the user's default.target.
Neither action starts or stops a currently running service.
`

func executeSystemdIntegration(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, systemdIntegrationUsage)
		return 0
	}
	switch args[0] {
	case "install":
		return executeInstallSystemdUnit(args[1:], stdout, stderr)
	case "enable":
		return executeSetSystemdUnitEnabled(args[1:], true, stdout, stderr)
	case "disable":
		return executeSetSystemdUnitEnabled(args[1:], false, stdout, stderr)
	case "remove":
		return executeRemoveSystemdUnit(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd: unknown action %q\n\n%s", args[0], systemdIntegrationUsage)
		return 2
	}
}

func executeSetSystemdUnitEnabled(args []string, enable bool, stdout, stderr io.Writer) int {
	action := "enable"
	if !enable {
		action = "disable"
	}
	flags := flag.NewFlagSet("isobox integration systemd "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var appID string
	flags.StringVar(&appID, "app-id", "", "stable application identity and unit name")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stdout, systemdIntegrationUsage)
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd %s: %v\n\n%s", action, err, systemdIntegrationUsage)
		return 2
	}
	if flags.NArg() != 0 || appID == "" {
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd %s: --app-id is required; positional arguments are not accepted\n\n%s", action, systemdIntegrationUsage)
		return 2
	}
	var path string
	var err error
	if enable {
		path, err = integration.EnableSystemdUnit(appID)
	} else {
		path, err = integration.DisableSystemdUnit(appID)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd %s: %v\n", action, err)
		return 1
	}
	result := "Enabled"
	if !enable {
		result = "Disabled"
	}
	_, _ = fmt.Fprintf(stdout, "%s user service: %s\n", result, path)
	return 0
}

func executeInstallSystemdUnit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isobox integration systemd install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var profilePath, appID string
	flags.StringVar(&profilePath, "profile", "", "path to a UNIC profile that contains command")
	flags.StringVar(&appID, "app-id", "", "stable application identity and unit name")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stdout, systemdIntegrationUsage)
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd install: %v\n\n%s", err, systemdIntegrationUsage)
		return 2
	}
	if flags.NArg() != 0 || profilePath == "" || appID == "" {
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd install: --profile and --app-id are required; positional arguments are not accepted\n\n%s", systemdIntegrationUsage)
		return 2
	}
	path, err := integration.InstallSystemdUnit(integration.SystemdUnitOptions{Profile: profilePath, AppID: appID})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd install: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Installed user service: %s\n", path)
	return 0
}

func executeRemoveSystemdUnit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isobox integration systemd remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var appID string
	flags.StringVar(&appID, "app-id", "", "stable application identity and unit name")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stdout, systemdIntegrationUsage)
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd remove: %v\n\n%s", err, systemdIntegrationUsage)
		return 2
	}
	if flags.NArg() != 0 || appID == "" {
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd remove: --app-id is required; positional arguments are not accepted\n\n%s", systemdIntegrationUsage)
		return 2
	}
	path, err := integration.RemoveSystemdUnit(appID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox integration systemd remove: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Removed user service: %s\n", path)
	return 0
}
