"""Tiny fixture module used by the LoopForge E2E suite (fixed version)."""

import re


def slugify(value):
    words = re.findall(r"[a-z0-9]+", value.lower())
    return "-".join(words)
