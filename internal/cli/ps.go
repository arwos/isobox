/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package cli

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"text/tabwriter"

	"github.com/arwos/isobox/internal/processlist"
)

const psUsage = `Usage:
  isobox ps

List applications currently running through isobox.
`

func executePS(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		_, _ = io.WriteString(stdout, psUsage)
		return 0
	}
	if len(args) != 0 {
		_, _ = fmt.Fprintf(stderr, "isobox ps: this command accepts no arguments\n\n%s", psUsage)
		return 2
	}
	entries, err := processlist.List()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "isobox ps: cannot read process list: %v\n", err)
		return 1
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].PID < entries[j].PID })
	writer := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "PID\tAPP-ID\tWORKING DIRECTORY\tMEMORY\tCPU\tINTERNET")
	for _, entry := range entries {
		appID := entry.AppID
		if appID == "" {
			appID = "-"
		}
		_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%.1f%%\t%s\n", entry.PID, appID, strconv.Quote(entry.CWD), formatBytes(entry.MemoryBytes), entry.CPUPercent, internetStatus(entry.Network))
	}
	_ = writer.Flush()
	return 0
}

func formatBytes(value uint64) string {
	if value < 1<<20 {
		return fmt.Sprintf("%d KiB", value/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(value)/(1<<20))
}

func internetStatus(mode string) string {
	switch mode {
	case "off":
		return "off"
	case "direct":
		return "direct"
	case "proxy":
		return "proxy"
	case "egress":
		return "egress"
	default:
		return "unknown"
	}
}
