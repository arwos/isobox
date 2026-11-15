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
                f"  port 127.0.0.1 {port} both;",
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
        import pathlib, select, socket, struct, sys
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
            try:
                udp_probe.sendto(query, ('1.1.1.1', 53))
                udp_probe.recvfrom(2048)
            except OSError:
                pass
            else:
                raise SystemExit('off mode received an external UDP/DNS response')
        finally:
            udp_probe.close()

        ipv6_interfaces = set()
        ipv6_table = pathlib.Path('/proc/net/if_inet6')
        if ipv6_table.exists():
            with ipv6_table.open(encoding='ascii') as addresses:
                for line in addresses:
                    fields = line.split()
                    if len(fields) >= 6:
                        ipv6_interfaces.add(fields[5])
        if ipv6_interfaces:
            raise SystemExit('off mode has IPv6 addresses configured: %r' % sorted(ipv6_interfaces))
        status = {}
        with open('/proc/self/status', encoding='ascii') as fields:
            for line in fields:
                if ':' in line:
                    name, value = line.split(':', 1)
                    status[name] = value.strip()
        for name in ('CapEff', 'CapPrm', 'CapInh', 'CapAmb'):
            if int(status.get(name, '-1'), 16) != 0:
                raise SystemExit('%s is not empty after off-mode setup: %s' % (name, status.get(name)))
        if status.get('NoNewPrivs') != '1':
            raise SystemExit('no_new_privs is not set in off mode')
        try:
            ipv6_probe = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
        except OSError:
            ipv6_probe = None
        if ipv6_probe is not None:
            ipv6_probe.settimeout(1)
            try:
                ipv6_probe.connect(('2606:4700:4700::1111', 443))
            except OSError:
                pass
            else:
                raise SystemExit('off mode allowed an external IPv6 TCP connection')
            finally:
                ipv6_probe.close()
        try:
            ipv6_udp = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        except OSError:
            ipv6_udp = None
        if ipv6_udp is not None:
            ipv6_udp.settimeout(1)
            try:
                try:
                    ipv6_udp.sendto(b'isobox-off-ipv6', ('2606:4700:4700::1111', 53, 0, 0))
                    ipv6_udp.recvfrom(512)
                except OSError:
                    pass
                else:
                    raise SystemExit('off mode received an external IPv6 UDP response')
            finally:
                ipv6_udp.close()

        try:
            icmp_probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM, socket.IPPROTO_ICMP)
        except OSError as error:
            print('ICMP socket unavailable:', error, flush=True)
            print('ICMP_BLOCKED_SOCKET', flush=True)
        else:
            icmp_probe.settimeout(1)
            try:
                try:
                    packet = bytearray(b'\x08\x00\x00\x00\x49\x58\x00\x01')
                    checksum = (~sum((packet[index] << 8) + packet[index + 1] for index in range(0, len(packet), 2))) & 0xffff
                    packet[2:4] = struct.pack('!H', checksum)
                    icmp_probe.sendto(packet, ('1.1.1.1', 0))
                    icmp_probe.recvfrom(2048)
                except OSError:
                    print('ICMP_BLOCKED_EGRESS', flush=True)
                else:
                    raise SystemExit('off mode received an external ICMP response')
            finally:
                icmp_probe.close()

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
        hidden_udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        hidden_udp.bind(('0.0.0.0', unannounced))
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
        ready, _, _ = select.select([hidden_udp], [], [], 1.5)
        if ready:
            message, peer = hidden_udp.recvfrom(64)
            hidden_udp.sendto(b'hidden-udp-response', peer)
            raise SystemExit('off mode received a datagram for an unannounced port')
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
        stdout_parts = []
        marker = ""
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline and process.poll() is None and marker != "READY":
            ready, _, _ = select.select([process.stdout, process.stderr], [], [], 0.25)
            if process.stdout in ready:
                line = process.stdout.readline()
                stdout_parts.append(line)
                marker = line.strip()
            if process.stderr in ready:
                chunk = os.read(process.stderr.fileno(), 8192)
                if chunk:
                    stderr_parts.append(chunk.decode("utf-8", errors="replace"))
        if marker != "READY":
            kill_test_process_group(process)
            stdout, stderr = process.communicate()
            stdout = "".join(stdout_parts) + stdout
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

        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
            client.settimeout(2)
            client.sendto(b"hidden-udp-request", ("127.0.0.1", published_port + 1))
            try:
                client.recvfrom(64)
            except socket.timeout:
                pass
            else:
                raise RuntimeError("host received a response from an unannounced guest UDP port")

        stdout, stderr = process.communicate(timeout=10)
        stdout = "".join(stdout_parts) + stdout
        if process.returncode != 0 or "DONE" not in stdout:
            raise RuntimeError(
                f"sandbox exited {process.returncode}\nstdout:\n{stdout}\nstderr:\n{stderr}"
            )
        print("PASS off mode has no IPv6 addresses and dropped setup capabilities")
        if "ICMP_BLOCKED_SOCKET" in stdout:
            print("PASS sandbox process could not create an ICMP socket")
        elif "ICMP_BLOCKED_EGRESS" in stdout:
            print("PASS ICMP echo probe received no response")
        else:
            raise RuntimeError("off mode did not confirm the ICMP block")
        print("PASS off mode blocked host/Internet TCP and external UDP/DNS")
        print("PASS host TCP/UDP requests reached the announced port and received replies")
        print("PASS host could not reach the unannounced guest TCP/UDP port")
    finally:
        host_probe.close()
        if process.poll() is None:
            kill_test_process_group(process)


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        raise SystemExit(str(error)) from error
