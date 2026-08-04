# Changelog

English | [简体中文](CHANGELOG.zh-CN.md)

## 0.1.0

- Rewrote English and Chinese READMEs to highlight project value, quick start, workflow, and edition selection.
- Promoted the first public release to a stable version and unified npm, Python, CLI, and release validation versions.
- Classic small flow now only creates solo-developer, which directly completes the final summary.
- Test stage now selects unit, integration/API, or E2E tests based on the target project's existing test layers.
- Released CodeBuddy, Codex, and cross-host DevFlow Skills.
- Removed internal operational, deployment, and MCP configurations while keeping the original workflow permission semantics and documenting risks.
- Added security policy, contribution guide, third-party notices, and automated validation.
- Retained Superpowers native document paths and provided independent artefact override rules for DevFlow mode.
- Added Cursor and Claude Code repository-level projections, a shared executor source of truth, and CI drift detection.
- Supported traceable Skill copy installation and safe refresh, with Cursor/Claude installation smoke tests.
- Clarified the distinction between Classic and Portable editions and added complete Classic Cursor/Claude host packages.
- Added a unified `loopforge` CLI installable via pipx, supporting install, status, update, uninstall, and diagnostics for four hosts.
- Added an npm/npx entry that shares the installation core with the original Python CLI, supporting one-off execution and npm global install.
- Added a unified edition registry and read-only `loopforge editions` / `loopforge plan` to make the two sources of truth, entry points, and installation contents discoverable and verifiable.
- `loopforge install <host>` defaults to Classic; Portable uses the distinct `loopforge skills install <host>` command group.
- Added explicit `--force` to overwrite installation; by default it still stops on user file or symlink conflicts.
- `install --force` supports switching between Classic and Portable editions on the same host.
- CodeBuddy Portable stage executors and research assistants uniformly enable auto-run and `bypassPermissions`.
