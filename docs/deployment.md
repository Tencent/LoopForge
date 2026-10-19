# Deployment

## Native development

`bash scripts/dev.sh` explicitly configures local secrets, starts only this checkout's MySQL service, builds the platform, provisions the `admin` account, installs the missing observability generated columns through an explicit script, applies `migrations/seed/*.sql`, and starts the server on port 8090. Existing catalog definitions are preserved by `INSERT IGNORE`. Keep `.env`, `.local/` and database volumes outside version control.

For a native installation, use Node.js 22+ on the API host and install the CLIs you choose. CodeBuddy is also used for requirement clarification and scheduled assistant tasks:

```bash
npm install --global @tencent-ai/codebuddy-code
codebuddy --version
# Optional executors for Main Bot and SOP roles:
npm install --global @openai/codex@0.162.1 @anthropic-ai/claude-code@2.1.300
```

Ensure the service can find `codebuddy` and Node.js on its PATH, or set `AGENTFLOW_CODEBUDDY_BIN` to the installed executable. Configure the model key and its network environment in personal settings. The no-key script demo does not need the CLI. The production Docker image already includes CodeBuddy, Codex and Claude Code; Docker sandboxes and connected runners have their own CLI requirements.

Stop the foreground service with Ctrl-C. Start it again with the same command. MySQL data stays in its Compose volume. `docker compose stop mysql` stops the database without deleting it.

Use `AGENTFLOW_MYSQL_DSN` for your own MySQL deployment. A fresh account command or server connection creates missing schema objects. Apply existing-installation data migrations individually after reviewing their prerequisites; do not blindly replay all historical migrations against a new database.

## Container installation

```bash
bash scripts/install.sh
```

The installer checks Docker, Compose and Python (and offers to install Docker when it is missing; set `LOOPFORGE_YES=1` to accept without prompting), generates `.env` with unique secrets, builds and starts the containers, installs the observability columns and applies `migrations/seed/*.sql`. It is safe to re-run. Set `AGENTFLOW_HTTP_PORT` or `LOOPFORGE_MYSQL_PORT` in the environment before the first run (or edit `.env` later) when the default ports are taken.

Open the platform and create the first account in the browser; it receives platform administration. Until that account exists the login page only offers this setup step.

The container listener is published to `127.0.0.1` by default. Set `LOOPFORGE_BIND` or put an HTTPS reverse proxy in front of it when serving other machines. Proxy WebSocket and streaming requests. Set `AGENTFLOW_SECURE_COOKIES=true` when HTTPS terminates at the proxy. Keep `AGENTFLOW_SESSION_SECRET` stable across API instances and restarts.

### Docker sandboxes

For native services running as an ordinary host user, default/root Docker images run with that service user's UID/GID so private workspaces remain accessible without relaxing their permissions or adding capabilities. Images that explicitly select a non-root user retain that identity; operators must arrange compatible bind-mount ownership for those images.

The Compose deployment mounts the host Docker socket into the platform container so built-in AI SOPs run out of the box. On startup the platform builds the shared sandbox image in the background, so it is usually ready by the time the first SOP starts. For each run the platform reuses the image built for that Dockerfile and starts a sibling container on the Compose network with the run workspace mounted at `/workspace`. Because those bind mounts are resolved by the host Docker daemon, `LOOPFORGE_DATA_DIR` (written to `.env` by `scripts/configure.sh`, default `./.data`) is mounted into the platform container at the same absolute path; workspaces and CLI state live under it.

Access to the Docker socket is equivalent to root on the host. Two safeguards follow from that: the platform runs every task in a sandbox container, including SOPs configured for the `local` driver (`AGENTFLOW_LOCAL_SANDBOX=docker`), and only platform administrators can point a repository at a host source path. Even so, anyone who can run SOPs can execute code in containers on this host, so only give accounts to people you trust and do not expose the service to untrusted networks. To run without the socket, remove the `docker.sock` volume and the `AGENTFLOW_LOCAL_SANDBOX` setting from `compose.yaml`, then connect a separate Docker-capable worker or execution machine for AI SOPs; the script-only welcome SOP then runs inside the platform container.

## Accounts

The first account created in the browser becomes the platform administrator. After that, anyone who can reach the login page may sign up as a regular user while `AGENTFLOW_ALLOW_SIGNUP` is `true` (the default). Registered users can create requirements and run SOPs on your workers, so set `AGENTFLOW_ALLOW_SIGNUP=false` for any installation reachable from untrusted networks and create accounts explicitly instead.

LoopForge assumes a trusted team. Every signed-in account can list all projects, requirements and runs. Project settings and repositories can be changed only by the project owner or a platform administrator, and personal credentials only by their owner. Do not use one installation to separate untrusted tenants.

