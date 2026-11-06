# Changelog

Platform releases use `vX.Y.Z` tags and publish `ghcr.io/tencent/loopforge`. LoopForge Local packages use `local-vX.Y.Z` tags and are published by manually running the `release-local` workflow.

## 0.3.2 — Claude Code installed on first use

- Published images no longer bundle Claude Code, which is proprietary. The first Main Bot conversation or SOP role that selects it installs the pinned `@anthropic-ai/claude-code@2.1.300` from npm into `$LOOPFORGE_DATA_DIR/cli`; Docker sandboxes from the platform image read it through a read-only mount. That first use waits for the download (about a minute); later runs reuse it. Codex and CodeBuddy remain bundled.
- Listen on `0.0.0.0` by default, including older installations without `LOOPFORGE_BIND`. The installer no longer asks for, or fails without, `LOOPFORGE_PUBLIC_URL`; it is optional and only changes the printed address.

## 0.3.1 — published-image startup

- Start from the published GHCR image pinned in `compose.yaml` instead of building locally: `./start.sh` (`logs`, `stop`, `down`, `restart`, `pull`) and a plain `docker compose --profile platform up` no longer build.
- Build a checkout explicitly with `./start.sh dev` (`compose.dev.yaml`, local `loopforge-platform:local` tag); `./start.sh release` switches back to the published image.
- Releases require `compose.yaml` to pin the tagged version and smoke-test the pushed image digest before creating the GitHub Release.

Existing source-built installations switch to the published image on the next `./start.sh`; no migrations are required. The image is published for linux/amd64; use `./start.sh dev` on other architectures.

## 0.3.0 — platform edition

- Add the self-hosted platform implementation, including Main Bot workspace editing, durable workspace checkpoints, idle reclamation, and task-group SOP completion/reopening.
- Add public password accounts, explicit catalog installation, GitHub issue/PR adapters, actor-scoped GitHub MCP credentials, language and theme preferences, local deployment and public acceptance checks.
- Connect GitHub by copying a PIN and approving the built-in LoopForge application, then pick repositories; PAT entry remains available.
- Choose the branch mode, base branch and per-repository branches when opening a conversation workspace; role dispatches report their collected deliverables.
- Install as an ordinary Linux user through sudo, install from a published platform image, and reuse that image for default sandboxes.
- Validate repository remote URLs and restrict SSO return URLs to the same site.
- Integrate the original workflows as LoopForge Local under `local/`, with unified documentation, project policies and release history.

Existing installations apply `migrations/022_auth_integrations.sql` and `migrations/023_role_prompt.sql`. Creating a named workspace branch on a connected runner requires runner 0.9.1 or later.

## 0.2.3 — Local packaging

- Document Node.js / Python requirements and the npm package's Local-only scope.
- Record the `local/` package directory and exclude host tests, caches, credentials and runtime logs from npm artifacts.
- Include the CLI entry point, README and notices preparation script in release archives.
- Quote hook paths for projects containing spaces; validate npm/npx installation in a space-containing project path.
- Harden release tag verification and npm registry error handling.

## Local workflow release history

The following releases predate the platform edition.

### 0.2.2

- Keep executable bits when installing Classic host files so `run_hook.sh` can run after `npx loopforge-cli install`, restoring Agent Observability collection.

### 0.2.1

- Added `tools/tcmcp`, a Tencent Cloud read-only MCP Initializr that is documented and run separately from `loopforge install`. LoopForge version tags also publish `ghcr.io/tencent/loopforge-tcmcp`.

### 0.2.0

- Added an opt-in local observability dashboard for Classic DevFlow runs on CodeBuddy, Cursor, and Claude Code.
- Added local metrics for tool latency, transcript token usage, session cost, agent attribution, and DevFlow stage progress.
- Added per-model pricing overrides for models that are not covered by the built-in price table.
- Improved dashboard accuracy for turn and duration aggregation, dispatch attribution, session counting, and medium/large workflow classification.
- Fixed generated Classic bundle validation so Python cache files are not reported as unmanaged content.
- Made release workflows retryable from an existing version tag.

### 0.1.0

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
