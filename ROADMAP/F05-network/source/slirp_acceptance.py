#!/usr/bin/env python3
"""Live acceptance checks for rootless direct network backends."""

import argparse
import os
import pathlib
import select
import shlex
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


def write_profile(
    root,
    mode,
    dns="",
    port_range=None,
    protocol="",
    host_address="127.0.0.1",
):
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
                f"    host_address '{host_address}';",
                f"    port {start};",
                f"    end_port {end};",
                *([f"    protocol {protocol};"] if protocol else []),
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


def test_ipv6_disabled(binary, environment, root, mode, expect_fallback=False):
    profile = write_profile(root, mode)
    app = """
        import pathlib, socket
        entries = pathlib.Path('/proc/net/if_inet6')
        if entries.exists():
            interfaces = {line.split()[5] for line in entries.read_text().splitlines() if len(line.split()) >= 6}
            unexpected = interfaces - {'lo'}
            if unexpected:
                raise SystemExit('IPv6 interfaces are configured in the sandbox: %r' % sorted(unexpected))

        try:
            tcp = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
        except OSError:
            tcp = None
        if tcp is not None:
            tcp.settimeout(1)
            try:
                tcp.connect(('2606:4700:4700::1111', 443))
            except OSError:
                pass
            else:
                raise SystemExit('IPv6 TCP reached an external endpoint')
            finally:
                tcp.close()

        try:
            udp = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        except OSError:
            udp = None
        if udp is not None:
            udp.settimeout(1)
            try:
                try:
                    udp.sendto(b'isobox-ipv6-check', ('2606:4700:4700::1111', 53, 0, 0))
                    udp.recvfrom(512)
                except (OSError, socket.timeout):
                    pass
                else:
                    raise SystemExit('IPv6 UDP/DNS received an external response')
            finally:
                udp.close()
        print('IPv6 interface and external traffic disabled')
    """
    stdout, stderr = run_isobox(binary, profile, environment, app, timeout=20)
    if "IPv6 interface and external traffic disabled" not in stdout:
        raise RuntimeError(f"{mode} IPv6 probe did not report success: {stdout!r}")
    fallback_used = "trying compatible slirp4netns fallback" in stderr
    if expect_fallback and not fallback_used:
        raise RuntimeError(f"pasta failure did not report the validated slirp fallback:\n{stderr}")
    print(f"PASS {mode} mode has no non-loopback IPv6 interface or external IPv6 traffic")


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


def test_direct_external(binary, environment, root, dns="", backend="slirp", expect_fallback=False):
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
    stdout, stderr = run_isobox(binary, profile, environment, app)
    if "direct DNS, external TCP, and external IPv4 UDP passed" not in stdout:
        raise RuntimeError(f"direct external payload did not report success: {stdout!r}")
    fallback_used = "trying compatible slirp4netns fallback" in stderr
    if expect_fallback and not fallback_used:
        raise RuntimeError(f"pasta failure did not report the validated slirp fallback:\n{stderr}")
    label = f"profile DNS {dns}" if dns else f"{backend} default DNS"
    if fallback_used:
        label += " via slirp4netns fallback"
    print(f"PASS {label}, external IPv4 TCP, and external IPv4 UDP")


