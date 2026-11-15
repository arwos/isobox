#!/usr/bin/env python3
import errno
import os
from pathlib import Path
import sys


phase = sys.argv[1]
host_secret = Path(sys.argv[2])
home = Path("/home/isobox")
read_only = Path("/mnt/readonly")
host_writable = Path("/mnt/writable")


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


require(os.environ.get("HOME") == str(home), "HOME is not /home/isobox")
require(sorted(path.name for path in Path("/home").iterdir()) == ["isobox"], "host /home entries are visible")
require(not host_secret.exists(), "an unmounted host path is visible inside the sandbox")
require((read_only / "visible.txt").read_text(encoding="utf-8") == "read-only mount\n", "read-only mount content differs")

try:
    (read_only / "write-probe.txt").write_text("must not be written\n", encoding="utf-8")
except OSError as error:
    if error.errno not in {errno.EACCES, errno.EPERM, errno.EROFS}:
        raise
else:
    raise RuntimeError("read-only mount accepted a write")

home_marker = home / "persisted.txt"
if phase in {"explicit-first", "managed-first"}:
    require(not home_marker.exists(), "new HOME already contains the persistence marker")
    home_marker.write_text(phase + "\n", encoding="utf-8")
elif phase in {"explicit-repeat", "managed-repeat"}:
    expected = "explicit-first\n" if phase == "explicit-repeat" else "managed-first\n"
    require(home_marker.read_text(encoding="utf-8") == expected, "HOME data did not persist at the expected location")
elif phase == "managed-isolation":
    require(not home_marker.exists(), "a different profile/app-id pair reused another HOME")
    home_marker.write_text("isolated\n", encoding="utf-8")
elif phase == "managed-shared":
    require(home_marker.read_text(encoding="utf-8") == "managed-first\n", "the same app-id did not reuse its HOME across profiles")
elif phase.startswith("explicit-"):
    raise RuntimeError("unknown explicit HOME phase: " + phase)
elif phase.startswith("managed-"):
    raise RuntimeError("unknown managed HOME phase: " + phase)
else:
    raise RuntimeError("unknown probe phase: " + phase)

if phase.startswith("explicit-"):
    writable_marker = host_writable / "persisted.txt"
    if phase == "explicit-first":
        require(not writable_marker.exists(), "writable mount already contains the marker")
        writable_marker.write_text("writable mount\n", encoding="utf-8")
    else:
        require(writable_marker.read_text(encoding="utf-8") == "writable mount\n", "writable mount did not use the configured host path")

print("PASS " + phase, flush=True)
