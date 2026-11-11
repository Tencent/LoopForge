# Deployment

## Native development

`bash scripts/dev.sh` explicitly configures local secrets, starts only this checkout's MySQL service, builds the platform, provisions the `admin` account, installs the missing observability generated columns through an explicit script, applies the knowledge-engine migrations and their ngram check, applies `migrations/seed/*.sql`, and starts the server on port 8090. Existing catalog definitions are preserved by `INSERT IGNORE`. Keep `.env`, `.local/` and database volumes outside version control.

For a native installation, use Node.js 22+ on the API host and install the CLIs you choose. CodeBuddy is also used for requirement clarification and scheduled assistant tasks:

```bash
npm install --global @tencent-ai/codebuddy-code
codebuddy --version
# Optional executors for Main Bot and SOP roles:
npm install --global @openai/codex@0.162.1 @anthropic-ai/claude-code@2.1.300
```

Ensure the service can find `codebuddy` and Node.js on its PATH, or set `AGENTFLOW_CODEBUDDY_BIN` to the installed executable. Configure the model key and its network environment in personal settings. The no-key script demo does not need the CLI. The production Docker image includes CodeBuddy and Codex. Claude Code is not redistributed: the first Main Bot conversation or SOP role that selects it installs the pinned `@anthropic-ai/claude-code` from npm into `$LOOPFORGE_DATA_DIR/cli`, so that first run waits for the download and later runs reuse it; Docker sandboxes and connected runners have their own CLI requirements.

Stop the foreground service with Ctrl-C. Start it again with the same command. MySQL data stays in its Compose volume. `docker compose stop mysql` stops the database without deleting it.

Use `AGENTFLOW_MYSQL_DSN` for your own MySQL deployment. A fresh account command or server connection creates missing schema objects. Run `bash scripts/knowledge-schema.sh apply --native` before starting the service so the knowledge baseline (002), tables and ngram index exist; `repo.Open` only adds missing objects. Apply other existing-installation data migrations individually after reviewing their prerequisites; do not blindly replay all historical migrations against a new database.

## Container installation

```bash
./start.sh             # install or start in the background (same as bash scripts/install.sh)
./start.sh logs        # follow platform logs
./start.sh stop        # stop; ./start.sh down removes containers but keeps data volumes
./start.sh restart     # restart the platform container
./start.sh pull        # refresh images and recreate changed containers
./start.sh dev         # build this checkout instead (compose.dev.yaml)
./start.sh release     # switch a source build back to the published image
```

The installer checks Docker, Compose and Python (and offers to install Docker when it is missing; set `LOOPFORGE_YES=1` to accept without prompting), generates `.env` with unique secrets, pulls the images, starts MySQL, applies the knowledge baseline (002) and migrations 026 onward with the ngram index check, starts the platform container, then installs the observability columns and applies `migrations/seed/*.sql` through the configured DSN. It is safe to re-run. `./start.sh pull` performs the same knowledge migration and ngram check before recreating the platform container. Both stop this host's platform first; for multiple API/worker instances see [Upgrading a multi-instance deployment](#upgrading-a-multi-instance-deployment). Set `AGENTFLOW_HTTP_PORT` or `LOOPFORGE_MYSQL_PORT` in the environment before the first run (or edit `.env` later) when the default ports are taken.

Run it as the installation owner, including an ordinary Linux user. If that user cannot access Docker directly, the installer validates sudo and uses `sudo -n docker` consistently for Compose, schema installation and seeds. Before each operation it checks whether sudo credentials expired during a long build, prompting again in a terminal or failing with instructions in unattended mode. It does not require adding the user to the Docker group or logging out. For unattended installations run `sudo -v` first; `LOOPFORGE_YES=1` accepts installation prompts but does not bypass operating-system permissions. Without sudo or Docker access it fails immediately with instructions. Rootless Docker uses its active context's Unix socket; remote Docker contexts need a separately configured deployment because sandbox workspaces are local bind mounts.

