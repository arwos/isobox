#!/usr/bin/env python3
"""Live acceptance for off mode with explicit pasta TCP/UDP host forwards."""

import os
import pathlib
import select
import signal
import shutil
import socket
import subprocess
import sys
import tempfile
import textwrap
import time


def reserve_adjacent_ports():
    for _ in range(64):
        first = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        second = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            first.bind(("127.0.0.1", 0))
            port = first.getsockname()[1]
            if port >= 65535:
                first.close()
                second.close()
                continue
            second.bind(("127.0.0.1", port + 1))
            return port, (first, second)
        except OSError:
            first.close()
            second.close()
    raise RuntimeError("could not reserve adjacent host ports")


def write_profile(root, home, port):
    profile = root / "profile.unic"
    profile.write_text(
        "\n".join(
            [
                "version 1;",
                f"home_path '{home}';",
                "network {",
                "  mode off;",
                "  port {",
                "    host_address '127.0.0.1';",
                f"    port {port};",
                "    protocol both;",
                "  }",
                "}",
                "",
            ]
        ),
        encoding="utf-8",
    )
    return profile


def host_gateway_probe_app():
    return textwrap.dedent(
        r"""
        import socket, struct, sys
        published, unannounced, host_probe_port = map(int, sys.argv[1:4])

        gateway = None
        with open('/proc/net/route', encoding='ascii') as routes:
            next(routes)
            for line in routes:
                fields = line.split()
                if len(fields) > 2 and fields[1] == '00000000':
                    gateway = socket.inet_ntoa(bytes.fromhex(fields[2])[::-1])
                    break
        if gateway is None:
            raise SystemExit('no IPv4 default gateway available for host-isolation probe')

        def must_fail_tcp(address, label):
            client = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            client.settimeout(1)
            try:
                client.connect(address)
            except OSError:
                return
            finally:
                client.close()
            raise SystemExit('off mode allowed TCP to ' + label)

        must_fail_tcp((gateway, host_probe_port), 'a host listener')
        must_fail_tcp(('1.1.1.1', 443), 'the Internet')

        query = struct.pack('!HHHHHH', 0x4958, 0x0100, 1, 0, 0, 0)
        query += b'\x07example\x03com\x00' + struct.pack('!HH', 1, 1)
        udp_probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp_probe.settimeout(1)
        try:
            udp_probe.sendto(query, ('1.1.1.1', 53))
            try:
                udp_probe.recvfrom(2048)
            except OSError:
                pass
            else:
                raise SystemExit('off mode received an external UDP/DNS response')
        finally:
            udp_probe.close()

        tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        tcp.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        tcp.bind(('0.0.0.0', published))
        tcp.listen(1)
        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp.bind(('0.0.0.0', published))
        hidden = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        hidden.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        hidden.bind(('0.0.0.0', unannounced))
        hidden.listen(1)
        print('READY', flush=True)

        connection, _ = tcp.accept()
        if connection.recv(64) != b'tcp-request':
            raise SystemExit('published TCP port received an invalid request')
        connection.sendall(b'tcp-response')
        connection.close()
        message, peer = udp.recvfrom(64)
        if message != b'udp-request':
            raise SystemExit('published UDP port received an invalid request')
        udp.sendto(b'udp-response', peer)
        print('DONE', flush=True)
        """
    )


