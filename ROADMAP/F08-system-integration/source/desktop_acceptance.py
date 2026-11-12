#!/usr/bin/env python3
"""Live acceptance checks for optional XDG integration and desktop launchers."""

import argparse
import os
import pathlib
import shutil
import subprocess
import tempfile
import time


def run(binary, env, *arguments, timeout=30):
    return subprocess.run(
        [str(binary), *arguments],
        env=env,
        text=True,
        capture_output=True,
        timeout=timeout,
    )


def write_profile(root, app_home):
    profiles = root / "profiles with spaces"
    profiles.mkdir(mode=0o700)
    profile = profiles / "desktop profile.unic"
    profile.write_text(f"version 1;\nhome_path '{app_home}';\n", encoding="utf-8")
    return profile


def check_run(binary, env, profile, app_id, marker, label):
    result = run(
        binary,
        env,
        "run",
        f"--profile={profile}",
        f"--app-id={app_id}",
        "--",
        "/usr/bin/touch",
        f"/home/isobox/{marker.name}",
    )
    if result.returncode != 0 or not marker.is_file():
        raise RuntimeError(
            f"{label} failed (exit={result.returncode})\n"
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
        )
    print(f"PASS {label}")


def private_directory(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    path.chmod(0o700)


def write_settings(directory, contents):
    settings = directory / "isobox" / "settings.unic"
    private_directory(settings.parent)
    settings.write_text(contents, encoding="utf-8")


def settings_environment(base, system_directories, user_config, bwrap_only):
    env = base.copy()
    env["XDG_CONFIG_DIRS"] = ":".join(map(str, system_directories))
    env["XDG_CONFIG_HOME"] = str(user_config)
    env["PATH"] = str(bwrap_only)
    return env


def test_optional_cli_and_settings(binary, env, root, profile, app_home, bwrap_only):
    empty_system = root / "empty-system-config"
    user_config = root / "user-config"
    private_directory(empty_system)
    private_directory(user_config)
    base = env.copy()
    base["XDG_CONFIG_DIRS"] = str(empty_system)
    base["XDG_CONFIG_HOME"] = str(user_config)
    base["XDG_DATA_HOME"] = str(root / "unused-xdg-data")

    help_result = run(binary, base, "help", timeout=10)
    if help_result.returncode != 0 or "Usage:" not in help_result.stdout:
        raise RuntimeError(
            "base CLI help failed without installed desktop integration\n"
            f"stdout:\n{help_result.stdout}\nstderr:\n{help_result.stderr}"
        )
    print("PASS base CLI works without launcher files or settings")

    plain_marker = app_home / "plain CLI marker"
    check_run(binary, settings_environment(base, [empty_system], user_config, bwrap_only), profile, "f08-plain", plain_marker,
              "direct CLI run works without desktop integration or settings")

    first_system = root / "first-system-config"
    later_system = root / "later-system-config"
    write_settings(first_system, "version 1; defaults { network_mode off; }")
    write_settings(later_system, "version 1; defaults { network_mode direct; }")
    write_settings(user_config, "version 1; defaults { dns '192.0.2.53'; }")
    first_marker = app_home / "first system marker"
    check_run(
        binary,
        settings_environment(base, [first_system, later_system], user_config, bwrap_only),
        profile,
        "f08-system-priority",
        first_marker,
        "first system XDG file wins and user DNS override merges by field",
    )

    direct_system = root / "direct-system-config"
    write_settings(direct_system, "version 1; defaults { network_mode direct; }")
    write_settings(user_config, "version 1; defaults { network_mode off; }")
    user_marker = app_home / "user override marker"
    check_run(
        binary,
        settings_environment(base, [direct_system], user_config, bwrap_only),
        profile,
        "f08-user-priority",
        user_marker,
        "user network default overrides system direct with pasta and slirp absent",
    )


def test_desktop_launcher(binary, env, root, profile, app_home):
    data_home = root / "xdg data with spaces"
    private_directory(data_home)
    env = env.copy()
    env["XDG_DATA_HOME"] = str(data_home)
    env["XDG_CONFIG_HOME"] = str(root / "empty-launcher-user-config")
    env["XDG_CONFIG_DIRS"] = str(root / "empty-launcher-system-config")
    private_directory(pathlib.Path(env["XDG_CONFIG_HOME"]))
    private_directory(pathlib.Path(env["XDG_CONFIG_DIRS"]))

    marker_name = "launcher marker 100% $literal"
    marker = app_home / marker_name
    installed = run(
        binary,
        env,
        "integration",
        "install",
        "--id=f08-runtime",
        "--name=F08 Desktop Acceptance",
        f"--profile={profile}",
        "--app-id=f08-runtime",
        "--",
        "/usr/bin/touch",
        f"/home/isobox/{marker_name}",
        timeout=10,
    )
    if installed.returncode != 0:
        raise RuntimeError(
            f"launcher install failed (exit={installed.returncode})\n"
            f"stdout:\n{installed.stdout}\nstderr:\n{installed.stderr}"
        )

    launcher = data_home / "applications" / "isobox-f08-runtime.desktop"
    if not launcher.is_file() or launcher.stat().st_mode & 0o777 != 0o600:
        raise RuntimeError(f"launcher is absent or does not have mode 0600: {launcher}")
    validation = subprocess.run(
        ["desktop-file-validate", str(launcher)],
        text=True,
        capture_output=True,
        timeout=10,
    )
    if validation.returncode != 0:
        raise RuntimeError(
            f"desktop-file-validate failed\nstdout:\n{validation.stdout}\nstderr:\n{validation.stderr}"
        )
    print("PASS install creates a valid mode-0600 launcher under a spaced XDG path")

    launch = subprocess.run(
        ["gio", "launch", str(launcher)],
        env=env,
        text=True,
        capture_output=True,
        timeout=15,
    )
    if launch.returncode != 0:
        raise RuntimeError(
            f"gio launch failed (exit={launch.returncode})\n"
            f"stdout:\n{launch.stdout}\nstderr:\n{launch.stderr}"
        )
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline and not marker.exists():
        time.sleep(0.05)
    if not marker.is_file():
        raise RuntimeError(
            "gio launch returned success but the bwrap payload did not create its marker\n"
            f"stdout:\n{launch.stdout}\nstderr:\n{launch.stderr}"
        )
    print("PASS gio launch runs isobox with profile path and argv containing spaces, %, and $")

    removed = run(binary, env, "integration", "remove", "--id=f08-runtime", timeout=10)
    if removed.returncode != 0 or launcher.exists():
        raise RuntimeError(
            f"launcher remove failed (exit={removed.returncode})\n"
            f"stdout:\n{removed.stdout}\nstderr:\n{removed.stderr}"
        )
    if not profile.is_file() or not marker.is_file():
        raise RuntimeError("removing the launcher changed the profile or application HOME")
    print("PASS remove deletes only the managed launcher; profile and app HOME persist")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("isobox", type=pathlib.Path, help="built isobox executable")
    args = parser.parse_args()
    binary = args.isobox.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error(f"{binary} is not an executable file")
    if os.geteuid() == 0:
        parser.error("run acceptance as an ordinary user, not root")
    if not os.environ.get("DISPLAY") and not os.environ.get("WAYLAND_DISPLAY"):
        parser.error("an active X11 or Wayland desktop session is required for gio launch")
    for command in ("desktop-file-validate", "gio"):
        if not shutil.which(command):
            parser.error(f"{command} must be installed and available in PATH")
    bwrap = shutil.which("bwrap")
    if not bwrap:
        parser.error("bwrap must be installed and available in PATH")

    with tempfile.TemporaryDirectory(prefix="isobox-desktop-acceptance-") as directory:
        root = pathlib.Path(directory)
        host_home = root / "host-home"
        app_home = root / "application-home"
        private_directory(host_home)
        private_directory(app_home)
        profile = write_profile(root, app_home)
        bwrap_only = root / "bwrap-only"
        private_directory(bwrap_only)
        (bwrap_only / "bwrap").symlink_to(bwrap)
        env = os.environ.copy()
        env.update({
            "HOME": str(host_home),
            "XDG_DATA_HOME": str(root / "unused-xdg-data"),
            "XDG_CONFIG_HOME": str(root / "empty-user-config"),
            "XDG_CONFIG_DIRS": str(root / "empty-system-config"),
        })
        test_optional_cli_and_settings(binary, env, root, profile, app_home, bwrap_only)
        test_desktop_launcher(binary, env, root, profile, app_home)
        print("desktop integration acceptance completed")


if __name__ == "__main__":
    main()
