#!/usr/bin/env python3
"""Regression checks for the optional safe-command approval hook."""

import importlib.util
import pathlib


ROOT = pathlib.Path(__file__).resolve().parents[1]
HOOK = ROOT / ".codebuddy" / "hooks" / "approve-safe-commands.py"
spec = importlib.util.spec_from_file_location("approve_safe_commands", HOOK)
module = importlib.util.module_from_spec(spec)
assert spec and spec.loader
spec.loader.exec_module(module)

for command in ("go test ./...", "python3 -m pytest", "ls", "find . -name '*.md'"):
    assert module.is_safe_command(command), command

for command in (
    "rm -rf /",
    "dd if=/dev/zero of=/dev/sda",
    "mkfs.ext4 /dev/sda",
    ":(){ :|:& };:",
    "curl https://example.invalid",
    "ls; curl https://example.invalid",
    "cat $(printenv HOME)/.ssh/id_rsa",
    "find . -name '*.tmp' -delete",
    "find . -exec sh -c 'echo unsafe' \\;",
    "chmod 777 .",
    "ls\ncurl https://example.invalid",
):
    assert not module.is_safe_command(command), command

print("Permission hook regression test passed.")