For a native installation, export the `.env` values before running the explicit account command:

```bash
set -a; source .env; set +a
read -r -s -p 'Password: ' account_password; echo
printf '%s\n' "$account_password" | go run ./cmd/loopforge-account -user alice
unset account_password
```

Use `-admin` only when granting platform administration. Running the command again rotates that account's password. Password hashes use bcrypt. Browser sessions use signed HttpOnly, SameSite cookies and expire after seven days. No named person receives an implicit administrator role; additional administrators can be managed through the existing platform administration UI.

## Workers and runners

The upstream process roles remain `-role api`, `-role worker` and `-role all`. API and worker instances must share the MySQL database and configuration appropriate to their deployment. Set `AGENTFLOW_CONTROL_TOKEN`, `AGENTFLOW_CONTROL_URL` and `AGENTFLOW_WORKER_URL` when separating control and execution. Set `AGENTFLOW_PLATFORM_MCP_URL` to the actual platform URL reachable from execution containers.

Personal runners are connected from the execution-machine page with a short-lived binding code. The installer is served at `/runners/install.sh`. `AGENTFLOW_RUNNER_BINARY` can point to binaries with `{os}` and `{arch}` substitutions. Container images bundle Linux/macOS amd64/arm64 runner and gate binaries. Native source installations can build on demand with Go.

Configure provider credentials in the platform UI, not the host's global CLI configuration. Credentials are bound to the responsible actor by the upstream runtime. The home composer and SOP roles support CodeBuddy, Codex and Claude Code independently. Select the executor beside the home model selector; its API key (and optional Codex/Claude base URL) comes from your personal settings. Changing the executor preserves the draft; saved conversations record the executor used. Main Bot writes still pass through the platform tool gateway and its approval checks. Requirement clarification, scheduled assistant tasks and notifications continue to use CodeBuddy. Live model tests are opt-in; set `AGENTFLOW_LIVE_TESTS=1` only for the acceptance suites that need to load local credentials.

## Observability schema

After the base tables have been created, explicitly run `bash scripts/install-observability-schema.sh` for the Compose database. It installs absent `run_telemetry.adapter` and `run_logs.level` columns and their indexes from upstream migration `001_run_obs_generated_columns.sql`. The local development installer calls this step before installing templates. It can be repeated without reapplying existing columns. Application startup does not run data migrations.

For an external MySQL deployment, review that migration and check column existence before applying its corresponding statements. Stored generated columns can rebuild large existing tables; schedule the operation accordingly.

## Updating existing public SOPs

Repository binding is optional for all built-in generic SOPs. To update the original catalog instructions on an existing installation, explicitly apply:

```bash
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/016_public_optional_repositories.sql
```

This repeatable SQL changes only instruction fields that still match the original public seed. It preserves operator edits and project copies. New installations already receive these instructions from the public catalog seed; application startup does not migrate data.

## GitHub credentials

GitHub Issue import and PR status synchronization use the project owner's Git token. Git operations and the optional GitHub MCP integration use the responsible user's token. Public reads may work without a token; private repositories and writes require repository access. GitHub MCP must be installed explicitly and sends credentials only to its official HTTPS endpoint.

Git commits created by the platform use the responsible local account name and a non-deliverable `@users.noreply.loopforge.invalid` email. A local account does not establish a corporate email or a verified GitHub identity.

## CodeBuddy authentication environment

In personal settings, choose the network environment belonging to the API key. A key from one edition may return HTTP 401 when sent to another edition. The setting is per user; public installations do not assume an enterprise environment.

| Edition | CodeBuddy network environment |
| --- | --- |
| International | Leave empty |
| China | `internal` |
| iOA | `ioa` |

Other dedicated/self-hosted configurations are described in the [official CodeBuddy identity documentation](https://www.codebuddy.ai/docs/cli/iam). Configure the model catalog with `AGENTFLOW_ASSISTANT_MODEL` and `AGENTFLOW_ASSISTANT_MODELS`; ordinary roles can select a model independently.

## Isolated container acceptance

From the repository root, run `bash scripts/test-container.sh` after installing `e2e/` dependencies and Chromium. It builds the production image, creates an isolated Compose project and database on ports 8093/33317, provisions test accounts, explicitly applies the observability baseline and public catalog, checks bundled CLIs and cross-platform runner/gate binaries, and runs the public browser suite. Its containers and volumes are removed afterwards. Override `LOOPFORGE_CONTAINER_TEST_PORT` and `LOOPFORGE_CONTAINER_MYSQL_PORT` when those ports are in use. Model calls are disabled for this clean-install check.
