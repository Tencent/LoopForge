# Changelog

Platform releases use `vX.Y.Z` tags and publish `ghcr.io/tencent/loopforge`. LoopForge Local packages use `local-vX.Y.Z` tags and are published by manually running the `release-local` workflow.

## Unreleased

- Project owners can no longer save a repository `cloneUrl` that points at the platform or runner host itself: loopback (`127.0.0.0/8`, `::1`, `localhost`, and forms such as `127.1` or `2130706433`), unspecified, link-local (including `169.254.169.254` metadata) and multicast hosts return 400. Private ranges such as `10.0.0.0/8` and `192.168.0.0/16` stay allowed for self-hosted Git servers. Platform administrators, and deployments with `AGENTFLOW_AUTH_REQUIRED` off, are unaffected. Repositories already saved are not re-checked. Refs #85.
- The platform image bundles DeepSeek Harness `@deepseek-ai/dsh@0.2.2-rc.1` next to Codex and CodeBuddy, so a DeepSeek key works in conversations and SOP roles without an install step. 0.3.8 had no `dsh` and answered a DeepSeek Harness chat with “AI 助手生成失败，请稍后重试”. The image download grows by about 180 MB. An installation without the image installs the same pinned version from the conversation: every dsh release is a prerelease requiring Node 24.9, and npm on Node 22 found no version for the bare package name.
- A conversation workspace whose image lacks the chosen CLI installs Cursor CLI and Antigravity again. The sandbox drops every capability, so the installer's `tar` running as root could not restore the archive owner (`Cannot change ownership to uid 2000`), and the turn failed with “准备 cursor 原生CLI失败”. The workspace install of DeepSeek Harness uses the pinned `@deepseek-ai/dsh@0.2.2-rc.1` with relaxed engines, as the platform install already did; the bare package name resolved to no version.
- A conversation workspace on a custom Dockerfile without Node.js, npm or Python 3 runs the CLIs bundled in the platform image (Codex, CodeBuddy, DeepSeek Harness). The platform copies its Node.js, npm and a relocatable Python 3.12 into `${LOOPFORGE_DATA_DIR}/cli/toolchain/<build>/` on first use, and a bundled CLI the first time a sandbox needs it; every sandbox mounts that directory read-only at the end of `PATH`, so tools shipped by the image still win. The platform image gains python-build-standalone (about 30 MB to download, checked against a pinned SHA-256). Other CLIs are still installed inside the sandbox, now with the mounted npm. A musl (Alpine) or foreign-architecture image, which lacks the loader the copied `node` needs, keeps its own `PATH` and installs with its package manager as before. Each platform build gets its own directory of about 200 MB plus the CLIs it copied; directories of earlier builds are not removed, so delete the other `cli/toolchain/<build>/` directories after an upgrade.
- A DeepSeek Harness reply longer than 8 KB (about 2,700 Chinese characters) is shown in full. `dsh --json` cuts every event string at 8 KiB and only its `final` event carries the whole answer, so the chat stopped mid-sentence while the saved reply was complete.
- SOP sandboxes of the published image install OpenCode, Kimi Code and Pi on first use, as they already did for Claude Code. An SOP role on one of these CLIs used to fail with `exec: <cli>: not found` (exit 127) even after a conversation installed it.
- Run logs and telemetry redact every environment variable ending in `_API_KEY`, `_TOKEN`, `_SECRET` or `_PASSWORD`. Agent CLI keys such as `DEEPSEEK_API_KEY`, and `AGENTFLOW_CLI_API_KEY` used with a custom Base URL, used to appear in clear text in the `docker exec` line of an SOP run log, readable by project members. Rotate a key that ran an SOP role before this release.
- Upgrading with `./start.sh pull` now re-applies the schema pieces gorm AutoMigrate cannot create: the `run_telemetry.adapter` and `run_logs.level` generated columns from `migrations/001_run_obs_generated_columns.sql`, and the `INSERT IGNORE` starter catalog. A data volume from an older release no longer fails with `Error 1054 Unknown column 'adapter'` the first time someone opens a run detail. The installer and the upgrade command share one script, `scripts/install-schema-extensions.sh`, and a failed step stops the command with the migration file an operator has to run by hand.
- Deleting a conversation removes its shared native root (`shared/<hash>`). It used to stay on disk, about 73 MB each because every root holds its own CLI home with the CodeBuddy plugin marketplace, so a scheduled task that replaces its previous run on every cycle grew the disk by one root per run. The daily session janitor also removes roots left behind earlier once their conversation is gone and they have not changed for a day.
- CodeBuddy no longer downloads its built-in plugin marketplaces into every conversation and workspace. Each one has its own CLI home, so each new conversation used to take about 72 MB; it now takes under 1 MB. LoopForge installs no CodeBuddy plugins, and conversations that already downloaded the marketplaces keep working.
- Native conversation roots move to `AGENTFLOW_CONVERSATION_DIR`, which `compose.yaml` sets to `${LOOPFORGE_DATA_DIR}/conversations`. They used to sit in the `platform-data` volume under `/data/assistant`, a path the host Docker daemon does not have, so opening a conversation workspace on a Compose installation failed with `mkdirat ... read-only file system`. Attachments and other per-user assistant data stay in `/data/assistant`; nothing needs moving. A customised `compose.yaml` needs the same setting. `docs/deployment.md` now spells out the upgrade steps: update the checkout, then `./start.sh pull`.
- The built-in `generic-backend` Dockerfile includes `openssh-client` again, matching the platform image, so a `generic-docker` workspace or SOP sandbox starts on the platform image instead of building `agentflow-img-*` on first use. #207 added the package to the platform image but not to the starter catalog. The installer and `./start.sh pull` run `migrations/031_generic_backend_openssh.sql`, which updates the row only while it still holds the unedited previous default.
- `docs/deployment.md` splits `migrations/` into scripts the installer already covers, scripts an upgrade must run once, and scripts that only backfill existing rows.
- The 预览/源码 switch in the chat artifact preview now matches the 30px copy and download buttons beside it (36px on narrow screens) instead of using the 48px page-level tab size.
- Requirement clarification, Bot replies, run evals and the SOP preview build list only CodeBuddy models, because they run on CodeBuddy. `POST /api/chat-clarify`, `POST /api/issues/{id}/chat-clarify` and `POST /api/runs/{id}/eval` answer 400 for a model of another CLI (such as `codex:…`) instead of switching it to the CodeBuddy default; a Codex model saved earlier in these pickers falls back to the CLI default (#228).

- Repository clone URLs no longer carry credentials. Saving a `cloneUrl` with a user name or password, such as `https://user:token@github.com/acme/app.git`, is rejected with a pointer to the Git token in personal settings; the repositories API, bootstrap payload, run, task and requirement views, and the runner task management list strip credentials from `cloneUrl` and `webUrl` of rows saved earlier; a `webUrl` with credentials is rejected on save; runner clone errors and redacted Git output mask them. `ssh://git@host/...` and `git@host:path` remotes are still accepted.

Repositories saved earlier with a token in `cloneUrl` keep cloning until they are saved again. Saving one from the studio stores the URL without the token, so first put the Git token in 个人配置, then save the repository. Until then the token also lands in each workspace's `.git/config`, where the agent can read it, so re-save such repositories soon after upgrading.

## 0.3.8 — header sign-in hardening and a GHCR mirror option

- With `AGENTFLOW_TRUST_IDENTITY_HEADERS=true`, identity headers such as `Staffname` are ignored on requests that carry `X-Forwarded-For`, `Forwarded` or `X-Real-IP`, unless the gateway also sends `X-AgentFlow-Gateway-Secret` matching the new `AGENTFLOW_IDENTITY_HEADER_SECRET`. A client behind a pass-through reverse proxy could otherwise sign in as any account. Deployments with the option off are unaffected.
- MCP, REST and quick-access snippets copied from an HTTPS studio use `https://` endpoints, so agents no longer send their Bearer token over cleartext HTTP.
- `MIRROR=<host> ./start.sh` pulls `<host>/tencent/loopforge:<version>` and tags it back to the name pinned in `compose.yaml`. `ghcr.nju.edu.cn` is one such host. Unset `MIRROR` still pulls `ghcr.io` directly. `MIRROR=<host> ./start.sh pull` refreshes through the same host. Setting `MIRROR` trusts that host. The published image stays on GHCR, and the GitHub Release records its digest. `registry-mirrors` such as `https://docker.nju.edu.cn` only accelerates Docker Hub.
- The release workflow publishes GHCR only. It no longer copies the image to `ccr.ccs.tencentyun.com`.
- The assistant asks before handing a requirement to an SOP, does not run a rejected proposal, and no longer runs the same draft twice after a timeout.
- The side browser renders sandbox dev servers natively instead of through screencast frames.
- The workbench shows the image's short git commit in the corner.
- The AI orchestrator's canvas build animations are back, and follow the dark theme.
- The chatbot clears the missing-key notice once quick access saves a key.
- Sandbox Git cleanup keeps `index.lock` while a process holds it through a bind-mounted or hard-linked path.
- Tool messages and executor output are clipped on UTF-8 character boundaries.

Before upgrading a deployment that signs users in by header behind a proxy adding forwarding headers, set `AGENTFLOW_IDENTITY_HEADER_SECRET` and have the gateway send it in `X-AgentFlow-Gateway-Secret`; otherwise header sign-in falls back to guest after the upgrade. See `docs/deployment.md`.

No migrations are required.

## 0.3.7 — Tencent Cloud image mirror

- Platform releases also push `ccr.ccs.tencentyun.com/loopforge/loopforge` with the same tags as `ghcr.io/tencent/loopforge`. `compose.yaml` still pins GHCR. Install from Tencent Cloud with `LOOPFORGE_IMAGE=ccr.ccs.tencentyun.com/loopforge/loopforge:0.3.7 ./start.sh`.
- A running SOP node animates its activity on the graph.
- Approving a run reloads the queue from the server, so the waiting card disappears once execution continues.
- LoopForge Local's Classic CodeBuddy hook keeps manual gates when PyYAML is not installed.
- The TKE node-pool tool keeps the SDK error and CAM hint when its fallback query fails.

No migrations are required.

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
