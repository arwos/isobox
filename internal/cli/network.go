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
	"path/filepath"
	"text/tabwriter"

	"github.com/arwos/isobox/internal/networkmanager"
)

const networkUsage = `Usage:
  isobox network create --name=NAME --subnet=IPv4-CIDR
  isobox network list
  isobox network remove --name=NAME

Networks are persistent rootless IPv4 bridges managed by systemd --user.
The user manager must be running. Enable lingering separately if networks
must start before the user logs in; isobox does not enable it automatically.
`

func executeNetwork(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, _ = io.WriteString(stdout, networkUsage)
		return 0
	}
	switch args[0] {
	case "create":
		return executeCreateNetwork(args[1:], stdout, stderr)
	case "list":
		return executeListNetworks(args[1:], stdout, stderr)
	case "remove":
		return executeRemoveNetwork(args[1:], stdout, stderr)
	case "__manager":
		path, err := parseNetworkManagerArgs("isobox network __manager", args[1:])
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			_, _ = fmt.Fprintf(stderr, "isobox network manager: %v\n", err)
			return 2
		}
		if err := networkmanager.StartManager(path); err != nil {
			_, _ = fmt.Fprintf(stderr, "isobox network manager: %v\n", err)
			return 1
		}
		return 0
	case "__manager-worker":
		path, err := parseNetworkManagerArgs("isobox network __manager-worker", args[1:])
		if err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			_, _ = fmt.Fprintf(stderr, "isobox network manager worker: %v\n", err)
			return 2
		}
		if err := networkmanager.RunManagerWorker(path); err != nil {
			_, _ = fmt.Fprintf(stderr, "isobox network manager worker: %v\n", err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "isobox network: unknown action %q\n\n%s", args[0], networkUsage)
		return 2
	}
}

func executeCreateNetwork(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isobox network create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var name, subnet string
	flags.StringVar(&name, "name", "", "stable network name")
	flags.StringVar(&subnet, "subnet", "", "IPv4 subnet in CIDR form")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stdout, networkUsage)
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "isobox network create: %v\n\n%s", err, networkUsage)
		return 2
	}
	if flags.NArg() != 0 || name == "" || subnet == "" {
		_, _ = fmt.Fprintf(stderr, "isobox network create: --name and --subnet are required; positional arguments are not accepted\n\n%s", networkUsage)
		return 2
	}
	network, err := networkmanager.Create(name, subnet)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox network create: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Created network %s (%s), gateway %s, bridge %s\n", network.Name, network.Subnet, network.Gateway, network.Bridge)
	return 0
}

func executeListNetworks(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		_, _ = fmt.Fprintf(stderr, "isobox network list: positional arguments are not accepted\n\n%s", networkUsage)
		return 2
	}
	networks, err := networkmanager.List()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox network list: %v\n", err)
		return 1
	}
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "NAME\tSUBNET\tGATEWAY\tBRIDGE\tSTATUS\tMEMBERS")
	for _, network := range networks {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%d\n", network.Name, network.Subnet, network.Gateway, network.Bridge, network.State, network.ActiveMembers)
	}
	_ = writer.Flush()
	return 0
}

func executeRemoveNetwork(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("isobox network remove", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var name string
	flags.StringVar(&name, "name", "", "stable network name")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(stdout, networkUsage)
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "isobox network remove: %v\n\n%s", err, networkUsage)
		return 2
	}
	if flags.NArg() != 0 || name == "" {
		_, _ = fmt.Fprintf(stderr, "isobox network remove: --name is required; positional arguments are not accepted\n\n%s", networkUsage)
		return 2
	}
	if err := networkmanager.Remove(name); err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox network remove: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "Removed network %s\n", name)
	return 0
}

func parseNetworkManagerArgs(commandName string, args []string) (string, error) {
	flags := flag.NewFlagSet(commandName, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var path string
	flags.StringVar(&path, "config", "", "managed network configuration path")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 || path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("--config must be an absolute path and positional arguments are not accepted")
	}
	return path, nil
}
