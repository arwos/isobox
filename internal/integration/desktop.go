/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LauncherOptions describes a per-user launcher for one explicit isobox run.
type LauncherOptions struct {
	ID       string
	Name     string
	Icon     string
	Profile  string
	AppID    string
	Command  []string
	Terminal bool
}

func prepareLauncher(options LauncherOptions) ([]byte, error) {
	if !validLauncherID(options.ID) {
		return nil, fmt.Errorf("launcher id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}
	if !validLauncherID(options.AppID) {
		return nil, fmt.Errorf("app-id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}
	if err := validateDesktopText("name", options.Name, true); err != nil {
		return nil, err
	}
	if options.Icon != "" {
		if err := validateDesktopText("icon", options.Icon, false); err != nil {
			return nil, err
		}
	}
	if len(options.Command) == 0 || options.Command[0] == "" {
		return nil, fmt.Errorf("application command is required after --")
	}

	profilePath, err := resolveRegularFile(options.Profile, "profile")
	if err != nil {
		return nil, err
	}
	isoboxPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate isobox executable: %w", err)
	}
	isoboxPath, err = resolveExecutable(isoboxPath)
	if err != nil {
		return nil, fmt.Errorf("resolve isobox executable: %w", err)
	}
	if strings.Contains(isoboxPath, "=") {
		return nil, fmt.Errorf("isobox executable path contains '=' which the desktop Exec format cannot represent")
	}

	applicationPath, err := exec.LookPath(options.Command[0])
	if err != nil {
		return nil, fmt.Errorf("resolve application executable %q: %w", options.Command[0], err)
	}
	applicationPath, err = resolveExecutable(applicationPath)
	if err != nil {
		return nil, fmt.Errorf("resolve application executable %q: %w", options.Command[0], err)
	}
	if strings.Contains(applicationPath, "=") {
		return nil, fmt.Errorf("application executable path contains '=' which the desktop Exec format cannot represent")
	}

	name, _ := encodeDesktopValue(options.Name)
	execArguments := []string{isoboxPath, "run", "--profile=" + profilePath, "--app-id=" + options.AppID, "--", applicationPath}
	execArguments = append(execArguments, options.Command[1:]...)
	encodedArguments := make([]string, 0, len(execArguments))
	for index, argument := range execArguments {
		encoded, err := encodeExecArgument(argument, index == 0)
		if err != nil {
			return nil, fmt.Errorf("encode desktop Exec argument %d: %w", index, err)
		}
		encodedArguments = append(encodedArguments, encoded)
	}

	var entry strings.Builder
	entry.WriteString("[Desktop Entry]\nType=Application\nVersion=1.5\nName=")
	entry.WriteString(name)
	entry.WriteString("\nTryExec=")
	tryExec, err := encodeDesktopValue(isoboxPath)
	if err != nil {
		return nil, fmt.Errorf("encode isobox path: %w", err)
	}
	entry.WriteString(tryExec)
	entry.WriteString("\nExec=")
	entry.WriteString(strings.Join(encodedArguments, " "))
	if options.Icon != "" {
		icon, err := encodeDesktopValue(options.Icon)
		if err != nil {
			return nil, fmt.Errorf("encode icon: %w", err)
		}
		entry.WriteString("\nIcon=")
		entry.WriteString(icon)
	}
	if options.Terminal {
		entry.WriteString("\nTerminal=true")
	} else {
		entry.WriteString("\nTerminal=false")
	}
	entry.WriteString("\nX-Isobox-Managed=true\n")
	return []byte(entry.String()), nil
}

func resolveRegularFile(path, field string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s path is required", field)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s path %q: %w", field, path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %s path %q: %w", field, path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect %s path %q: %w", field, resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s path %q must be a regular file", field, resolved)
	}
	return resolved, nil
}

func resolveExecutable(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("path is not an executable regular file")
	}
	return abs, nil
}

func validLauncherID(value string) bool {
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

func validateDesktopText(field, value string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s cannot contain control characters", field)
		}
	}
	return nil
}

func encodeDesktopValue(value string) (string, error) {
	if err := validateDesktopText("desktop entry value", value, false); err != nil {
		return "", err
	}
	return strings.ReplaceAll(value, `\`, `\\`), nil
}

func encodeExecArgument(argument string, executable bool) (string, error) {
	if !utf8.ValidString(argument) {
		return "", fmt.Errorf("argument is not valid UTF-8")
	}
	for _, character := range argument {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("argument contains a control character unsupported by desktop Exec")
		}
	}
	if executable && strings.Contains(argument, "=") {
		return "", fmt.Errorf("executable path contains '='")
	}

	argument = strings.ReplaceAll(argument, "%", "%%")
	reserved := " \t\"'\\><~|&;$*?#()" + "`"
	quoted := argument == "" || strings.ContainsAny(argument, reserved)
	if quoted {
		argument = strings.NewReplacer(
			`\`, `\\`,
			`"`, `\"`,
			"`", "\\`",
			"$", `\$`,
		).Replace(argument)
		argument = `"` + argument + `"`
	}
	// Exec is a string value before it is parsed into argv; escape the command
	// line's backslashes once more for the desktop-entry string layer.
	argument = strings.ReplaceAll(argument, `\`, `\\`)
	return argument, nil
}
