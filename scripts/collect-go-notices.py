#!/usr/bin/env python3
"""Collect original Go module licenses/notices for binary distributions."""
import argparse
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--write', action='store_true', help='refresh the reviewed notice bundle')
args = parser.parse_args()
raw = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], cwd=ROOT, text=True)
modules = []
decoder = json.JSONDecoder()
while raw.strip():
    item, end = decoder.raw_decode(raw.lstrip())
    modules.append(item)
    raw = raw.lstrip()[end:]
parts = ['LoopForge Go dependency license bundle\nGenerated from go.mod; retain with binary distributions.\n']
for module in sorted(modules, key=lambda m: m['Path']):
    if module.get('Main'):
        continue
    if 'Dir' not in module:
        module.update(json.loads(subprocess.check_output(['go', 'mod', 'download', '-json', module['Path']+'@'+module['Version']], cwd=ROOT)))
    source = Path(module['Dir'])
    notices = sorted(p for p in source.iterdir() if p.is_file() and p.name.upper().startswith(('LICENSE', 'LICENCE', 'COPYING', 'NOTICE', 'AUTHORS', 'PATENTS')))
    if not notices:
        sys.exit('Review required: no root license/notice in '+module['Path'])
    parts.append('\n'+'='*78+'\n'+module['Path']+' '+module['Version']+'\n')
    for notice in notices:
        parts.append('\n--- '+notice.name+' ---\n'+notice.read_text(errors='strict').rstrip()+'\n')
result = ''.join(parts)
target = ROOT/'third_party/go/THIRD_PARTY_NOTICES.txt'
if args.write:
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(result)
elif not target.exists() or target.read_text() != result:
    sys.exit('Go dependency notices differ; review and run scripts/collect-go-notices.py --write')
print('Go license/notice bundle verified for', len(modules)-1, 'modules.')
