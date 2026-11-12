#!/usr/bin/env python3
"""Live acceptance checks for the rootless slirp4netns network backend."""

import argparse
import os
import pathlib
import shutil
import socket
import subprocess
import tempfile
import textwrap
import time


def reserve_port_range():
    for _ in range(64):
        first = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        second = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        third = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            first.bind(("127.0.0.1", 0))
            port = first.getsockname()[1]
            if port > 65532:
                first.close()
                second.close()
                third.close()
                continue
            second.bind(("127.0.0.1", port + 1))
            third.bind(("127.0.0.1", port + 2))
            return port, (first, second, third)
        except OSError:
            first.close()
            second.close()
            third.close()
    raise RuntimeError("could not reserve three consecutive loopback ports")


def create_environment(root, path_dir):
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
    return environment


def write_profile(root, mode, dns="", port_range=None):
    root.mkdir(parents=True, exist_ok=True)
    home = root / "sandbox-home"
    home.mkdir(mode=0o700)
    lines = ["version 1;", f"home_path '{home}';", "network {", f"  mode {mode};"]
    if dns:
        lines.append(f"  dns '{dns}';")
    if port_range is not None:
        start, end = port_range
        lines.extend(
            [
                "  port {",
                "    host_address '127.0.0.1';",
                f"    port {start};",
                f"    end_port {end};",
                "  }",
            ]
        )
    lines.append("}")
    profile = root / "profile.unic"
    profile.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return profile


def run_isobox(binary, profile, environment, app, timeout=30, app_args=()):
    command = [
        str(binary),
        "run",
        f"--profile={profile}",
        "--app-id=f05-acceptance",
        "--",
        "/usr/bin/python3",
        "-c",
        textwrap.dedent(app),
        *map(str, app_args),
    ]
    result = subprocess.run(
        command,
        env=environment,
        text=True,
        capture_output=True,
        timeout=timeout,
    )
    if result.returncode != 0:
        raise RuntimeError(
            f"isobox exited {result.returncode}\n"
            f"stdout:\n{result.stdout}\n"
            f"stderr:\n{result.stderr}"
        )
    return result.stdout, result.stderr


def test_off_egress(binary, environment, root):
    profile = write_profile(root, "off")
    host_listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    host_listener.bind(("127.0.0.1", 0))
    host_listener.listen(1)
    app = """
        import socket, sys
        host_port = int(sys.argv[1])
        def must_fail_tcp(address):
            client = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            client.settimeout(1)
            try:
                client.connect(address)
            except OSError:
                return
            finally:
                client.close()
            raise SystemExit("off mode allowed TCP connection to %r" % (address,))
        must_fail_tcp(("127.0.0.1", host_port))
        must_fail_tcp(("1.1.1.1", 443))
        client = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        client.settimeout(1)
        try:
            client.sendto(b"isobox-off-check", ("1.1.1.1", 53))
            try:
                client.recvfrom(512)
            except (OSError, socket.timeout):
                pass
            else:
                raise SystemExit("off mode received an external UDP response")
        except OSError:
            pass
        finally:
            client.close()
        try:
            socket.getaddrinfo("example.com", 443)
        except socket.gaierror:
            pass
        else:
            raise SystemExit("off mode unexpectedly resolved an external name")
        print("off mode blocked host loopback, external TCP/UDP, and DNS")
    """
    try:
        stdout, _ = run_isobox(
            binary, profile, environment, app, timeout=20, app_args=(host_listener.getsockname()[1],)
        )
        if "blocked host loopback, external TCP/UDP, and DNS" not in stdout:
            raise RuntimeError(f"off-mode payload did not report success: {stdout!r}")
        print("PASS off mode blocks host-loopback TCP, external TCP/UDP, and DNS")
    finally:
        host_listener.close()


def test_off_ports_fail_closed(binary, environment, root):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as reserve:
        reserve.bind(("127.0.0.1", 0))
        port = reserve.getsockname()[1]
    profile = write_profile(root, "off", port_range=(port, port))
    home = root / "sandbox-home"
    command = [
        str(binary),
        "run",
        f"--profile={profile}",
        "--app-id=f05-acceptance",
        "--",
        "/usr/bin/python3",
        "-c",
        "open('/home/isobox/should-not-run', 'w').write('started')",
    ]
    result = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=20)
    if result.returncode == 0 or (home / "should-not-run").exists():
        raise RuntimeError("off mode with ports did not stop before the application")
    if "requires pasta's explicit forwarding path" not in result.stderr:
        raise RuntimeError(
            f"off-mode preflight failed for an unexpected reason:\n{result.stderr}"
        )
    print("PASS off mode with ports fails closed when pasta is unavailable")


