#!/usr/bin/env python3
import errno
import json
import os
from pathlib import Path
import stat
import sys


host_secret = Path(sys.argv[1])
expected_args = json.loads(sys.argv[2])
actual_args = sys.argv[3:]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def require_readonly_mount(target):
    for line in Path("/proc/self/mountinfo").read_text(encoding="utf-8").splitlines():
        fields = line.split()
        if len(fields) > 5 and fields[4].replace("\\040", " ") == target:
            require("ro" in fields[5].split(","), f"{target} mount is not read-only")
            return
    raise RuntimeError(f"{target} is not an independent mount")


def require_tmpfs_mount(target):
    for line in Path("/proc/self/mountinfo").read_text(encoding="utf-8").splitlines():
        fields = line.split()
        if len(fields) > 5 and fields[4].replace("\\040", " ") == target:
            separator = fields.index("-")
            require(fields[separator + 1] == "tmpfs", f"{target} is not a tmpfs mount")
            return
    raise RuntimeError(f"{target} is not an independent mount")


require(os.getcwd() == "/home/isobox", "sandbox working directory is not /home/isobox")
require(os.environ.get("PWD") == "/home/isobox", "PWD is not /home/isobox")
require(os.environ.get("HOME") == "/home/isobox", "HOME is not /home/isobox")
require(os.environ.get("TMPDIR") == "/tmp", "TMPDIR is not /tmp")
require("ISOBOX_T01_HOST_SECRET" not in os.environ, "host environment was not cleared")
require(actual_args == expected_args, "application argv changed after --")
require(not host_secret.exists(), "an unmounted host path is visible")
require(not Path("/etc/shadow").exists(), "host shadow database is visible")
require(sorted(path.name for path in Path("/home").iterdir()) == ["isobox"], "host home entries are visible")
require(list(Path("/root").iterdir()) == [], "host root home entries are visible")
require(Path("/proc/self/stat").is_file(), "/proc is unavailable")
require(stat.S_ISCHR(os.stat("/dev/null").st_mode), "/dev/null is unavailable")
for target in ("/tmp", "/run", "/var/tmp"):
    require_tmpfs_mount(target)
require_readonly_mount("/usr")

for path in (Path("/tmp"), Path("/var/tmp")):
    info = path.stat()
    require(stat.S_ISDIR(info.st_mode), f"{path} is not a directory")
    require(stat.S_IMODE(info.st_mode) == 0o1777, f"{path} does not have mode 01777")
    probe = path / f"isobox-t01-{os.getpid()}"
    probe.write_text("temporary\n", encoding="utf-8")
    probe.unlink()

passwd = Path("/etc/passwd")
if passwd.exists():
    require_readonly_mount("/etc/passwd")
    try:
        descriptor = os.open(passwd, os.O_WRONLY | os.O_APPEND)
    except OSError as error:
        require(error.errno in {errno.EACCES, errno.EPERM, errno.EROFS}, "/etc/passwd failed for an unexpected reason")
    else:
        os.close(descriptor)
        raise RuntimeError("system file /etc/passwd is writable")

print("PASS bwrap root, runtime paths, clean environment, readonly system paths, and argv", flush=True)