`compose.yaml` is the published-image stack and never builds: `./start.sh`, `scripts/install.sh` and a plain `docker compose --profile platform up` all pull the GHCR release pinned there (currently `ghcr.io/tencent/loopforge:0.3.4`). The pin matches the checkout's migrations and seeds, so check out a release tag when you install. Set `LOOPFORGE_IMAGE` (for example to an `@sha256:` digest) only to override it; a value saved in `.env` takes precedence over later pin changes.

`./start.sh dev` (or `LOOPFORGE_BUILD_FROM_SOURCE=1 bash scripts/install.sh`) builds this checkout as `loopforge-platform:local` from `compose.dev.yaml`. It saves `COMPOSE_FILE=compose.yaml:compose.dev.yaml` in `.env`, so later runs and plain `docker compose` commands keep using the source build until `./start.sh release`. Source builds reuse npm, Go module and Go compiler caches.

Pushing a `vX.Y.Z` tag on `main` runs the `release` workflow once the `platform` workflow has passed on that commit. The tagged `compose.yaml` must already pin `loopforge:X.Y.Z`. It publishes `ghcr.io/<owner>/loopforge` as `X.Y.Z`, `X.Y`, `latest` and `sha-<commit>`, smoke-tests the pushed digest with `./start.sh`, and creates the GitHub Release. Forks publish under their own owner; set the GHCR package visibility/access for your team before distributing the image. Images are pulled only when missing by default; use an immutable tag/digest, or set `LOOPFORGE_PULL_POLICY=always` when updating a mutable tag. Existing `.env` values take precedence; change the saved image or URL there when updating an installation. Compose waits for the API's health check with a bounded timeout before installing schema extensions and seeds.

Open the platform and create the first account in the browser; it receives platform administration. Until that account exists the login page only offers this setup step.

During initial setup, restrict the cloud security group or firewall to the operator's IP. Do not expose the first-account setup page to arbitrary Internet clients before the administrator account has been created.

The platform listens on `LOOPFORGE_BIND`, `0.0.0.0` by default, without prompting for an access URL. The installer prints the public, LAN and local addresses; on Tencent Cloud, Alibaba Cloud, AWS, GCP and Azure it reads the public IP from the provider's link-local metadata service, and elsewhere behind NAT you open the public IP yourself. `LOOPFORGE_PUBLIC_URL` is optional and only changes the printed address. Allow the HTTP port in the cloud security group/firewall, or use an HTTPS reverse proxy. Existing `.env` files are preserved; older installations without a bind setting now also listen on `0.0.0.0`. Local-only installations can set `LOOPFORGE_BIND=127.0.0.1`. Proxy WebSocket and streaming requests. Set `AGENTFLOW_SECURE_COOKIES=true` when HTTPS terminates at the proxy. Keep `AGENTFLOW_SESSION_SECRET` stable across API instances and restarts.

### Docker sandboxes

For native services running as an ordinary host user, default/root Docker images run with that service user's UID/GID so private workspaces remain accessible without relaxing their permissions or adding capabilities. Images that explicitly select a non-root user retain that identity; operators must arrange compatible bind-mount ownership for those images.

The Compose deployment mounts the host Docker socket into the platform container so built-in AI SOPs run out of the box. `AGENTFLOW_ISOLATED_IMAGE` points to the installed platform image: default sandboxes start separate sibling containers from that same image, with no second CLI download or image build. They do not mount the Docker socket. Custom Dockerfiles still build and cache their own images. The run workspace is mounted at `/workspace` on the Compose network. Because bind mounts are resolved by the host Docker daemon, `LOOPFORGE_DATA_DIR` (written to `.env` by `scripts/configure.sh`, default `./.data`) is mounted into the platform container at the same absolute path; workspaces and CLI state live under it.

Access to the Docker socket is equivalent to root on the host. Two safeguards follow from that: the platform runs every task in a sandbox container, including SOPs configured for the `local` driver (`AGENTFLOW_LOCAL_SANDBOX=docker`), and only platform administrators can point a repository at a host source path. Even so, anyone who can run SOPs can execute code in containers on this host, so only give accounts to people you trust and do not expose the service to untrusted networks. To run without the socket, remove the `docker.sock` volume and the `AGENTFLOW_LOCAL_SANDBOX` setting from `compose.yaml`, then connect a separate Docker-capable worker or execution machine for AI SOPs; the script-only welcome SOP then runs inside the platform container.