def test_direct_external(binary, environment, root, dns=""):
    profile = write_profile(root, "direct", dns=dns)
    app = """
        import socket, struct
        answers = socket.getaddrinfo("example.com", 443, socket.AF_INET, socket.SOCK_STREAM)
        if not answers:
            raise SystemExit("DNS returned no IPv4 addresses")
        connection = socket.create_connection(answers[0][4], timeout=10)
        connection.close()
        query = struct.pack("!HHHHHH", 0x4958, 0x0100, 1, 0, 0, 0)
        query += b"\\x07example\\x03com\\x00" + struct.pack("!HH", 1, 1)
        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp.settimeout(8)
        udp.sendto(query, ("1.1.1.1", 53))
        response, _ = udp.recvfrom(2048)
        udp.close()
        if len(response) < 12 or response[:2] != query[:2] or not (response[2] & 0x80) or response[3] & 0x0f:
            raise SystemExit("invalid or failed external UDP DNS response")
        print("direct DNS, external TCP, and external IPv4 UDP passed")
    """
    stdout, _ = run_isobox(binary, profile, environment, app)
    if "direct DNS, external TCP, and external IPv4 UDP passed" not in stdout:
        raise RuntimeError(f"direct external payload did not report success: {stdout!r}")
    label = f"profile DNS {dns}" if dns else "slirp default DNS"
    print(f"PASS {label}, external IPv4 TCP, and external IPv4 UDP")


