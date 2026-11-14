//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package sandbox

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/arwos/isobox/internal/profile"
)

func resolveGUI(access profile.GUIAccess) (guiSetup, error) {
	setup := guiSetup{environment: make(map[string]string)}
	if access.Wayland || access.X11 || access.Audio || access.Desktop {
		setup.runtimeDir = true
		setup.environment["XDG_RUNTIME_DIR"] = sandboxGUIRuntime
	}

	if access.Wayland {
		source, err := waylandSocketPath()
		if err != nil {
			return guiSetup{}, err
		}
		mount, err := unixSocketMount("profile.gui.wayland", source, filepath.Join(sandboxGUIRuntime, "wayland-0"))
		if err != nil {
			return guiSetup{}, err
		}
		setup.mounts = append(setup.mounts, mount)
		setup.environment["WAYLAND_DISPLAY"] = "wayland-0"
	}

	if access.X11 {
		display := os.Getenv("DISPLAY")
		socketPath, normalizedDisplay, err := x11DisplaySocket(display)
		if err != nil {
			return guiSetup{}, err
		}
		mount, err := unixSocketMount("profile.gui.x11", socketPath, "/tmp/.X11-unix/"+filepath.Base(socketPath))
		if err != nil {
			return guiSetup{}, err
		}
		setup.mounts = append(setup.mounts, mount)
		setup.environment["DISPLAY"] = normalizedDisplay

		authority, exists, err := xAuthorityPath()
		if err != nil {
			return guiSetup{}, err
		}
		if exists {
			setup.mounts = append(setup.mounts, profileMount{
				source: authority,
				target: filepath.Join(sandboxGUIRuntime, "Xauthority"),
			})
			setup.environment["XAUTHORITY"] = filepath.Join(sandboxGUIRuntime, "Xauthority")
		}
	}

	if access.Audio {
		mounts, environment, err := audioAccess()
		if err != nil {
			return guiSetup{}, err
		}
		setup.mounts = append(setup.mounts, mounts...)
		for name, value := range environment {
			setup.environment[name] = value
		}
	}

	if access.GPU {
		mounts, err := gpuDeviceMounts()
		if err != nil {
			return guiSetup{}, err
		}
		setup.mounts = append(setup.mounts, mounts...)
	}

	if access.Desktop {
		paths, err := sessionBusSocketPaths()
		if err != nil {
			return guiSetup{}, err
		}
		target := filepath.Join(sandboxGUIRuntime, "bus")
		var selected *profileMount
		var lastError error
		for _, path := range paths {
			mount, mountErr := unixSocketMount("profile.gui.desktop", path, target)
			if mountErr == nil {
				selected = &mount
				break
			}
			lastError = mountErr
		}
		if selected == nil {
			return guiSetup{}, fmt.Errorf("profile.gui.desktop: no usable filesystem session-bus socket: %w", lastError)
		}
		setup.mounts = append(setup.mounts, *selected)
		setup.environment["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + target
		if desktop := os.Getenv("XDG_CURRENT_DESKTOP"); desktop != "" {
			setup.environment["XDG_CURRENT_DESKTOP"] = desktop
		}
	}

	sort.SliceStable(setup.mounts, func(i, j int) bool {
		return setup.mounts[i].target < setup.mounts[j].target
	})
	return setup, nil
}

func waylandSocketPath() (string, error) {
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		display = "wayland-0"
	}
	if filepath.IsAbs(display) {
		if filepath.Clean(display) != display {
			return "", fmt.Errorf("profile.gui.wayland: WAYLAND_DISPLAY must be a normalized absolute socket path")
		}
		return display, nil
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir {
		return "", fmt.Errorf("profile.gui.wayland: XDG_RUNTIME_DIR must be set to a normalized absolute path when WAYLAND_DISPLAY is relative")
	}
	if filepath.Clean(display) != display || display == "." || display == ".." || strings.HasPrefix(display, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("profile.gui.wayland: WAYLAND_DISPLAY must name a socket inside XDG_RUNTIME_DIR")
	}
	return filepath.Join(runtimeDir, display), nil
}

func x11DisplaySocket(display string) (string, string, error) {
	if display == "" {
		return "", "", fmt.Errorf("profile.gui.x11: DISPLAY is not set")
	}
	value := display
	switch {
	case strings.HasPrefix(value, "unix:"):
		value = strings.TrimPrefix(value, "unix:")
	case strings.HasPrefix(value, ":"):
		value = strings.TrimPrefix(value, ":")
	default:
		return "", "", fmt.Errorf("profile.gui.x11: only a local Unix DISPLAY is supported; got %q", display)
	}
	displayNumber, screen, hasScreen := strings.Cut(value, ".")
	if !decimalNumber(displayNumber) || hasScreen && !decimalNumber(screen) {
		return "", "", fmt.Errorf("profile.gui.x11: DISPLAY %q must identify a local numeric display", display)
	}
	parsedDisplay, err := strconv.ParseUint(displayNumber, 10, 32)
	if err != nil {
		return "", "", fmt.Errorf("profile.gui.x11: DISPLAY %q has an invalid display number", display)
	}
	displayNumber = strconv.FormatUint(parsedDisplay, 10)
	normalized := ":" + displayNumber
	if hasScreen {
		normalized += "." + screen
	}
	return filepath.Join("/tmp/.X11-unix", "X"+displayNumber), normalized, nil
}

func decimalNumber(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func xAuthorityPath() (string, bool, error) {
	path := os.Getenv("XAUTHORITY")
	explicit := path != ""
	if !explicit {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("profile.gui.x11: resolve the host user's Xauthority file: %w", err)
		}
		path = filepath.Join(home, ".Xauthority")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return "", false, nil
		}
		return "", false, fmt.Errorf("profile.gui.x11: resolve Xauthority file %q: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false, fmt.Errorf("profile.gui.x11: inspect Xauthority file %q: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("profile.gui.x11: Xauthority path %q must be a regular file", path)
	}
	return resolved, true, nil
}

func audioAccess() ([]profileMount, map[string]string, error) {
	mounts := make([]profileMount, 0, 3)
	environment := make(map[string]string)
	pipewireRuntime := os.Getenv("PIPEWIRE_RUNTIME_DIR")
	if pipewireRuntime == "" {
		pipewireRuntime = os.Getenv("XDG_RUNTIME_DIR")
	}
	if pipewireRuntime != "" && (!filepath.IsAbs(pipewireRuntime) || filepath.Clean(pipewireRuntime) != pipewireRuntime) {
		return nil, nil, fmt.Errorf("profile.gui.audio: PIPEWIRE_RUNTIME_DIR/XDG_RUNTIME_DIR must be a normalized absolute path")
	}

	hasAudio := false
	if pipewireRuntime != "" {
		remote := os.Getenv("PIPEWIRE_REMOTE")
		if remote == "" {
			remote = "pipewire-0"
		}
		if filepath.Base(remote) != remote || filepath.Clean(remote) != remote || remote == "." || remote == ".." {
			return nil, nil, fmt.Errorf("profile.gui.audio: PIPEWIRE_REMOTE must be a socket name, not a path")
		}
		pipewirePath := filepath.Join(pipewireRuntime, remote)
		source, exists, err := optionalUnixSocket("profile.gui.audio PipeWire", pipewirePath)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			target := filepath.Join(sandboxGUIRuntime, remote)
			mounts = append(mounts, profileMount{source: source, target: target})
			environment["PIPEWIRE_REMOTE"] = remote
			environment["PIPEWIRE_RUNTIME_DIR"] = sandboxGUIRuntime
			hasAudio = true
		}
	}

	pulsePath := os.Getenv("PULSE_SERVER")
	if pulsePath != "" {
		var err error
		pulsePath, err = pulseUnixSocketPath(pulsePath)
		if err != nil {
			return nil, nil, err
		}
	} else if pipewireRuntime != "" {
		pulsePath = filepath.Join(pipewireRuntime, "pulse", "native")
	}
	if pulsePath != "" {
		source, exists, err := optionalUnixSocket("profile.gui.audio PulseAudio", pulsePath)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			target := filepath.Join(sandboxGUIRuntime, "pulse", "native")
			mounts = append(mounts, profileMount{source: source, target: target})
			environment["PULSE_SERVER"] = "unix:" + target
			cookie, cookieExists, cookieErr := pulseCookiePath()
			if cookieErr != nil {
				return nil, nil, cookieErr
			}
			if cookieExists {
				cookieTarget := filepath.Join(sandboxGUIRuntime, "pulse-cookie")
				mounts = append(mounts, profileMount{source: cookie, target: cookieTarget})
				environment["PULSE_COOKIE"] = cookieTarget
			}
			hasAudio = true
		}
	}

	sndPath := "/dev/snd"
	if info, err := os.Lstat(sndPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(sndPath)
			if resolveErr != nil {
				return nil, nil, fmt.Errorf("profile.gui.audio: resolve %s: %w", sndPath, resolveErr)
			}
			sndPath = resolved
			info, err = os.Stat(sndPath)
		}
		if err != nil || !info.IsDir() {
			return nil, nil, fmt.Errorf("profile.gui.audio: %s must be a directory of audio devices", sndPath)
		}
		hasDevice, deviceErr := directoryContainsCharacterDevice(sndPath)
		if deviceErr != nil {
			return nil, nil, fmt.Errorf("profile.gui.audio: inspect devices in %s: %w", sndPath, deviceErr)
		}
		if hasDevice {
			mounts = append(mounts, profileMount{source: sndPath, target: "/dev/snd", device: true})
			hasAudio = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("profile.gui.audio: inspect %s: %w", sndPath, err)
	}
	if !hasAudio {
		return nil, nil, fmt.Errorf("profile.gui.audio: no local PipeWire/PulseAudio socket or /dev/snd devices are available")
	}
	return mounts, environment, nil
}

