#!/usr/bin/env python3
import argparse
import errno
import os
from pathlib import Path
import selectors
import signal
import socket
import stat


parser = argparse.ArgumentParser()
parser.add_argument("--ready-file", required=True)
parser.add_argument("--x11-dir", required=True)
parser.add_argument("--socket", action="append", default=[])
args = parser.parse_args()

running = True
listeners = []
created = []
x11_directory = Path(args.x11_dir)
x11_directory_created = False
x11_directory_identity = None


def stop(_signum, _frame):
    global running
    running = False


def remember_socket(path):
    info = path.lstat()
    created.append((path, info.st_dev, info.st_ino))


def cleanup():
    for listener in listeners:
        try:
            listener.close()
        except OSError:
            pass
    for path, device, inode in reversed(created):
        try:
            info = path.lstat()
            if stat.S_ISSOCK(info.st_mode) and info.st_dev == device and info.st_ino == inode:
                path.unlink()
        except FileNotFoundError:
            pass
    if x11_directory_created and x11_directory_identity is not None:
        try:
            info = x11_directory.lstat()
            if stat.S_ISDIR(info.st_mode) and (info.st_dev, info.st_ino) == x11_directory_identity:
                x11_directory.rmdir()
        except OSError:
            pass


signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
selector = selectors.DefaultSelector()
try:
    for value in args.socket:
        path = Path(value)
        path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        listener.bind(str(path))
        path.chmod(0o600)
        listener.listen(16)
        listener.setblocking(False)
        listeners.append(listener)
        remember_socket(path)
        selector.register(listener, selectors.EVENT_READ)

    if not x11_directory.exists():
        x11_directory.mkdir(mode=0o1777)
        x11_directory.chmod(0o1777)
        x11_directory_created = True
        info = x11_directory.lstat()
        x11_directory_identity = (info.st_dev, info.st_ino)
    if not x11_directory.is_dir():
        raise RuntimeError(f"X11 socket path parent is not a directory: {x11_directory}")

    x11_listener = None
    display_number = None
    for number in range(700, 10000):
        path = x11_directory / f"X{number}"
        if path.exists():
            continue
        candidate = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            candidate.bind(str(path))
        except OSError as error:
            candidate.close()
            if error.errno in {errno.EADDRINUSE, errno.EADDRNOTAVAIL}:
                continue
            raise
        remember_socket(path)
        path.chmod(0o777)
        candidate.listen(16)
        candidate.setblocking(False)
        x11_listener = candidate
        display_number = number
        listeners.append(candidate)
        selector.register(candidate, selectors.EVENT_READ)
        break
    if x11_listener is None:
        raise RuntimeError("no unused local X11 display number was available")

    Path(args.ready_file).write_text(str(display_number) + "\n", encoding="utf-8")
    while running:
        for key, _ in selector.select(timeout=0.1):
            connection, _ = key.fileobj.accept()
            with connection:
                connection.sendall(b"OK")
finally:
    selector.close()
    cleanup()
