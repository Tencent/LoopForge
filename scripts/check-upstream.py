#!/usr/bin/env python3
"""Verify the recorded source export and explicit public overrides. Read only."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--source", type=Path, help="optional private source checkout")
p.add_argument("--ref", default="origin/master", help="source ref to compare; does not fetch")
args = p.parse_args()
upstream = json.loads((ROOT / "upstream.json").read_text())
overrides = json.loads((ROOT / "upstream-overrides.json").read_text())


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None


errors = []
if overrides.get("baseline") != upstream["commit"]:
    errors.append("override baseline does not match the recorded source commit")
unchanged = adapted = removed = 0
for name, source_hash in upstream["files"].items():
    override = overrides["files"].get(name)
    expected = override["sha256"] if override else source_hash
    if digest(ROOT / name) != expected:
        errors.append(name + ": differs from recorded source/adaptation")
    if override:
        if not override.get("reason"):
            errors.append(name + ": public adaptation has no reason")
        if expected is None:
            removed += 1
        else:
            adapted += 1
    else:
        unchanged += 1
for name, record in overrides.get("added", {}).items():
    if digest(ROOT / name) != record["sha256"]:
        errors.append(name + ": public addition changed without audit update")
candidate_names = subprocess.check_output(["git","ls-files","--cached","--others","--exclude-standard","-z"],cwd=ROOT).decode().split("\0")
for name in set(candidate_names):
    if name.startswith(("cmd/","internal/","web/","deploy/","e2e/","migrations/")) and (ROOT/name).is_file():
        if name not in upstream["files"] and name not in overrides.get("added",{}):
            errors.append(name+": unrecorded public addition")
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print(f"Source {upstream['commit']}: {unchanged} unchanged, {adapted} adapted, {removed} excluded; {len(overrides.get('added', {}))} public additions.")
if args.source:
    # Independently check the source hashes, rather than trusting the manifest
    # merely because its commit label matches master. Never export blob data.
    source_errors = []
    with subprocess.Popen(["git", "-C", str(args.source), "cat-file", "--batch"], stdin=subprocess.PIPE, stdout=subprocess.PIPE) as blobs:
        for name, expected in upstream["files"].items():
            blobs.stdin.write((upstream["commit"] + ":" + name + "\n").encode())
            blobs.stdin.flush()
            header = blobs.stdout.readline().decode().strip().split()
            if len(header) != 3 or header[1] != "blob":
                source_errors.append(name + ": not a source blob at the recorded commit")
                continue
            raw = blobs.stdout.read(int(header[2]))
            blobs.stdout.read(1)
            if hashlib.sha256(raw).hexdigest() != expected:
                source_errors.append(name + ": source hash differs from the recorded commit")
        blobs.stdin.close()
        blobs.wait()
    if source_errors:
        print("\n".join(source_errors), file=sys.stderr)
        sys.exit(1)
    print(f"Verified {len(upstream['files'])} source blob hashes against the recorded commit.")
    latest = subprocess.check_output(["git", "-C", str(args.source), "rev-parse", args.ref], text=True).strip()
    if latest != upstream["commit"]:
        count = subprocess.check_output(["git", "-C", str(args.source), "rev-list", "--count", upstream["commit"] + ".." + latest], text=True).strip()
        print(f"Source ref has changed: {latest} ({count} commits after the recorded baseline). Prepare a reviewed three-way sync.")
        sys.exit(2)
    print("Source ref matches the recorded master baseline.")
