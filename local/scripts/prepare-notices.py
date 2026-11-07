#!/usr/bin/env python3
"""Prepare local-package notices from the repository's canonical notices."""
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def prepare():
    destination = ROOT / 'THIRD_PARTY_NOTICES.md'
    canonical = ROOT.parent / 'THIRD_PARTY_NOTICES.md'
    if not canonical.is_file():
        # A source distribution already includes its complete license notice.
        if not destination.is_file():
            raise SystemExit('Missing third-party license notices')
        return destination
    text = canonical.read_text(encoding='utf-8')
    section = text.split('## Superpowers\n', 1)[1].split('\n## Platform editor distributions', 1)[0]
    content = '# Third-Party Notices\n\n<!-- Generated from the repository root THIRD_PARTY_NOTICES.md. -->\n\n## Superpowers\n' + section.replace('`local/', '`').rstrip() + '\n'
    destination.write_text(content, encoding='utf-8')
    return destination


if __name__ == '__main__':
    prepare()
