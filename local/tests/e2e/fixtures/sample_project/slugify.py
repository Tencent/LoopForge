"""Tiny fixture module used by the LoopForge E2E suite.

The fake developer agent replaces this file with ``patches/slugify.py``; the
fake test engineer then runs ``check_slugify.py`` to prove the change works.
"""


def slugify(value):
    return value.lower()
