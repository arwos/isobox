#!/usr/bin/env python3
"""Live acceptance checks for isobox cgroup and rlimit enforcement."""

import argparse
import os
import pathlib
import shutil
import socket
import subprocess
import tempfile
import textwrap
import time


def write_profile(root, limits, published_port=None):
    root.mkdir(parents=True, exist_ok=True)
    home = root / "sandbox-home"
    home.mkdir(mode=0o700)
    profile = root / "profile.unic"
    contents = ["version 1;", f"home_path '{home}';"]
    if published_port is not None:
        contents.extend(
            [
                "network {",
                "  mode off;",
                "  port {",
                "    host_address '127.0.0.1';",
                f"    port {published_port};",
                "    protocol tcp;",
                "  }",
                "}",
            ]
        )
    contents.append(f"limits {{ {limits} }}")
    profile.write_text("\n".join(contents) + "\n", encoding="utf-8")
    return profile, home


def run_app(binary, profile, environment, app, timeout=20):
    command = [
        str(binary),
        "run",
        f"--profile={profile}",
        "--app-id=f07-acceptance",
        "--",
        "/usr/bin/python3",
        "-c",
        textwrap.dedent(app),
    ]
    return subprocess.run(
        command,
        env=environment,
        text=True,
        capture_output=True,
        timeout=timeout,
    )


def cgroup_directory(cgroup_path):
    for line in pathlib.Path("/proc/self/mountinfo").read_text(encoding="utf-8").splitlines():
        fields = line.split()
        separator = fields.index("-")
        if fields[separator + 1] != "cgroup2":
            continue
        root, mount = pathlib.Path(fields[3]), pathlib.Path(fields[4])
        relative = os.path.relpath(cgroup_path, root)
        if relative == ".." or relative.startswith(".." + os.sep):
            raise RuntimeError(f"application cgroup {cgroup_path!r} is outside mount root {str(root)!r}")
        return mount / relative
    raise RuntimeError("host has no visible cgroup v2 mount")


def test_open_files_only(binary, environment, root):
    profile, _ = write_profile(root, "open_files 32;")
    app = """
        import resource
        limits = resource.getrlimit(resource.RLIMIT_NOFILE)
        if limits != (32, 32):
            raise SystemExit("unexpected RLIMIT_NOFILE: %r" % (limits,))
        print("open_files=32/32")
    """
    result = run_app(binary, profile, environment, app)
    if result.returncode != 0 or "open_files=32/32" not in result.stdout:
        raise RuntimeError(
            f"open-files-only acceptance failed (exit={result.returncode})\n"
            f"stdout:\n{result.stdout}\nstderr:\n{result.stderr}"
        )
    print("PASS open_files-only profile applies soft and hard RLIMIT_NOFILE")


def test_combined_limits(binary, environment, root, include_off_published_port):
    published_port = None
    if include_off_published_port:
        reservation = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        reservation.bind(("127.0.0.1", 0))
        published_port = reservation.getsockname()[1]
        reservation.close()
    profile, home = write_profile(
        root,
        "cpu_quota_percent 50; memory_bytes 268435456; process_count 4; open_files 64;",
        published_port,
    )
    app = """
        import os, resource, threading, time
        stop = threading.Event()
        workers = []
        for _ in range(3):
            worker = threading.Thread(target=stop.wait)
            worker.start()
            workers.append(worker)
        cgroup = open("/proc/self/cgroup", encoding="utf-8").read().strip().split("::", 1)[1]
        with open("/home/isobox/ready", "w", encoding="utf-8") as marker:
            marker.write(cgroup)
        deadline = time.monotonic() + 10
        while not os.path.exists("/home/isobox/continue") and time.monotonic() < deadline:
            time.sleep(0.01)
        try:
            extra = threading.Thread(target=stop.wait)
            extra.start()
            extra_started = True
            workers.append(extra)
        except RuntimeError:
            extra_started = False
        stop.set()
        for worker in workers:
            worker.join()
        nofile = resource.getrlimit(resource.RLIMIT_NOFILE)
        with open("/home/isobox/result", "w", encoding="utf-8") as marker:
            marker.write(f"{extra_started} {nofile[0]} {nofile[1]}\\n")
        time.sleep(2)
        if extra_started:
            raise SystemExit("process_count allowed a task beyond the requested budget")
        if nofile != (64, 64):
            raise SystemExit("open_files was not applied exactly: %r" % (nofile,))
        print("CPU, memory, process/thread and open-files limits passed")
    """
    command = [
        str(binary),
        "run",
        f"--profile={profile}",
        "--app-id=f07-acceptance",
        "--",
        "/usr/bin/python3",
        "-c",
        textwrap.dedent(app),
    ]
    process = subprocess.Popen(
        command,
        env=environment,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        ready = home / "ready"
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline and not ready.exists() and process.poll() is None:
            time.sleep(0.05)
        if not ready.exists():
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=3)
            stdout, stderr = process.communicate()
            raise RuntimeError(
                f"limited application did not reach its startup marker "
                f"(exit={process.returncode})\nstdout:\n{stdout}\nstderr:\n{stderr}"
            )

        cgroup = cgroup_directory(ready.read_text(encoding="utf-8"))
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                cpu_fields = (cgroup / "cpu.max").read_text(encoding="ascii").split()
                memory = int((cgroup / "memory.max").read_text(encoding="ascii").strip())
                tasks = int((cgroup / "pids.max").read_text(encoding="ascii").strip())
                current = int((cgroup / "pids.current").read_text(encoding="ascii").strip())
                break
            except FileNotFoundError:
                time.sleep(0.05)
        else:
            raise RuntimeError(f"resource control files did not appear under {cgroup}")

        if len(cpu_fields) != 2 or int(cpu_fields[0]) * 100 != int(cpu_fields[1]) * 50:
            raise RuntimeError(f"CPU quota was not exactly 50%: {' '.join(cpu_fields)}")
        if memory != 268435456:
            raise RuntimeError(f"memory.max={memory}, want 268435456")
        if tasks != 6:
            raise RuntimeError(f"pids.max={tasks}, want process_count 4 plus two bwrap tasks")
        if current != tasks:
            raise RuntimeError(f"pids.current={current}, want {tasks} with four app tasks active")

        (home / "continue").write_text("continue", encoding="ascii")
        result_marker = home / "result"
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and not result_marker.exists() and process.poll() is None:
            time.sleep(0.05)
        if not result_marker.exists():
            raise RuntimeError("payload did not report thread and RLIMIT results")
        extra_started, soft, hard = result_marker.read_text(encoding="ascii").split()
        if extra_started != "False" or (int(soft), int(hard)) != (64, 64):
            raise RuntimeError(f"payload result was {result_marker.read_text()!r}")

        returncode = process.wait(timeout=10)
        stdout, stderr = process.communicate()
        if returncode != 0 or "limits passed" not in stdout:
            raise RuntimeError(
                f"isobox exited {returncode}\nstdout:\n{stdout}\nstderr:\n{stderr}"
            )

        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and cgroup.exists():
            time.sleep(0.05)
        if cgroup.exists():
            raise RuntimeError(f"transient scope cgroup was not cleaned up: {cgroup}")
        print(
            f"PASS CPUQuota=50%, memory.max={memory}, pids.max={tasks}; "
            "four app tasks allowed, extra task denied, RLIMIT_NOFILE=64/64, scope removed"
        )
        if published_port is not None:
            print("PASS off-mode firewall setup task allowance was removed before the app gate opened")
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        process.communicate()


