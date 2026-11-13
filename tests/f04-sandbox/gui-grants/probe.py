#!/usr/bin/env python3
import glob
import os
from pathlib import Path
import socket
import stat
import sys


mode = sys.argv[1]
marker_name = sys.argv[2]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


grant_environment = (
    "XDG_RUNTIME_DIR",
    "WAYLAND_DISPLAY",
    "DISPLAY",
    "XAUTHORITY",
    "PIPEWIRE_RUNTIME_DIR",
    "PIPEWIRE_REMOTE",
    "PULSE_SERVER",
    "PULSE_COOKIE",
    "DBUS_SESSION_BUS_ADDRESS",
    "DBUS_SYSTEM_BUS_ADDRESS",
    "XDG_CURRENT_DESKTOP",
)


def require_absent(names):
    for name in names:
        require(name not in os.environ, f"ungranted environment variable {name} is visible")


def runtime_entries(expected):
    runtime = Path("/run/isobox")
    require(runtime.is_dir(), "/run/isobox is unavailable for this grant")
    require(stat.S_IMODE(runtime.stat().st_mode) == 0o700, "/run/isobox is not private mode 0700")
    require(sorted(path.name for path in runtime.iterdir()) == sorted(expected), "unexpected GUI resource was mounted")


def connect_echo(path):
    client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    client.settimeout(3)
    try:
        client.connect(str(path))
        require(client.recv(2) == b"OK", f"GUI socket {path} did not answer")
    finally:
        client.close()


def require_readonly_mount(target):
    for line in Path("/proc/self/mountinfo").read_text(encoding="utf-8").splitlines():
        fields = line.split()
        if len(fields) > 5 and fields[4].replace("\\040", " ") == target:
            require("ro" in fields[5].split(","), f"{target} is not mounted read-only")
            return
    raise RuntimeError(f"{target} is not an independent mount")


def require_tmpfs_mount(target):
    for line in Path("/proc/self/mountinfo").read_text(encoding="utf-8").splitlines():
        fields = line.split()
        if len(fields) > 5 and fields[4].replace("\\040", " ") == target:
            separator = fields.index("-")
            require(fields[separator + 1] == "tmpfs", f"{target} is not a private tmpfs")
            return
    raise RuntimeError(f"{target} is not an independent mount")


def has_gpu_device():
    for path in ("/dev/dri", "/dev/kfd", "/dev/nvidia-caps", *glob.glob("/dev/nvidia*")):
        try:
            info = os.lstat(path)
        except FileNotFoundError:
            continue
        if stat.S_ISCHR(info.st_mode):
            return True
        if stat.S_ISDIR(info.st_mode) and path in {"/dev/dri", "/dev/nvidia-caps"}:
            if any(stat.S_ISCHR(os.stat(entry.path).st_mode) for entry in os.scandir(path)):
                return True
    return False


require(os.environ.get("HOME") == "/home/isobox", "sandbox HOME differs from the profile")
require("ISOBOX_GUI_TEST_SECRET" not in os.environ, "host environment was not cleared")
require("ISOBOX_GUI_TEST_MODE" not in os.environ, "test mode variable leaked into the app")
require("ISOBOX_GUI_TEST_MARKER" not in os.environ, "test marker variable leaked into the app")
require(not Path("/run/dbus/system_bus_socket").exists(), "system D-Bus socket is visible")
require_tmpfs_mount("/run")
if not mode.startswith("x11:"):
    require(not Path("/tmp/.X11-unix").exists(), "X11 socket path is visible without the X11 grant")
if mode not in {"pipewire", "pulse"}:
    require(not Path("/dev/snd").exists(), "audio devices are visible without the audio grant")
if mode != "gpu":
    require(not has_gpu_device(), "GPU devices are visible without the GPU grant")

if mode == "default":
    require_absent(grant_environment)
    require(not Path("/run/isobox").exists(), "GUI runtime directory is exposed without grants")
    require(not Path("/tmp/.X11-unix").exists(), "X11 socket directory is exposed without a grant")
    for path in ("/dev/snd", "/dev/dri", "/dev/kfd", "/dev/nvidia-caps"):
        require(not Path(path).exists(), f"device path {path} is exposed without a grant")
    require(not glob.glob("/dev/nvidia[0-9]*"), "NVIDIA devices are exposed without a grant")
elif mode == "wayland":
    require(os.environ.get("XDG_RUNTIME_DIR") == "/run/isobox", "Wayland runtime dir was not normalized")
    require(os.environ.get("WAYLAND_DISPLAY") == "wayland-0", "Wayland display was not normalized")
    require_absent(name for name in grant_environment if name not in {"XDG_RUNTIME_DIR", "WAYLAND_DISPLAY"})
    runtime_entries(["wayland-0"])
    require(stat.S_ISSOCK(os.stat("/run/isobox/wayland-0").st_mode), "Wayland socket is unavailable")
    require_readonly_mount("/run/isobox/wayland-0")
    connect_echo("/run/isobox/wayland-0")