def test_direct_forwards(binary, environment, root):
    start, reservations = reserve_port_range()
    for reservation in reservations:
        reservation.close()
    profile = write_profile(root, "direct", port_range=(start, start + 1))
    app = """
        import select, socket, sys, time
        base = int(sys.argv[1])
        listeners = {}
        for offset in (0, 1):
            for kind in (socket.SOCK_STREAM, socket.SOCK_DGRAM):
                item = socket.socket(socket.AF_INET, kind)
                item.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                item.bind(("0.0.0.0", base + offset))
                if kind == socket.SOCK_STREAM:
                    item.listen(2)
                listeners[item] = (kind, offset)
        unannounced = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        unannounced.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        unannounced.bind(("0.0.0.0", base + 2))
        unannounced.listen(2)
        listeners[unannounced] = (socket.SOCK_STREAM, 2)
        seen = set()
        shutdown = False
        deadline = time.monotonic() + 25
        while not shutdown and time.monotonic() < deadline:
            ready, _, _ = select.select(listeners.keys(), [], [], max(0, deadline - time.monotonic()))
            for item in ready:
                kind, offset = listeners[item]
                if kind == socket.SOCK_STREAM:
                    connection, _ = item.accept()
                    connection.sendall(("tcp%d:" % offset).encode() + connection.recv(64))
                    connection.close()
                    seen.add(("tcp", offset))
                else:
                    payload, address = item.recvfrom(64)
                    item.sendto(b"udp%d:" % offset + payload, address)
                    if payload == b"shutdown":
                        shutdown = True
                    else:
                        seen.add(("udp", offset))
        if not shutdown or seen != {("tcp", 0), ("udp", 0), ("tcp", 1), ("udp", 1)}:
            raise SystemExit("did not receive every declared TCP/UDP port")
    """
    command = [
        str(binary),
        "run",
        f"--profile={profile}",
        "--app-id=f05-acceptance",
        "--",
        "/usr/bin/python3",
        "-c",
        textwrap.dedent(app),
        str(start),
    ]
    process = subprocess.Popen(
        command,
        env=environment,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline and process.poll() is None:
            try:
                with socket.create_connection(("127.0.0.1", start), timeout=0.5) as connection:
                    connection.sendall(b"probe")
                    if connection.recv(64) != b"tcp0:probe":
                        raise RuntimeError("declared TCP forward returned an unexpected response")
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("declared TCP forward did not become ready")

        external_address = routed_host_ipv4()
        if external_address:
            try:
                socket.create_connection((external_address, start), timeout=1).close()
            except OSError:
                pass
            else:
                raise RuntimeError("host forward for 127.0.0.1 was also reachable on a non-loopback address")
            bind_result = "non-loopback bind refused"
        else:
            bind_result = "non-loopback bind skipped (no routed host IPv4 address)"

        for offset in (0, 1):
            with socket.create_connection(("127.0.0.1", start + offset), timeout=2) as connection:
                connection.sendall(b"probe")
                if connection.recv(64) != f"tcp{offset}:probe".encode():
                    raise RuntimeError(f"declared TCP forward on offset {offset} returned an unexpected response")
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as datagram:
                datagram.settimeout(5)
                datagram.sendto(b"probe", ("127.0.0.1", start + offset))
                response, _ = datagram.recvfrom(64)
                if response != f"udp{offset}:probe".encode():
                    raise RuntimeError(f"declared UDP forward on offset {offset} returned an unexpected response")

        try:
            socket.create_connection(("127.0.0.1", start + 2), timeout=1).close()
        except OSError:
            pass
        else:
            raise RuntimeError("host reached an application port absent from the profile")

        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as datagram:
            datagram.settimeout(5)
            datagram.sendto(b"shutdown", ("127.0.0.1", start + 1))
            response, _ = datagram.recvfrom(64)
            if response != b"udp1:shutdown":
                raise RuntimeError("forwarding fixture did not shut down cleanly")

        returncode = process.wait(timeout=10)
        stdout, stderr = process.communicate()
        if returncode != 0:
            raise RuntimeError(
                f"isobox exited {returncode}\nstdout:\n{stdout}\nstderr:\n{stderr}"
            )
        print(
            f"PASS explicit loopback host bind, TCP and UDP on range {start}-{start + 1}; "
            f"{bind_result}; undeclared TCP {start + 2} refused"
        )
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        process.communicate()


def routed_host_ipv4():
    probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        probe.connect(("1.1.1.1", 53))
        address = probe.getsockname()[0]
        return address if not address.startswith("127.") else ""
    except OSError:
        return ""
    finally:
        probe.close()


def has_host_ipv6_default_route():
    try:
        routes = pathlib.Path("/proc/net/ipv6_route").read_text(encoding="ascii").splitlines()
    except OSError:
        return False
    for line in routes:
        fields = line.split()
        if len(fields) < 10 or fields[0] != "0" * 32 or fields[1] != "00":
            continue
        if fields[9] == "lo" or int(fields[5], 16) == 0xFFFFFFFF:
            continue
        return True
    return False


def test_direct_ipv6(binary, environment, root):
    profile = write_profile(root, "direct")
    app = """
        import socket
        answers = socket.getaddrinfo("example.com", 443, socket.AF_INET6, socket.SOCK_STREAM)
        if not answers:
            raise SystemExit("IPv6 DNS returned no addresses")
        connection = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
        connection.settimeout(10)
        connection.connect(answers[0][4])
        connection.close()
        print("external IPv6 TCP connect passed")
    """
    stdout, _ = run_isobox(binary, profile, environment, app)
    if "external IPv6 TCP connect passed" not in stdout:
        raise RuntimeError(f"IPv6 payload did not report success: {stdout!r}")
    print("PASS external IPv6 DNS and TCP")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("isobox", type=pathlib.Path, help="built isobox executable")
    parser.add_argument(
        "--local-only",
        action="store_true",
        help="skip public DNS/TCP/UDP probes; still test local forwarding and off mode",
    )
    args = parser.parse_args()
    binary = args.isobox.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error(f"{binary} is not an executable file")
    if os.geteuid() == 0:
        parser.error("run acceptance as an ordinary user, not root")

    bwrap = shutil.which("bwrap")
    slirp = shutil.which("slirp4netns")
    if not bwrap or not slirp:
        parser.error("bwrap and slirp4netns must be installed and available in PATH")

    with tempfile.TemporaryDirectory(prefix="isobox-slirp-acceptance-") as temp:
        root = pathlib.Path(temp)
        path_dir = root / "bin"
        path_dir.mkdir()
        (path_dir / "bwrap").symlink_to(bwrap)
        (path_dir / "slirp4netns").symlink_to(slirp)
        environment = create_environment(root, path_dir)

        test_off_egress(binary, environment, root / "off")
        test_off_ports_fail_closed(binary, environment, root / "off-ports")
        test_direct_forwards(binary, environment, root / "forwards")
        if not args.local_only:
            test_direct_external(binary, environment, root / "default-dns")
            test_direct_external(binary, environment, root / "profile-dns", dns="1.1.1.1")
            if has_host_ipv6_default_route():
                test_direct_ipv6(binary, environment, root / "ipv6")
            else:
                print("SKIP external IPv6: host has no IPv6 default route")
        print("slirp direct acceptance completed")


if __name__ == "__main__":
    main()
