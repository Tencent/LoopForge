# Changelog

Platform releases use `vX.Y.Z` tags and publish `ghcr.io/tencent/loopforge`. LoopForge Local packages use `local-vX.Y.Z` tags and are published by manually running the `release-local` workflow.

## 0.3.7 — requirement clarification on the framework you choose

- Requirement clarification no longer runs only on CodeBuddy. The picker groups every framework the platform offers by engine — CodeBuddy, Codex, Claude Code and the agent CLIs — and the clarification really runs on the one you pick, using the matching credential from personal settings.
- Availability is judged from the issue's responsible user, because clarification runs on their credentials. A framework that cannot run is still listed, greyed out as a whole group, with the reason shown: no API key, no Base URL where one is required, the CLI is not installed on this host, or no model is available yet.
- Personal settings keep a default clarification framework at the top of the 模型与执行 group. Each clarification can switch for this round without writing that default back, and a default whose key was removed is labelled as no longer available instead of being used silently.
- An item keeps the framework its clarification started with, so later rounds resume the same session; switching on purpose starts a new session and replays the questions already answered instead of failing to resume.
- Nothing changes for deployments that select no framework: CodeBuddy stays the default, its model catalog stays validated as before, and AI notification summaries still use CodeBuddy.

Existing installations may apply `migrations/028_clarify_model_preference.sql`; the platform also adds these tables and columns on startup.

## 0.3.6 — a default sandbox on an uninitialized catalog

- A database without sandbox catalog rows, for example after a plain `docker compose --profile platform up` without the installer, now offers the built-in 默认运行沙箱 (`local`). Creating a blank SOP no longer fails with `sandbox profile not found`. The default is not written to the database, a saved `local` profile takes priority, and it cannot be deleted. Compose still runs it in a separate Docker container.
- The new-SOP wizard picks the run sandbox from the project's, shared and global profiles, keeps a template's sandbox when it is available, asks for a replacement when it is not, and can refresh the list without losing the entered fields.
- Project sandbox lists include globally shared profiles.

No migrations are required.

## 0.3.5 — more agent CLIs, parallel SOP branches and a smaller settings menu

- Chat and SOP roles can run ZCode with a personal API key. Personal settings keep a model catalog for Codex, Claude Code and the agent CLIs behind a custom Base URL: fetch the models the service publishes, then pick, reorder and rename them.
- The SOP orchestration assistant chooses its own CLI and model, and scheduled tasks run as ordinary chat turns on the CLI they were created with, using the project owner's key.
- SOP branches whose dependencies are ready run together, and rerunning one cancelled branch restores its cancelled siblings without rerunning their upstream.
- Requirement clarification is optional: new, imported and synced issues no longer start a clarification round, and 待澄清 no longer blocks triggering an SOP.
- Main Bot lists issues and pull requests, checks PR status, comments and adds labels on GitHub without opening a workspace.
- The icon rail keeps only daily work; 流程模板, 运行记录 and 定时任务 move into a two-level settings sidebar, MCP, rules and skills share one 能力资产 page, and personal tokens are called 访问密钥.
- The conversation sidebar marks a reply that finished while you were elsewhere with a blue dot.
- Opening a workspace reports clone and image-build progress and stops a step that has stopped moving. A failed or interrupted restore is no longer reported as ready, a checkpoint operation that meets an open terminal fails at once, and resuming a new work branch refuses a remote branch someone else published.
- Quick access explains before the last step when a member cannot bind repositories to the selected project. Issues that cannot be deleted say why, and stages that have not started show no duration.
- Saving an unchanged workflow no longer creates a revision, and publishing the same content keeps the current version.
- Malformed sign-ups no longer spend the per-IP login budget, and each server keeps its own password-attempt limits.
- The image builds with Go 1.26.11 (GO-2026-6859). `scripts/install.sh` offers to install missing curl, Python and the Docker Compose plugin with apt, dnf, yum or Homebrew.
- LoopForge Local supports DeepSeek Harness (DSH) and Hermes Agent.

Existing installations may apply `migrations/026_assistant_session_reads.sql` and `migrations/027_cli_model_catalog.sql`; the platform also adds these tables and columns on startup.

## 0.3.4 — ARM64 platform images and shared CodeBuddy keys

- Platform administrators create accounts and reset passwords under 平台管理 → 成员账号, so teams that turn off self sign-up no longer need the `loopforge-account` command.
- Platform administrators can share one CodeBuddy API Key under 平台管理 → 共享 CodeBuddy Key and choose the models it may run. Members without their own key chat and run SOPs on it within those models; `AGENTFLOW_ASSISTANT_SPONSOR_LOGIN` still applies while none is configured. Members with their own key keep their own network environment's models and default model; previously a configured sponsor also changed their default.
- A stored CodeBuddy network environment such as `china` can be saved again as-is to rewrite it, and the quick-access wizard no longer keeps an environment switched back to 国际站 elsewhere.
- In English, the navigation, the top bar and the 个人配置 page (now "Personal settings") no longer show Chinese labels, tooltips or credential descriptions.
- The platform image is published for linux/amd64 and linux/arm64, so Apple silicon Macs and arm servers can pull it. On a native arm64 host the bundled CodeBuddy and Codex CLIs, chat, and SOP runs in the Docker sandbox behave and perform like amd64. Under QEMU emulation (an arm64 image on an x86 host) CodeBuddy starts about ten times slower and the model list cannot be read; use the amd64 image there.
- Prepare nested sandbox mount targets before Docker starts, so Docker Desktop can launch SOP containers with VirtioFS-backed workspaces.

Existing installations may apply `migrations/024_platform_codebuddy_key.sql`; the platform also creates the table on startup.

## 0.3.3 — Codex and Claude Code roles in Docker sandboxes

- Codex SOP roles in Docker sandboxes run with `--sandbox danger-full-access`: the container is the isolation boundary, and Codex's own bubblewrap sandbox cannot create namespaces inside it, so every shell command, file write and `submit_gate_result` call failed.
- Claude Code SOP roles run with `--permission-mode bypassPermissions` (as CodeBuddy roles already did) instead of `dontAsk`, which denied every file write, shell command and the gate tool. Docker sandboxes set `IS_SANDBOX=1`, without which Claude Code refuses that mode as root.
- The installer labels the addresses it prints (public, LAN, this machine) and reads the public IP from the cloud provider's link-local metadata service, so a cloud host behind NAT no longer shows only its private address.
- The 0.3.0 and 0.3.1 images were republished without Claude Code.

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