elif mode.startswith("x11:"):
    display_number = mode.split(":", 1)[1]
    require(os.environ.get("XDG_RUNTIME_DIR") == "/run/isobox", "X11 runtime dir was not normalized")
    require(os.environ.get("DISPLAY") == ":" + display_number, "X11 display was not normalized")
    require(os.environ.get("XAUTHORITY") == "/run/isobox/Xauthority", "Xauthority was not relocated")
    require_absent(name for name in grant_environment if name not in {"XDG_RUNTIME_DIR", "DISPLAY", "XAUTHORITY"})
    runtime_entries(["Xauthority"])
    require(Path("/run/isobox/Xauthority").read_text(encoding="utf-8") == "synthetic Xauthority data\n", "Xauthority content differs")
    require_readonly_mount("/run/isobox/Xauthority")
    socket_path = f"/tmp/.X11-unix/X{display_number}"
    require(stat.S_ISSOCK(os.stat(socket_path).st_mode), "X11 socket was not mounted")
    require_readonly_mount(socket_path)
    connect_echo(socket_path)
elif mode == "pipewire":
    require(os.environ.get("XDG_RUNTIME_DIR") == "/run/isobox", "audio runtime dir was not normalized")
    require(os.environ.get("PIPEWIRE_RUNTIME_DIR") == "/run/isobox", "PipeWire runtime dir is not sandbox-local")
    require(os.environ.get("PIPEWIRE_REMOTE") == "pipewire-0", "PipeWire remote differs")
    require_absent(name for name in grant_environment if name not in {"XDG_RUNTIME_DIR", "PIPEWIRE_RUNTIME_DIR", "PIPEWIRE_REMOTE"})
    runtime_entries(["pipewire-0"])
    require(stat.S_ISSOCK(os.stat("/run/isobox/pipewire-0").st_mode), "PipeWire socket is unavailable")
    require_readonly_mount("/run/isobox/pipewire-0")
    connect_echo("/run/isobox/pipewire-0")
elif mode == "pulse":
    require(os.environ.get("XDG_RUNTIME_DIR") == "/run/isobox", "audio runtime dir was not normalized")
    require(os.environ.get("PULSE_SERVER") == "unix:/run/isobox/pulse/native", "PulseAudio server is not sandbox-local")
    require(os.environ.get("PULSE_COOKIE") == "/run/isobox/pulse-cookie", "PulseAudio cookie path was not relocated")
    require_absent(name for name in grant_environment if name not in {"XDG_RUNTIME_DIR", "PULSE_SERVER", "PULSE_COOKIE"})
    runtime_entries(["pulse", "pulse-cookie"])
    require(Path("/run/isobox/pulse-cookie").read_text(encoding="utf-8") == "synthetic PulseAudio cookie\n", "PulseAudio cookie content differs")
    require_readonly_mount("/run/isobox/pulse-cookie")
    require(stat.S_ISSOCK(os.stat("/run/isobox/pulse/native").st_mode), "PulseAudio socket is unavailable")
    require_readonly_mount("/run/isobox/pulse/native")
    connect_echo("/run/isobox/pulse/native")
elif mode == "desktop":
    require(os.environ.get("XDG_RUNTIME_DIR") == "/run/isobox", "desktop runtime dir was not normalized")
    require(os.environ.get("DBUS_SESSION_BUS_ADDRESS") == "unix:path=/run/isobox/bus", "session bus was not relocated")
    require(os.environ.get("XDG_CURRENT_DESKTOP") == "isobox-test", "desktop name was not retained")
    require_absent(name for name in grant_environment if name not in {"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_CURRENT_DESKTOP"})
    runtime_entries(["bus"])
    require(stat.S_ISSOCK(os.stat("/run/isobox/bus").st_mode), "session bus socket is unavailable")
    require_readonly_mount("/run/isobox/bus")
    connect_echo("/run/isobox/bus")
elif mode == "gpu":
    require_absent(grant_environment)
    require(not Path("/run/isobox").exists(), "GPU grant exposed unrelated GUI runtime")
    devices = []
    for path in ("/dev/dri", "/dev/kfd", "/dev/nvidia-caps", *glob.glob("/dev/nvidia*")):
        try:
            info = os.lstat(path)
        except FileNotFoundError:
            continue
        if stat.S_ISCHR(info.st_mode):
            devices.append(path)
        elif stat.S_ISDIR(info.st_mode) and path in {"/dev/dri", "/dev/nvidia-caps"}:
            if any(stat.S_ISCHR(os.stat(entry.path).st_mode) for entry in os.scandir(path)):
                devices.append(path)
    require(devices, "GPU grant did not expose a supported character device")
    require(not Path("/dev/snd").exists(), "GPU grant exposed audio devices")
else:
    raise RuntimeError("unknown GUI test mode: " + mode)

safe_name = "".join(character if character.isalnum() or character in "-_" else "_" for character in marker_name)
Path("/home/isobox", safe_name).write_text("started\n", encoding="utf-8")
print("PASS GUI grant " + mode, flush=True)
