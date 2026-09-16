# Repository Agent Instructions

## Source of truth

- Treat the repository as two editions: Classic (`.codebuddy/.codex/.cursor/.claude`) and Portable (`skills/`). Do not mix their runtime assets.
- Treat `.codebuddy` as the Classic reference behavior. `.cursor` and `.claude` are generated Classic host bundles; do not edit them directly.
- Treat `skills/devflow/` and `skills/devflow-clarify-requirements/` as the canonical Portable workflow.
- Treat `tools/tcmcp/` as an independent optional tool (Python 3.11+). Do not mix it into Classic or Portable edition runtime files.
- Keep logical roles in `skills/devflow/agents/manifest.json`, artifacts in `skills/devflow/templates/manifest.json`, rules in `skills/devflow/rules/manifest.json`, and host capabilities in each `skills/devflow/adapters/<host>/manifest.json`.
- After changing Classic behavior or host adaptation, run `python3 scripts/build-classic-hosts.py --write`.
- A Classic change is not automatically a Portable change. Share explicit behavior contracts and tests, not runtime files.

## Validation

Run these commands before reporting a workflow change complete:

```bash
python3 scripts/build-classic-hosts.py --check
bash scripts/validate.sh
bash scripts/smoke-install.sh
bash scripts/scan-secrets.sh
```

Preserve user changes, do not commit secrets or organization-specific infrastructure, and distinguish passed, failed, blocked, and not-run checks.