func pulseUnixSocketPath(address string) (string, error) {
	if !strings.HasPrefix(address, "unix:") {
		return "", fmt.Errorf("profile.gui.audio: only a local Unix PULSE_SERVER is supported")
	}
	value := strings.TrimPrefix(address, "unix:")
	if strings.HasPrefix(value, "path=") {
		value = strings.TrimPrefix(value, "path=")
		if comma := strings.IndexByte(value, ','); comma >= 0 {
			value = value[:comma]
		}
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return "", fmt.Errorf("profile.gui.audio: invalid PULSE_SERVER path escape: %w", err)
		}
		value = decoded
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("profile.gui.audio: PULSE_SERVER must name a normalized absolute Unix socket path")
	}
	return value, nil
}

func pulseCookiePath() (string, bool, error) {
	if explicit := os.Getenv("PULSE_COOKIE"); explicit != "" {
		path, err := regularHostFile("profile.gui.audio PULSE_COOKIE", explicit)
		return path, err == nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("profile.gui.audio: resolve host user's PulseAudio cookie: %w", err)
	}
	for _, candidate := range []string{
		filepath.Join(home, ".config/pulse/cookie"),
		filepath.Join(home, ".pulse-cookie"),
	} {
		path, err := regularHostFile("profile.gui.audio PulseAudio cookie", candidate)
		if err == nil {
			return path, true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		return "", false, err
	}
	return "", false, nil
}

func regularHostFile(permission, path string) (string, error) {
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("%s: resolve host file path %q: %w", permission, path, err)
		}
		path = absolute
	}
	if filepath.Clean(path) != path {
		return "", fmt.Errorf("%s: host file path must be normalized", permission)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s: resolve host file %q: %w", permission, path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%s: inspect host file %q: %w", permission, resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %q is not a regular file", permission, path)
	}
	return resolved, nil
}