def kill_test_process_group(process):
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    time.sleep(0.2)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        pass


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: pasta_off_acceptance.py /path/to/isobox")
    binary = pathlib.Path(sys.argv[1]).resolve()
    if not binary.is_file():
        raise SystemExit(f"isobox binary does not exist: {binary}")

    root = pathlib.Path(tempfile.mkdtemp(prefix="isobox-pasta-off-"))
    path_dir = root / "bin"
    path_dir.mkdir()
    for name in ("bwrap", "pasta", "nft"):
        executable = shutil.which(name)
        if executable is None:
            raise SystemExit(f"required host executable is missing: {name}")
        (path_dir / name).symlink_to(executable)

    host_home = root / "host-home"
    config_home = root / "xdg-config"
    config_dirs = root / "xdg-config-dirs"
    sandbox_home = root / "sandbox-home"
    for directory in (host_home, config_home, config_dirs, sandbox_home):
        directory.mkdir(mode=0o700)

    published_port, reservations = reserve_adjacent_ports()
    for reservation in reservations:
        reservation.close()
    profile = write_profile(root, sandbox_home, published_port)

    host_probe = None
    for _ in range(64):
        candidate = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        candidate.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        candidate.bind(("0.0.0.0", 0))
        if candidate.getsockname()[1] not in (published_port, published_port + 1):
            host_probe = candidate
            break
        candidate.close()
    if host_probe is None:
        raise RuntimeError("could not reserve a host probe port outside the published range")
    host_probe.listen(1)
    host_probe.settimeout(2)
    host_probe_port = host_probe.getsockname()[1]

    environment = os.environ.copy()
    environment.update(
        {
            "PATH": str(path_dir),
            "HOME": str(host_home),
            "XDG_CONFIG_HOME": str(config_home),
            "XDG_CONFIG_DIRS": str(config_dirs),
        }
    )
    app = host_gateway_probe_app()
    command = [
        str(binary),
        "run",
        f"--profile={profile}",
        "--app-id=f05-off-acceptance",
        "--",
        "/usr/bin/python3",
        "-c",
        app,
        str(published_port),
        str(published_port + 1),
        str(host_probe_port),
    ]
    process = subprocess.Popen(
        command,
        env=environment,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    try:
        stderr_parts = []
        marker = ""
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline and process.poll() is None and marker != "READY":
            ready, _, _ = select.select([process.stdout, process.stderr], [], [], 0.25)
            if process.stdout in ready:
                marker = process.stdout.readline().strip()
            if process.stderr in ready:
                chunk = os.read(process.stderr.fileno(), 8192)
                if chunk:
                    stderr_parts.append(chunk.decode("utf-8", errors="replace"))
        if marker != "READY":
            kill_test_process_group(process)
            stdout, stderr = process.communicate()
            stderr = "".join(stderr_parts) + stderr
            raise RuntimeError(
                "sandbox did not become ready; "
                f"exit={process.returncode}\nstdout:\n{stdout}\nstderr:\n{stderr}"
            )

        try:
            unexpected, _ = host_probe.accept()
        except socket.timeout:
            pass
        else:
            unexpected.close()
            raise RuntimeError("off mode allowed the application to connect to a host listener")

        with socket.create_connection(("127.0.0.1", published_port), timeout=5) as client:
            client.sendall(b"tcp-request")
            if client.recv(64) != b"tcp-response":
                raise RuntimeError("published TCP port did not return the application response")

        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
            client.settimeout(5)
            client.sendto(b"udp-request", ("127.0.0.1", published_port))
            response, _ = client.recvfrom(64)
            if response != b"udp-response":
                raise RuntimeError("published UDP port did not return the application response")

        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as client:
            client.settimeout(1)
            if client.connect_ex(("127.0.0.1", published_port + 1)) == 0:
                raise RuntimeError("host reached an unannounced guest TCP port")

        stdout, stderr = process.communicate(timeout=10)
        if process.returncode != 0 or "DONE" not in stdout:
            raise RuntimeError(
                f"sandbox exited {process.returncode}\nstdout:\n{stdout}\nstderr:\n{stderr}"
            )
        print("PASS off mode blocked host/Internet TCP and external UDP/DNS")
        print("PASS host TCP/UDP requests reached the announced port and received replies")
        print("PASS host could not reach the unannounced guest TCP port")
    finally:
        host_probe.close()
        if process.poll() is None:
            kill_test_process_group(process)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error)) from error