### Slow networks: clone and image build limits

Cloning a repository and building a custom sandbox image report progress while they work, and their errors keep the last lines of git or Docker output. On the platform host the progress is recorded on the run. On an execution machine it is written to the runner's log, and only the final error reaches the platform. A step that prints nothing for its stall window is treated as hung and stopped. A step that keeps printing may be slow but is stopped only by a total limit. All settings take Go durations such as `90s` or `20m`; `0` turns a limit off. Each host reads its own environment, so set the clone limits on execution machines too.

| Setting | Default | Applies to |
|---|---|---|
| `AGENTFLOW_GIT_CLONE_STALL_TIMEOUT` | `5m` | `git clone` with no output. Not applied when the platform clones from a repository's local source path, whose final repack is silent. |
| `AGENTFLOW_GIT_CLONE_TIMEOUT` | off on the platform, `10m` on execution machines | Whole `git clone`. Execution machines keep their earlier fixed 10 minute limit as the default. |
| `AGENTFLOW_DOCKER_BUILD_STALL_TIMEOUT` | `10m` | `docker build` with no output. A quiet `RUN` step (a long compile) counts as no output. |
| `AGENTFLOW_DOCKER_BUILD_TIMEOUT` | off | Whole `docker build`. |

A clone that is stopped this way is not retried, since a retry would wait on the same network. When the execution host reaches GitHub or Debian mirrors slowly, point git at a proxy (`git config --global http.proxy`) and use a nearby package mirror in the Dockerfile rather than raising these limits.

## Accounts

The first account created in the browser becomes the platform administrator. After that, anyone who can reach the login page may sign up as a regular user while `AGENTFLOW_ALLOW_SIGNUP` is `true` (the default). Registered users can create requirements and run SOPs on your workers, so set `AGENTFLOW_ALLOW_SIGNUP=false` for any installation reachable from untrusted networks and create accounts explicitly instead.

Platform administrators create accounts under **Platform administration → Member accounts** (平台管理 → 成员账号): enter a login name and an initial password, optionally grant administration, and hand the password to the teammate. The same page lists every account and resets the password of a password account; accounts that sign in through GitHub or a gateway cannot be given a password there. New accounts join the welcome project, like self sign-ups.

LoopForge assumes a trusted team. Every signed-in account can list all projects, requirements and runs. Project settings and repositories can be changed only by the project owner or a platform administrator, and personal credentials only by their owner. Do not use one installation to separate untrusted tenants.

Without a browser, export the `.env` values for a native installation and run the explicit account command:

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

A platform administrator can share one CodeBuddy API Key under **Platform administration → Shared CodeBuddy Key** (平台共享 CodeBuddy Key): pick the network environment that issued it and the models members may use (the first is the default). Members without a key of their own then chat and run SOPs on it, limited to those models; a role configured with another model runs on the default and the run says so. Members with their own key keep using it with any model. Scheduled tasks, clarification, reports, bots and run evaluation still need a personal key. The key is stored in the database like personal keys and is never shown again after saving. `AGENTFLOW_ASSISTANT_SPONSOR_LOGIN`, which lent one account's key limited to `deepseek-v4.1-flash`, still applies while no shared key is configured.

Configure provider credentials in the platform UI, not the host's global CLI configuration. Credentials are bound to the responsible actor by the upstream runtime. The home composer and SOP roles support CodeBuddy, Codex and Claude Code independently. Select the executor beside the home model selector; its API key (and optional Codex/Claude base URL) comes from your personal settings. Changing the executor preserves the draft; saved conversations record the executor used. Main Bot writes still pass through the platform tool gateway and its approval checks. Requirement clarification, scheduled assistant tasks and notifications continue to use CodeBuddy. Live model tests are opt-in; set `AGENTFLOW_LIVE_TESTS=1` only for the acceptance suites that need to load local credentials.

