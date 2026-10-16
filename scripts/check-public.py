#!/usr/bin/env python3
"""Check the files intended for publication, without inspecting local secrets."""
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
paths = subprocess.check_output(
    ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=ROOT
).decode().split("\0")
patterns = {
    "CodeBuddy credential": re.compile(r"ck_[a-zA-Z0-9]{10,}\.[A-Za-z0-9_-]{20,}"),
    "private infrastructure": re.compile(r"\b[a-zA-Z0-9_.-]+(?:\\?\.)woa(?:\\?\.)com\b"),
    "private key": re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    "GitHub credential": re.compile(r"(?:ghp_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})"),
    "embedded employee access": re.compile(r"roberttwang|crispinhu|jjasonzhang", re.I),
    "corporate Git identity default": re.compile(r'login\s*\+\s*"@tencent\.com"'),
}
errors = []
for name in sorted(set(paths)):
    p = ROOT / name
    if not name or not p.is_file() or p.is_symlink() or p.resolve() == Path(__file__).resolve():
        continue
    # Vendored editors carry their upstream source/third-party notices.
    if name.startswith(("web/monaco/", "web/vscode-web/")):
        continue
    try:
        content = p.read_text()
    except (UnicodeError, OSError):
        continue
    for label, pattern in patterns.items():
        if pattern.search(content):
            errors.append(f"{name}: {label}")
    if p.name.endswith((".pem", ".key")):
        errors.append(f"{name}: deployment key/certificate must not be published")
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print("Public content check passed (local ignored files were not read).")
