/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/arwos/isobox/internal/profile"
)

const managedSystemdUnitMarker = "# Managed by isobox"

// SystemdUnitOptions describes a user service that runs one profile command.
type SystemdUnitOptions struct {
	Profile string
	AppID   string
}

func prepareSystemdUnit(options SystemdUnitOptions) (string, []byte, error) {
	if !validLauncherID(options.AppID) {
		return "", nil, fmt.Errorf("app-id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}
	profilePath, err := resolveRegularFile(options.Profile, "profile")
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return "", nil, fmt.Errorf("read profile %q: %w", profilePath, err)
	}
	appProfile, err := profile.DecodeProfile(data)
	if err != nil {
		return "", nil, err
	}
	if len(appProfile.Command) == 0 {
		return "", nil, fmt.Errorf("profile.command is required for a systemd service; add the program and its arguments to the profile")
	}

	isoboxPath, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("locate isobox executable: %w", err)
	}
	isoboxPath, err = resolveExecutable(isoboxPath)
	if err != nil {
		return "", nil, fmt.Errorf("resolve isobox executable: %w", err)
	}

	profileArgument, err := systemdExecArgument("--profile=" + profilePath)
	if err != nil {
		return "", nil, fmt.Errorf("encode profile path for systemd: %w", err)
	}
	appIDArgument, err := systemdExecArgument("--app-id=" + options.AppID)
	if err != nil {
		return "", nil, fmt.Errorf("encode app-id for systemd: %w", err)
	}
	executableArgument, err := systemdExecArgument(isoboxPath)
	if err != nil {
		return "", nil, fmt.Errorf("encode isobox path for systemd: %w", err)
	}

	content := fmt.Sprintf("%s\n[Unit]\nDescription=Isobox application %s\n\n[Service]\nType=simple\nExecStart=%s run %s %s\n\n[Install]\nWantedBy=default.target\n", managedSystemdUnitMarker, options.AppID, executableArgument, profileArgument, appIDArgument)
	return profilePath, []byte(content), nil
}

func systemdExecArgument(value string) (string, error) {
	var encoded strings.Builder
	encoded.Grow(len(value) + 2)
	encoded.WriteByte('"')
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", fmt.Errorf("argument contains a control character")
		}
		switch character {
		case '\\':
			encoded.WriteString(`\\`)
		case '"':
			encoded.WriteString(`\"`)
		case '$':
			encoded.WriteString("$$")
		case '%':
			encoded.WriteString("%%")
		default:
			encoded.WriteRune(character)
		}
	}
	encoded.WriteByte('"')
	return encoded.String(), nil
}

func systemdUserUnitName(appID string) (string, error) {
	if !validLauncherID(appID) {
		return "", fmt.Errorf("app-id must start with an ASCII letter or digit and contain only letters, digits, '.', '_' or '-' (maximum 64 characters)")
	}
	return "isobox-" + appID + ".service", nil
}

func systemdUserUnitDirectory() (string, error) {
	configHome, err := userConfigHome()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(configHome, "systemd", "user"), nil
}