## Knowledge engine schema

The built-in knowledge engine has two table groups. The control plane lives in the main database (`AGENTFLOW_MYSQL_DSN`): knowledge bases, sources, bindings, sync jobs, review metadata, writeback records, tombstones, feedback, gaps, governance results, activity and citations. The content plane owns document bodies, hosted revisions, index chunks, the review-body staging table and retraction fences. Set `AGENTFLOW_KNOWLEDGE_MYSQL_DSN` to a separate MySQL database to isolate it; empty reuses the main connection. In Compose, the DSN must be reachable from the platform container (for example `loopforge:PASSWORD@tcp(mysql:3306)/loopforge_content`, after creating that database); the migration helper maps `127.0.0.1:<published MySQL port>` to the Compose MySQL service so the same `.env` also works for native runs. Never place the content tables in the control database when a separate content DSN is configured, and never create foreign keys across the two groups.

`repo.Open` only adds missing tables/columns/indexes. It cannot create a MySQL full-text index with the ngram parser, and it does not run data migrations. Apply the scripts before starting the service:

```bash
bash scripts/knowledge-schema.sh apply --compose   # container install
bash scripts/knowledge-schema.sh check --compose
# Native development or an external MySQL:
bash scripts/knowledge-schema.sh apply --native
bash scripts/knowledge-schema.sh check --native
```

The helper applies the 002 knowledge baseline (needed by 035) and then every script from 026 onward in numeric order. It parses both DSNs, runs each control segment against the control database and each content segment against the content database, and is safe to repeat. For 037 it copies the control-plane ID mapping into the content database before the content segment, so a content database on another MySQL server is supported. The scripts may also be applied by hand in numeric order; `-- @plane control` / `-- @plane content` marks the target group in files with both. When running the 036 content segment by hand against a separate content database, set `@knowledge_control_schema` to the control database first (the helper does this); it defaults to the current database, which is only correct when both planes share one database. For 037, import `knowledge_id_migration_037` into the content database before applying its content segment when the two databases are on different servers; the content segment also copies it automatically when it can see the control schema on the same server.

| Script | Plane | Purpose |
|---|---|---|
| `002_knowledge_base.sql` | control | Pre-026 baseline: `knowledge_submissions` (and legacy settings); applied before 026 on every install/upgrade |
| `026_builtin_knowledge.sql` | control + content | Knowledge bases/sources/bindings/sync jobs; documents/revisions/chunks and the ngram full-text index |
| `027_knowledge_activity.sql` | control | Agent retrieval call records |
| `028_knowledge_vector.sql` | control | Per-base vector index contract columns |
| `029_knowledge_git_credentials.sql` | control | Encrypted Git source credentials |
| `030_shared_knowledge.sql` | control | Normalize shared-library bindings to read-only (including NULL) |
| `031_knowledge_writeback.sql` | control | Git branch / merge-request writeback records |
| `032_knowledge_sediment.sql` | control | Sediment publish mode, rules and tombstones |
| `033_knowledge_governance.sql` | control + content | Feedback, gaps, governance scans; document frontmatter metadata columns |
| `034_knowledge_citation.sql` | control | Structured citation records |
| `035_knowledge_concurrency.sql` | control + content | Submission version/fingerprint/CAS columns, writeback claims, operation leases; content fences and immutable review-body staging |
| `036_knowledge_schema_cleanup.sql` | content + control | Review-body staging table, binary path collation, nullable writeback submission ID, legacy payload-body migration |
| `037_knowledge_source_namespace.sql` | control + content + control | Rename legacy hosted base/source IDs, move content rows and fences to the new IDs, migrate control references, record schema version 37 |

`035`, `036` and `037` are data migrations, not just DDL. Do not run them while an old API or worker can still write review bodies, sync content or create sources. `037` also writes `knowledge_schema_versions(37)`; the new binary refuses to start until that row exists.

### Upgrading a multi-instance deployment

