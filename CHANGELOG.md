# Changelog

English | [简体中文](CHANGELOG.zh-CN.md)

## Unreleased

- Added `tools/tcmcp`, a Tencent Cloud read-only MCP Initializr that is documented and run separately from `loopforge install`. LoopForge version tags also publish `ghcr.io/tencent/loopforge-tcmcp`.

## 0.2.0

- Added an opt-in local observability dashboard for Classic DevFlow runs on CodeBuddy, Cursor, and Claude Code.
- Added local metrics for tool latency, transcript token usage, session cost, agent attribution, and DevFlow stage progress.
- Added per-model pricing overrides for models that are not covered by the built-in price table.
- Improved dashboard accuracy for turn and duration aggregation, dispatch attribution, session counting, and medium/large workflow classification.
- Fixed generated Classic bundle validation so Python cache files are not reported as unmanaged content.
- Made release workflows retryable from an existing version tag.

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
