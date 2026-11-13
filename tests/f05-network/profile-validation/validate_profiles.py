#!/usr/bin/env python3
"""Confirm invalid IPv6 UNIC profiles fail before their app command starts."""

import pathlib
import subprocess
import sys


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: validate_profiles.py /path/to/isobox /path/to/fixtures")
    binary = pathlib.Path(sys.argv[1]).resolve()
    fixture_dir = pathlib.Path(sys.argv[2]).resolve()
    failures = []
    for fixture in ("ipv6-dns.unic", "ipv6-host-bind.unic", "ipv4-mapped-ipv6-bind.unic"):
        profile = fixture_dir / fixture
        result = subprocess.run(
            [
                str(binary),
                "run",
                f"--profile={profile}",
                "--app-id=ipv6-profile-validation",
                "--",
                "/usr/bin/python3",
                "-c",
                "print('APP_STARTED')",
            ],
            text=True,
            capture_output=True,
            timeout=15,
        )
        diagnostic = result.stderr.strip()
        if result.returncode == 0 or "IPv6 is disabled" not in diagnostic or "APP_STARTED" in result.stdout:
            failures.append(
                f"FAIL {fixture}: exit={result.returncode}; stdout={result.stdout!r}; stderr={diagnostic!r}"
            )
        else:
            print(f"PASS {fixture}: rejected before app start")
            print(f"  {diagnostic}")
    if failures:
        raise SystemExit("\n".join(failures))


if __name__ == "__main__":
    main()