The install and update scripts stop only the platform container on the current host. With more than one API/worker instance, all of them share the same MySQL and must be quiesced:

1. Stop every API and worker process (all hosts, all Compose projects, Kubernetes replicas and standalone `-role api`/`-role worker` processes). Do not start the new version yet.
2. Confirm the control database has no live worker rows:
   ```sql
   SELECT instance_id, role, lease_until FROM engine_instances
   WHERE lease_until > UTC_TIMESTAMP(3);
   ```
   The script checks this table before applying 035 and waits up to `LOOPFORGE_MIGRATION_QUIESCE_SECONDS` (default 45s) for a just-stopped worker's lease to expire. If live rows remain it aborts with their IDs. API-only processes do not heartbeat, so the SQL check and the documented stop step still matter.
3. Run the migration once, from one host:
   ```bash
   bash scripts/knowledge-schema.sh apply --compose   # or --native with exported DSNs
   bash scripts/knowledge-schema.sh check --compose
   ```
   For a content database on another server, run the same command with `AGENTFLOW_KNOWLEDGE_MYSQL_DSN` exported; 037's mapping is imported into the content database before its content segment.
4. Start the new binaries only after the script and ngram check succeed. Starting an older binary after 037 is not supported, and starting the new binary before 037 fails fast with the repair command.

`./start.sh pull` performs steps 2–4 for this host's Compose platform, but it cannot stop remote API/worker instances; stop them first.

`036` moves review bodies that older installations kept in `knowledge_submissions.payload` into the content-plane `knowledge_submission_contents` table, then removes the `content` key from the control payload. The copy works when the control and content databases are on the same MySQL server (including two schemas on that server) and is skipped otherwise. For a content database on another server, export the affected payloads first, import them into `knowledge_submission_contents`, and only then run the control-plane `UPDATE ... JSON_REMOVE(payload, '$.content')` cleanup manually; the exact statement is in the script comments.

Before the API starts, `scripts/knowledge-schema.sh check` verifies that `knowledge_chunks` has the FULLTEXT index `ft_knowledge_chunks_text` using the ngram parser. If it is missing, retrieval fails with `Can't find FULLTEXT index matching the column list`. The check fails with the repair command; it never creates the index from Go code:

```bash
# Repair a content database that missed 026:
bash scripts/knowledge-schema.sh apply
```

For an external MySQL deployment, apply the same order and segments on the control and content databases, then run the check with both DSNs exported. A fresh schema created only by `repo.Open` is not enough for knowledge retrieval.

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

Users can choose **Connect GitHub** in personal credentials or while adding a repository, copy the displayed PIN, and approve it on GitHub. The platform saves the grant to that signed-in user's existing Git credential slot. The repository picker lists repositories owned by the account, shared with it, or accessible through its organizations, supports pagination and search over loaded repositories, and copies the selected repository's real default branch into the form. PAT entry remains available.

New installations use the built-in LoopForge OAuth App. Users do not create applications, configure a Client ID, or request a PAT: choose **Connect GitHub**, copy the PIN and approve the LoopForge application on GitHub. GitHub's confirmation cannot be bypassed; device authorization is not a zero-interaction login.

Application configuration is a deployment concern and is not shown in personal credentials. Operators who need their own application can register an OAuth App, enable **Device Flow**, keep token expiration disabled until refresh support is available, and set its public Client ID with `AGENTFLOW_GITHUB_CLIENT_ID` in `.env`. No client secret is needed. The deployment environment overrides the built-in application and any database override. The administrator-only `PUT /api/auth/github/config` endpoint remains available for an explicit database override; an empty Client ID explicitly disables device authorization rather than silently restoring the default. Existing explicit configurations are preserved.

The flow requests the OAuth `repo` scope so private cloning, pushes, Issues and PR operations work. GitHub's OAuth scope covers accessible repositories; choosing a repository in LoopForge binds it to the project and does not narrow the OAuth grant. Organization SSO or third-party-app restrictions may require organization approval. See [GitHub's device authorization documentation](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow). Pending grants expire and are held only in API memory: use session affinity for `/api/auth/github/*` behind multiple API instances, and restart authorization after an API restart. Tokens and GitHub device codes are never sent to the browser.