func gpuDeviceMounts() ([]profileMount, error) {
	candidates := []string{"/dev/dri", "/dev/kfd", "/dev/nvidia-caps"}
	nvidia, err := filepath.Glob("/dev/nvidia*")
	if err != nil {
		return nil, fmt.Errorf("profile.gui.gpu: find NVIDIA devices: %w", err)
	}
	candidates = append(candidates, nvidia...)
	sort.Strings(candidates)
	mounts := make([]profileMount, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		info, err := os.Lstat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("profile.gui.gpu: inspect %s: %w", candidate, err)
		}
		source := candidate
		if info.Mode()&os.ModeSymlink != 0 {
			source, err = filepath.EvalSymlinks(candidate)
			if err != nil {
				return nil, fmt.Errorf("profile.gui.gpu: resolve %s: %w", candidate, err)
			}
			info, err = os.Stat(source)
			if err != nil {
				return nil, fmt.Errorf("profile.gui.gpu: inspect %s: %w", source, err)
			}
		}
		isAllowedDirectory := false
		if info.IsDir() && (candidate == "/dev/dri" || candidate == "/dev/nvidia-caps") {
			hasDevice, deviceErr := directoryContainsCharacterDevice(source)
			if deviceErr != nil {
				return nil, fmt.Errorf("profile.gui.gpu: inspect devices in %s: %w", source, deviceErr)
			}
			isAllowedDirectory = hasDevice
		}
		isCharacterDevice := info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice != 0
		if !isAllowedDirectory && !isCharacterDevice {
			continue
		}
		mounts = append(mounts, profileMount{source: source, target: candidate, device: true})
	}
	if len(mounts) == 0 {
		return nil, fmt.Errorf("profile.gui.gpu: no supported GPU devices found under /dev/dri, /dev/kfd, or /dev/nvidia*")
	}
	return mounts, nil
}