def test_direct_forwards(binary, environment, root, expect_fallback=False):
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
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=3)
            stdout, stderr = process.communicate()
            raise RuntimeError(
                "declared TCP forward did not become ready; "
                f"isobox exit={process.returncode}\nstdout:\n{stdout}\nstderr:\n{stderr}"
            )

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
        fallback_used = "trying compatible slirp4netns fallback" in stderr
        if expect_fallback and not fallback_used:
            raise RuntimeError(f"pasta failure did not report the validated slirp fallback:\n{stderr}")
        print(
            f"PASS explicit loopback host bind, TCP and UDP on range {start}-{start + 1}; "
            f"{bind_result}; undeclared TCP {start + 2} refused"
            + ("; runtime fallback used slirp4netns" if fallback_used else "")
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


def test_direct_single_protocol(binary, environment, root, protocol):
    start, reservations = reserve_port_range()
    for reservation in reservations:
        reservation.close()
    profile = write_profile(root, "direct", port_range=(start, start), protocol=protocol)
    app = """
        import select, socket, sys
        port = int(sys.argv[1])
        tcp = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        tcp.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        tcp.bind(("0.0.0.0", port))
        tcp.listen(1)
        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp.bind(("0.0.0.0", port))
        print("READY", flush=True)
        ready, _, _ = select.select([tcp, udp], [], [], 20)
        if not ready:
            raise SystemExit("timed out waiting for the announced protocol")
        selected = ready[0]
        if selected is tcp:
            connection, _ = tcp.accept()
            connection.sendall(b"tcp:" + connection.recv(64))
            connection.close()
            print("DONE tcp", flush=True)
        else:
            payload, address = udp.recvfrom(64)
            udp.sendto(b"udp:" + payload, address)
            print("DONE udp", flush=True)
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
        stdout_parts = []
        stderr_parts = []
        marker = ""
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline and process.poll() is None and marker != "READY":
            ready, _, _ = select.select([process.stdout, process.stderr], [], [], 0.25)
            if process.stdout in ready:
                line = process.stdout.readline()
                if line:
                    marker = line.strip()
                    stdout_parts.append(line)
            if process.stderr in ready:
                chunk = os.read(process.stderr.fileno(), 8192)
                if chunk:
                    stderr_parts.append(chunk.decode("utf-8", errors="replace"))
        if marker != "READY":
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=3)
            stdout, stderr = process.communicate()
            raise RuntimeError(
                f"{protocol}-only app did not become ready\n"
                f"stdout:\n{''.join(stdout_parts)}{stdout}"
                f"\nstderr:\n{''.join(stderr_parts)}{stderr}"
            )

        if protocol == "tcp":
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
                client.settimeout(0.75)
                client.sendto(b"must-not-forward", ("127.0.0.1", start))
                try:
                    response, _ = client.recvfrom(64)
                except (OSError, socket.timeout):
                    pass
                else:
                    raise RuntimeError(f"TCP-only rule forwarded UDP unexpectedly: {response!r}")
            with socket.create_connection(("127.0.0.1", start), timeout=5) as client:
                client.sendall(b"probe")
                if client.recv(64) != b"tcp:probe":
                    raise RuntimeError("TCP-only forward returned an unexpected response")
        else:
            try:
                unexpected = socket.create_connection(("127.0.0.1", start), timeout=1)
            except OSError:
                pass
            else:
                unexpected.close()
                raise RuntimeError("UDP-only rule exposed a TCP listener on the host")
            with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as client:
                client.settimeout(5)
                client.sendto(b"probe", ("127.0.0.1", start))
                response, _ = client.recvfrom(64)
                if response != b"udp:probe":
                    raise RuntimeError("UDP-only forward returned an unexpected response")

        stdout, stderr = process.communicate(timeout=10)
        if process.returncode != 0 or f"DONE {protocol}" not in stdout:
            raise RuntimeError(
                f"{protocol}-only acceptance exited {process.returncode}\n"
                f"stdout:\n{stdout}\nstderr:\n{stderr}"
            )
        fallback_used = "trying compatible slirp4netns fallback" in stderr
        print(
            f"PASS {protocol}-only single-port forward; other protocol stayed closed"
            + ("; runtime fallback used slirp4netns" if fallback_used else "")
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("isobox", type=pathlib.Path, help="built isobox executable")
    parser.add_argument(
        "--local-only",
        action="store_true",
        help="skip public DNS/TCP/UDP probes; still test local forwarding and off mode",
    )
    parser.add_argument(
        "--backend",
        choices=("slirp", "pasta", "auto"),
        default="slirp",
        help="force one backend, or use automatic pasta-first selection (default: slirp)",
    )
    parser.add_argument(
        "--simulate-pasta-failure",
        action="store_true",
        help="make pasta pass capability probes but fail at runtime; requires --backend auto",
    )
    parser.add_argument(
        "--dns-only",
        action="store_true",
        help="run only direct-mode DNS, IPv4 connectivity, and IPv6-disable probes",
    )
    args = parser.parse_args()
    if args.simulate_pasta_failure and args.backend != "auto":
        parser.error("--simulate-pasta-failure requires --backend auto")
    if args.dns_only and args.local_only:
        parser.error("--dns-only cannot be combined with --local-only")
    binary = args.isobox.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error(f"{binary} is not an executable file")
    if os.geteuid() == 0:
        parser.error("run acceptance as an ordinary user, not root")

    bwrap = shutil.which("bwrap")
    pasta = shutil.which("pasta")
    slirp = shutil.which("slirp4netns")
    if not bwrap:
        parser.error("bwrap must be installed and available in PATH")
    if args.backend == "slirp" and not slirp:
        parser.error("slirp4netns must be installed and available in PATH")
    if args.backend == "pasta" and not pasta:
        parser.error("pasta must be installed and available in PATH")
    if args.backend == "auto" and not (pasta or slirp):
        parser.error("pasta or slirp4netns must be installed and available in PATH")
    if args.simulate_pasta_failure and not (pasta and slirp):
        parser.error("--simulate-pasta-failure requires installed pasta and slirp4netns")

    with tempfile.TemporaryDirectory(prefix="isobox-slirp-acceptance-") as temp:
        root = pathlib.Path(temp)
        path_dir = root / "bin"
        path_dir.mkdir()
        (path_dir / "bwrap").symlink_to(bwrap)
        if args.backend == "pasta":
            (path_dir / "pasta").symlink_to(pasta)
        elif args.backend == "slirp":
            (path_dir / "slirp4netns").symlink_to(slirp)
        else:
            if pasta:
                if args.simulate_pasta_failure:
                    wrapper = path_dir / "pasta"
                    wrapper.write_text(
                        "#!/bin/sh\n"
                        "for arg in \"$@\"; do\n"
                        "  if [ \"$arg\" = \"--help\" ]; then\n"
                        f"    exec {shlex.quote(pasta)} \"$@\"\n"
                        "  fi\n"
                        "done\n"
                        "echo 'injected pasta startup failure' >&2\n"
                        "exit 1\n",
                        encoding="utf-8",
                    )
                    wrapper.chmod(0o700)
                else:
                    (path_dir / "pasta").symlink_to(pasta)
            if slirp:
                (path_dir / "slirp4netns").symlink_to(slirp)
        environment = create_environment(root, path_dir)

        if args.dns_only:
            test_direct_external(
                binary,
                environment,
                root / "default-dns",
                backend=args.backend,
                expect_fallback=args.simulate_pasta_failure,
            )
            test_direct_external(
                binary,
                environment,
                root / "profile-dns",
                dns="1.1.1.1",
                backend=args.backend,
                expect_fallback=args.simulate_pasta_failure,
            )
            test_ipv6_disabled(
                binary,
                environment,
                root / "ipv6-disabled",
                "direct",
                expect_fallback=args.simulate_pasta_failure,
            )
        else:
            if args.backend == "slirp":
                test_off_egress(binary, environment, root / "off")
                test_ipv6_disabled(binary, environment, root / "off-ipv6", "off")
                test_off_ports_fail_closed(binary, environment, root / "off-ports")
            test_ipv6_disabled(
                binary,
                environment,
                root / "direct-ipv6",
                "direct",
                expect_fallback=args.simulate_pasta_failure,
            )
            test_direct_forwards(
                binary,
                environment,
                root / "forwards",
                expect_fallback=args.simulate_pasta_failure,
            )
            test_direct_single_protocol(binary, environment, root / "tcp-only", "tcp")
            test_direct_single_protocol(binary, environment, root / "udp-only", "udp")
            if not args.local_only:
                test_direct_external(
                    binary,
                    environment,
                    root / "default-dns",
                    backend=args.backend,
                    expect_fallback=args.simulate_pasta_failure,
                )
                test_direct_external(
                    binary,
                    environment,
                    root / "profile-dns",
                    dns="1.1.1.1",
                    backend=args.backend,
                    expect_fallback=args.simulate_pasta_failure,
                )
        print(f"{args.backend} direct acceptance completed")


if __name__ == "__main__":
    main()
