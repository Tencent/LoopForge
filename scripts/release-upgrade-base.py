#!/usr/bin/env python3
"""Choose the preceding published stable platform release, never a Local tag."""
import json
import re
import sys


PLATFORM_START = (0, 3, 0)


def upgrade_base(target, releases):
    match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?", target)
    if not match:
        raise ValueError("Expected a platform vMAJOR.MINOR.PATCH[-PRERELEASE] tag")
    version = tuple(map(int, match.groups()))
    candidates = []
    for release in releases:
        stable = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", release["tagName"])
        if not stable or release.get("isDraft") or release.get("isPrerelease"):
            continue
        previous = tuple(map(int, stable.groups()))
        if PLATFORM_START <= previous < version:
            candidates.append((previous, release["tagName"]))
    if candidates:
        return max(candidates)[1]
    if version == PLATFORM_START:
        return ""  # The first platform release has no platform to upgrade from.
    raise ValueError("No earlier published stable platform release found; refusing to skip upgrade validation")


if __name__ == "__main__":
    try:
        print(upgrade_base(sys.argv[1], json.load(sys.stdin)))
    except (IndexError, KeyError, ValueError) as error:
        sys.exit(str(error))