GitHub Issue import and PR status synchronization use the project owner's Git token. Git operations and the optional GitHub MCP integration use the responsible user's token. Public reads may work without a token; private repositories and writes require repository access. GitHub MCP must be installed explicitly and sends credentials only to its official HTTPS endpoint.

Git commits created by the platform use the responsible local account name and a non-deliverable `@users.noreply.loopforge.invalid` email. A local account does not establish a corporate email or a verified GitHub identity.

## CodeBuddy authentication environment

In personal settings, choose the network environment belonging to the API key; the "get API key" link opens that edition's key page. A key from one edition returns HTTP 401 when sent to another edition, and keys from third-party model providers (DeepSeek, OpenAI and similar) are not CodeBuddy keys. The setting is per user; public installations do not assume an enterprise environment.

| Edition | Stored value | Key page |
| --- | --- | --- |
| International | empty | https://www.codebuddy.ai/profile/keys |
| China | `internal` | https://copilot.tencent.com/profile/ |
| iOA | `ioa` | https://tencent.sso.copilot.tencent.com/profile/keys |

`PUT /api/users/:id/credentials` accepts only these values (`public` and `China` are also understood) and rejects anything else with HTTP 400, because the CLI silently falls back to the international edition for values it does not recognize.

Other dedicated/self-hosted configurations are described in the [official CodeBuddy identity documentation](https://www.codebuddy.ai/docs/cli/iam). Configure the model catalog with `AGENTFLOW_ASSISTANT_MODEL` and `AGENTFLOW_ASSISTANT_MODELS`; ordinary roles can select a model independently.

## Isolated container acceptance

Run `python3 scripts/test-install.py -v` for installer regression tests covering sudo selection, direct Docker access, unattended permission failures, repeated installation, image mode and access URLs. It also runs the real `knowledge-schema.sh` against the repository migrations with a recording MySQL client, covering the 002→035 baseline order, 037's control/content/control split, mapping import into a separate content database, fence migration, live-instance refusal and DSN-driven seeding. With a locally built platform image, `LOOPFORGE_INSTALL_TEST_IMAGE=your-image bash scripts/test-install-live.sh` verifies real root and UID 65534 installations, non-loopback HTTP access, first-account creation and repeated installation in isolated Compose projects on random ports, with all migrations including 002 and 030–037 copied into the fixture. This live test requires a Linux root test runner to drop privileges; it removes its own containers and volumes afterwards. To also exercise real sudo inside a disposable UID 1000 installer container, build `scripts/testdata/install-sudo.Dockerfile` with `--build-arg LOOPFORGE_TEST_BASE=your-image`, then pass that test image as `LOOPFORGE_INSTALL_SUDO_TEST_IMAGE`. The production installer itself does not require root.

For a real MySQL upgrade test (old payload body, legacy hosted IDs, retraction fence and an independent content database), set `AGENTFLOW_TEST_MYSQL_DSN` to a disposable server and run:

```bash
AGENTFLOW_TEST_MYSQL_DSN='root:...@tcp(127.0.0.1:3306)/kb_test' \
  go test ./internal/repo -run 'TestKnowledgeMigrationsOnSeparateContentDatabase|TestKnowledgeNamespaceMigrationRepeatable' -count=1
```
These tests create and drop their own databases; do not point them at production.

From the repository root, run `bash scripts/test-container.sh` after installing `e2e/` dependencies and Chromium. It builds the production image, creates an isolated Compose project and database on ports 8093/33317, provisions test accounts, explicitly applies the observability baseline and public catalog, checks bundled CLIs and cross-platform runner/gate binaries, and runs the public browser suite. Its containers and volumes are removed afterwards. Override `LOOPFORGE_CONTAINER_TEST_PORT` and `LOOPFORGE_CONTAINER_MYSQL_PORT` when those ports are in use. Model calls are disabled for this clean-install check.