func sessionBusSocketPaths() ([]string, error) {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if address == "" {
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if !filepath.IsAbs(runtimeDir) || filepath.Clean(runtimeDir) != runtimeDir {
			return nil, fmt.Errorf("profile.gui.desktop: DBUS_SESSION_BUS_ADDRESS or a normalized XDG_RUNTIME_DIR is required")
		}
		return []string{filepath.Join(runtimeDir, "bus")}, nil
	}

	paths := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, candidate := range strings.Split(address, ";") {
		if !strings.HasPrefix(candidate, "unix:") {
			continue
		}
		for _, attribute := range strings.Split(strings.TrimPrefix(candidate, "unix:"), ",") {
			name, value, found := strings.Cut(attribute, "=")
			if !found || name != "path" {
				continue
			}
			path, err := url.PathUnescape(value)
			if err != nil {
				return nil, fmt.Errorf("profile.gui.desktop: invalid D-Bus session socket path escape: %w", err)
			}
			if !filepath.IsAbs(path) || filepath.Clean(path) != path {
				return nil, fmt.Errorf("profile.gui.desktop: D-Bus session bus must use a normalized absolute Unix socket path")
			}
			if _, exists := seen[path]; !exists {
				paths = append(paths, path)
				seen[path] = struct{}{}
			}
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("profile.gui.desktop: only filesystem Unix session-bus addresses are supported")
	}
	return paths, nil
}

func directoryContainsCharacterDevice(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(path, entry.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		if info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice != 0 {
			return true, nil
		}
	}
	return false, nil
}

func unixSocketMount(permission, path, target string) (profileMount, error) {
	source, err := requiredUnixSocket(permission, path)
	if err != nil {
		return profileMount{}, err
	}
	return profileMount{source: source, target: target}, nil
}

func requiredUnixSocket(permission, path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("%s: host socket path must be normalized and absolute", permission)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s: resolve host socket %q: %w", permission, path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%s: inspect host socket %q: %w", permission, resolved, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return "", fmt.Errorf("%s: %q is not a Unix socket", permission, path)
	}
	return resolved, nil
}

func optionalUnixSocket(permission, path string) (string, bool, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", false, fmt.Errorf("%s: host socket path must be normalized and absolute", permission)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("%s: resolve host socket %q: %w", permission, path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", false, fmt.Errorf("%s: inspect host socket %q: %w", permission, resolved, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return "", false, fmt.Errorf("%s: %q is not a Unix socket", permission, path)
	}
	return resolved, true, nil
}
