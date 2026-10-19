#!/usr/bin/env python3
"""Check the files intended for publication, without inspecting local secrets."""
from pathlib import Path
import hashlib
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
    "private key": re.compile(r"-----BEGIN (?:RSA |EC |DSA |OPENSSH )?PRIVATE KEY-----"),
    "GitHub credential": re.compile(r"(?:ghp_|gho_|ghs_|ghu_)[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}"),
    "cloud/model credential": re.compile(r"\bAKID[0-9A-Za-z]{32}\b|\bAKIA[0-9A-Z]{16}\b|\bsk-(?:ant-)?[A-Za-z0-9_-]{32,}"),
    "personal access token": re.compile(r"\btai_pat_[A-Za-z0-9]{16,}"),
    "WeCom webhook key": re.compile(r"qyapi\.weixin\.qq\.com/cgi-bin/webhook/send\?key=[0-9a-f-]{20,}"),
    "corporate email": re.compile(r"\b[a-z0-9._-]+@tencent\.com\b", re.I),
    "corporate Git identity default": re.compile(r'login\s*\+\s*"@tencent\.com"'),
    "private network address": re.compile(r"(?<![\d.])(?:9|11|21|30)\.\d{1,3}\.\d{1,3}\.\d{1,3}(?![\d.])"),
}
# Known internal identifiers (employee logins, private project/pipeline IDs),
# stored as SHA-256 digests so the checker does not republish them.
blocked_word_digests = {
    "e67f9fd00921452fe9b75ce95025638424a91b7a2b0a6b8b95781565e65b477c",
    "0090bdc2248bf391d9c5233f4049a6ad29e338881e38578fbeb6c5383643bf0a",
    "4995e91f8e37b35151a791a4700ed6e677030224537ea34d7c6bd5145e22e48d",
    "d91d0f358b13e5af94a794c2da7bcb1e86119a901836f32c9b3bc498ee9b2230",
    "60a85e030cbfdf26d62e592fed9b424d127ec1e5f06413fe7c577ce20e887571",
    "39bc5d6cbaf0191504a022489a7182327f02e78d1704626d24b2be7ae8b2d189",
    "361c3bb7ef5caa3cf00bee22c7d232969677daa9a288cc9258ea0d6de8914673",
    "cbda26ecd6a0a9455c8ee73edbe819692c68cbc4dbb059e07c9a020030ceeb00",
}
word = re.compile(r"[A-Za-z0-9_]{5,24}")
errors = []
for name in sorted(set(paths)):
    p = ROOT / name
    if not name or not p.is_file() or p.is_symlink() or p.resolve() == Path(__file__).resolve():
        continue
    # Vendored editors and libraries carry their upstream source/third-party notices.
    if name.startswith(("web/monaco/", "web/vscode-web/", "web/vendor/")):
        continue
    try:
        content = p.read_text()
    except (UnicodeError, OSError):
        continue
    for label, pattern in patterns.items():
        if pattern.search(content):
            errors.append(f"{name}: {label}")
    if any(hashlib.sha256(w.lower().encode()).hexdigest() in blocked_word_digests for w in set(word.findall(content))):
        errors.append(f"{name}: internal identifier")
    if p.name.endswith((".pem", ".key")):
        errors.append(f"{name}: deployment key/certificate must not be published")
if errors:
    print("\n".join(errors), file=sys.stderr)
    sys.exit(1)
print("Public content check passed (local ignored files were not read).")