def test_missing_systemd_fails_closed(binary, environment, bwrap_only, root):
    profile, home = write_profile(root, "cpu_quota_percent 50;")
    restricted = environment.copy()
    restricted["PATH"] = str(bwrap_only)
    app = "open('/home/isobox/should-not-run', 'w').write('started')"
    result = run_app(binary, profile, restricted, app)
    if result.returncode == 0 or (home / "should-not-run").exists():
        raise RuntimeError("CPU-limited payload started without systemd-run")
    if "install systemd-run" not in result.stderr:
        raise RuntimeError(f"missing systemd-run produced an unexpected error:\n{result.stderr}")
    print("PASS requested cgroup limit fails closed when systemd-run is absent")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("isobox", type=pathlib.Path, help="built isobox executable")
    args = parser.parse_args()
    binary = args.isobox.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error(f"{binary} is not an executable file")
    if os.geteuid() == 0:
        parser.error("run acceptance as an ordinary user, not root")

    bwrap = shutil.which("bwrap")
    systemd_run = shutil.which("systemd-run")
    if not bwrap or not systemd_run:
        parser.error("bwrap and systemd-run must be installed and available in PATH")

    with tempfile.TemporaryDirectory(prefix="isobox-limits-acceptance-") as temp:
        root = pathlib.Path(temp)
        path_dir = root / "bin"
        bwrap_only = root / "bwrap-only"
        path_dir.mkdir()
        bwrap_only.mkdir()
        (path_dir / "bwrap").symlink_to(bwrap)
        (path_dir / "systemd-run").symlink_to(systemd_run)
        (bwrap_only / "bwrap").symlink_to(bwrap)
        off_forwarding_tools = all(shutil.which(tool) for tool in ("pasta", "nft", "systemctl"))
        for tool in ("pasta", "nft", "systemctl"):
            tool_path = shutil.which(tool)
            if tool_path:
                (path_dir / tool).symlink_to(tool_path)
        host_home = root / "host-home"
        config_home = root / "xdg-config"
        config_dirs = root / "xdg-config-dirs"
        host_home.mkdir()
        config_home.mkdir()
        config_dirs.mkdir()
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": str(path_dir),
                "HOME": str(host_home),
                "XDG_CONFIG_HOME": str(config_home),
                "XDG_CONFIG_DIRS": str(config_dirs),
            }
        )

        test_open_files_only(binary, environment, root / "open-files")
        test_combined_limits(binary, environment, root / "combined", off_forwarding_tools)
        if not off_forwarding_tools:
            print("SKIP off-mode published-port task-slack check: install pasta, nft, and systemctl")
        test_missing_systemd_fails_closed(binary, environment, bwrap_only, root / "missing-systemd")
        print("resource-limit acceptance completed")


if __name__ == "__main__":
    main()
