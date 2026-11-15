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
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/arwos/isobox/internal/integration"
	"github.com/arwos/isobox/internal/limits"
	"github.com/arwos/isobox/internal/preflight"
	"github.com/arwos/isobox/internal/profile"
	"github.com/arwos/isobox/internal/sandbox"
)

const configUsage = `Usage:
  isobox config [--profile=PATH] [--app-id=ID] [-- APP [ARGS...]]

Options match 'isobox run'. This command prints the resolved launch plan and
does not start the application. A command after -- overrides profile.command.
`

func executeConfig(args []string, stdout, stderr io.Writer) int {
	options, err := parseRunArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, configUsage)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: %v\n\n%s", err, configUsage)
		return 2
	}
	if options.ProfilePath == "" {
		options.ProfilePath, err = integration.EnsureProfileForApp(options.AppID)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "isobox config: cannot select profile: %v\n", err)
			return 2
		}
	}
	profileData, err := os.ReadFile(options.ProfilePath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: cannot read profile %q: %v\n", options.ProfilePath, err)
		return 2
	}
	appProfile, err := profile.DecodeProfile(profileData)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: %v\n", err)
		return 2
	}
	options.Command, err = selectApplicationCommand(options.Command, appProfile.Command)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: %v\n", err)
		return 2
	}
	effective, err := profile.ResolveProfile(appProfile, profile.DefaultSettings())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: %v\n", err)
		return 2
	}
	if effective.NetworkMode == profile.NetworkModeProxy {
		if err := inspectAndWarnProxy(effective.Network.ProxyAlias, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "isobox config: %v\n", err)
			return 2
		}
	}
	capabilities, err := preflight.Check(effective)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: %v\n", err)
		return 2
	}
	network, err := sandbox.PrepareNetwork(capabilities.Network, capabilities.Pasta, capabilities.Slirp4netns)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: cannot prepare network configuration: %v\n", err)
		return 2
	}
	command, err := sandbox.BuildCommand(capabilities.Bubblewrap, capabilities.NFT, effective, options.ProfilePath, options.AppID, options.Command, network)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox config: %v\n", err)
		return 2
	}
	defer func() {
		for _, file := range command.ExtraFiles {
			_ = file.Close()
		}
	}()
	printLaunchPlan(stdout, options, effective, capabilities, network, command.Args)
	return 0
}

func printLaunchPlan(output io.Writer, options RunOptions, effective profile.EffectiveProfile, capabilities preflight.Capabilities, network sandbox.NetworkRuntime, bubblewrapArgs []string) {
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	row := func(name, value string) { _, _ = fmt.Fprintf(writer, "%s\t%s\n", name, value) }
	row("Параметр", "Значение")
	row("Профиль", strconv.Quote(options.ProfilePath))
	row("Программа", strconv.Quote(options.Command[0]))
	if len(options.Command) > 1 {
		arguments := make([]string, len(options.Command)-1)
		for index, value := range options.Command[1:] {
			arguments[index] = strconv.Quote(value)
		}
		row("Аргументы", strings.Join(arguments, " "))
	}
	row("App ID", valueOr(options.AppID, "не задан"))
	row("HOME в sandbox", homeSourceFromArgs(bubblewrapArgs))
	row("Сеть", string(effective.NetworkMode))
	row("Сетевой backend", backendName(network.Backend))
	if network.Executable != "" {
		row("Исполняемый файл backend", strconv.Quote(network.Executable))
	}
	if network.PortForwarder != preflight.BackendNone {
		row("Проброс портов", backendName(network.PortForwarder))
	}
	if network.ForwarderExecutable != "" {
		row("Исполняемый файл проброса портов", strconv.Quote(network.ForwarderExecutable))
	}
	if network.Fallback != nil {
		row("Fallback backend", backendName(network.Fallback.Backend))
	}
	row("Egress filter", strconv.FormatBool(network.EnforceEgressFilter))
	if len(network.PastaDNSArgs) > 0 {
		args := make([]string, len(network.PastaDNSArgs))
		for index, argument := range network.PastaDNSArgs {
			args[index] = strconv.Quote(argument)
		}
		row("Аргументы DNS backend", strings.Join(args, " "))
	}
	if len(network.Resolver) > 0 {
		row("Resolver в sandbox", strings.TrimSpace(string(network.Resolver)))
	}
	if network.ProxyDNSAddress != "" {
		row("DNS через proxy", network.ProxyDNSAddress)
	}
	for index, port := range effective.Network.Ports {
		endPort := ""
		if port.EndPort != 0 {
			endPort = fmt.Sprintf("-%d", port.EndPort)
		}
		row(fmt.Sprintf("Входящий порт[%d]", index), fmt.Sprintf("%s:%d%s/%s", port.HostAddress, port.Port, endPort, port.Protocol))
	}
	row("GUI", fmt.Sprintf("Wayland=%t X11=%t Audio=%t GPU=%t Desktop=%t", effective.GUI.Wayland, effective.GUI.X11, effective.GUI.Audio, effective.GUI.GPU, effective.GUI.Desktop))
	row("Лимиты", fmt.Sprintf("CPU=%d%% Memory=%d bytes Processes=%d OpenFiles=%d", effective.Limits.CPUQuotaPercent, effective.Limits.MemoryBytes, effective.Limits.ProcessCount, effective.Limits.OpenFiles))
	if limits.Any(effective.Limits) {
		row("Backend лимитов", capabilities.SystemdRun)
	}
	envNames := make([]string, 0, len(effective.Env))
	for name := range effective.Env {
		envNames = append(envNames, name)
	}
	sort.Strings(envNames)
	if len(envNames) == 0 {
		row("Environment", "host environment очищен; профильные переменные не заданы")
	} else {
		for _, name := range envNames {
			row("Environment: "+name, strconv.Quote(effective.Env[name]))
		}
	}
	row("Bubblewrap", capabilities.Bubblewrap)
	for index, argument := range bubblewrapArgs {
		row(fmt.Sprintf("bwrap argv[%d]", index), strconv.Quote(argument))
	}
	_ = writer.Flush()
}

func homeSourceFromArgs(args []string) string {
	for index := 0; index+2 < len(args); index++ {
		if args[index] == "--bind" && args[index+2] == "/home/isobox" {
			return args[index+1]
		}
	}
	return "не определён"
}

func backendName(backend preflight.BackendKind) string {
	if backend == preflight.BackendNone {
		return "нет"
	}
	return string(backend)
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
