# Deployment

## Native development

`bash scripts/dev.sh` explicitly configures local secrets, starts only this checkout's MySQL service, builds the platform, provisions the `admin` account, installs the missing observability generated columns through an explicit script, applies `migrations/seed/*.sql`, and starts the server on port 8090. Existing catalog definitions are preserved by `INSERT IGNORE`. Keep `.env`, `.local/` and database volumes outside version control.

For a native installation, use Node.js 22+ on the API host and install the CLIs you choose. CodeBuddy is also used for requirement clarification; scheduled assistant tasks use whichever supported CLI the task selects:

```bash
npm install --global @tencent-ai/codebuddy-code
codebuddy --version
# Optional executors for Main Bot and SOP roles:
npm install --global @openai/codex@0.162.1 @anthropic-ai/claude-code@2.1.300
```

Ensure the service can find `codebuddy` and Node.js on its PATH, or set `AGENTFLOW_CODEBUDDY_BIN` to the installed executable. Configure the model key and its network environment in personal settings. The no-key script demo does not need the CLI. The production Docker image includes CodeBuddy, Codex and DeepSeek Harness (`dsh`). Claude Code and the other agent CLIs are not bundled. When a conversation fails because one is missing, the error says it is not installed and offers an install button. That downloads the pinned `@anthropic-ai/claude-code` release, or the public package for OpenCode, Kimi Code and Pi, into `$LOOPFORGE_DATA_DIR/cli`; an installation outside the image gets the pinned `@deepseek-ai/dsh` release the same way. Cursor CLI and Antigravity use their providers' installers and land in the service user's `~/.local/bin`. Closing or reloading the browser does not stop an install that has already started, and the button stays on “自动安装” until it finishes. A finished install replaces the earlier generation failure with a prompt to send the message again. The next message reuses the installation. An SOP sandbox of the published image that selects Claude Code, OpenCode, Kimi Code or Pi installs the same package on first use and shares it with conversations. Cursor CLI and Antigravity are not installed into SOP sandboxes. Connected runners keep their own CLI requirements.

A conversation workspace on a custom Dockerfile without Node.js, npm or Python 3 uses the platform's own copies. On first use the platform copies its Node.js, npm and a relocatable Python into `$LOOPFORGE_DATA_DIR/cli/toolchain/<build>/`, plus each bundled CLI (CodeBuddy, Codex, DeepSeek Harness) the first time a sandbox needs it, and mounts the directory read-only at the end of the sandbox `PATH`. Tools the image ships take precedence. Only glibc images of the platform's architecture use it; Alpine (musl) images need their own Node.js, npm and Python 3. A new platform build creates a new directory and leaves the previous ones in place: after an upgrade, delete the other `cli/toolchain/<build>/` directories (about 200 MB each plus the CLIs copied into them).

Stop the foreground service with Ctrl-C. Start it again with the same command. MySQL data stays in its Compose volume. `docker compose stop mysql` stops the database without deleting it.

Use `AGENTFLOW_MYSQL_DSN` for your own MySQL deployment. A fresh account command or server connection creates missing schema objects. Apply existing-installation data migrations individually after reviewing their prerequisites; do not blindly replay all historical migrations against a new database.

## Container installation

```bash
./start.sh             # install or start in the background (same as bash scripts/install.sh)
./start.sh logs        # follow platform logs
./start.sh stop        # stop; ./start.sh down removes containers but keeps data volumes
./start.sh restart     # restart the platform container
./start.sh pull        # refresh images and recreate changed containers
MIRROR=ghcr.nju.edu.cn ./start.sh  # same install; replace the ghcr.io host with this registry
./start.sh dev         # build this checkout instead (compose.dev.yaml)
./start.sh release     # switch a source build back to the published image
```

The installer checks Docker, Compose, Python 3 and curl. It offers to install missing Python/curl packages using apt, dnf or yum on Linux (Homebrew on macOS), Docker using its official installer, and the missing Linux x86_64/arm64 Compose plugin using the pinned official release with SHA-256 verification. A plugin is installed for the active Docker user, or system-wide when Docker requires sudo. macOS Compose comes from Docker Desktop. Set `LOOPFORGE_YES=1` to accept installation prompts; internet access and package-manager/administrator permissions are still required. The installer generates `.env` with unique secrets, pulls and starts the containers, installs the observability columns and applies `migrations/seed/*.sql`. It is safe to re-run. Set `AGENTFLOW_HTTP_PORT` or `LOOPFORGE_MYSQL_PORT` in the environment before the first run (or edit `.env` later) when the default ports are taken.

Run it as the installation owner, including an ordinary Linux user. If that user cannot access Docker directly, the installer validates sudo and uses `sudo -n docker` consistently for Compose, schema installation and seeds. Before each operation it checks whether sudo credentials expired during a long build, prompting again in a terminal or failing with instructions in unattended mode. It does not require adding the user to the Docker group or logging out. For unattended installations run `sudo -v` first; `LOOPFORGE_YES=1` accepts installation prompts but does not bypass operating-system permissions. Without sudo or Docker access it fails immediately with instructions. Rootless Docker uses its active context's Unix socket; remote Docker contexts need a separately configured deployment because sandbox workspaces are local bind mounts.

`compose.yaml` is the published-image stack and never builds: `./start.sh`, `scripts/install.sh` and a plain `docker compose --profile platform up` all pull the GHCR release pinned there (currently `ghcr.io/tencent/loopforge:0.3.8`). The pin matches the checkout's migrations and seeds, so check out a release tag when you install. Set `LOOPFORGE_IMAGE` (for example to an `@sha256:` digest) only to override it; a value saved in `.env` takes precedence over later pin changes.

To upgrade, update the checkout first, then refresh the containers:

```bash
git fetch --tags && git checkout vX.Y.Z
./start.sh pull
```

The new `compose.yaml` brings the image pin and any new platform settings together, and `./start.sh pull` recreates the platform container when either changes. A source build from `./start.sh dev` is not rebuilt by `./start.sh pull`; run `./start.sh dev` again after updating the checkout. Running `./start.sh pull` alone keeps the pinned version. With `LOOPFORGE_IMAGE` saved in `.env`, change it to the checked-out release as well; a newer image with an older `compose.yaml` can miss settings that release needs.

Where `ghcr.io` downloads stall, set `MIRROR` to the registry host that replaces `ghcr.io`. `./start.sh` and `./start.sh pull` pull that reference and tag it back to the name in `compose.yaml`. Setting `MIRROR` trusts that host, because the result is tagged with the official name. The GitHub Release records the image digest; `docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/tencent/loopforge:<pin>` should show the same digest after `@`. The pull policy stays `missing` on install, so a local image is kept; `./start.sh pull` refreshes it from the same host. Leaving `MIRROR` unset preserves the direct `ghcr.io` pull. [ghcr.nju.edu.cn](https://doc.nju.edu.cn/books/e1654/page/ghcr) is one compatible host. Docker's `registry-mirrors` setting, including `https://docker.nju.edu.cn`, only applies to Docker Hub and cannot be used as `MIRROR`. A `LOOPFORGE_IMAGE` value that is not a `ghcr.io` name:tag is left unchanged.

```bash
MIRROR=ghcr.nju.edu.cn ./start.sh
MIRROR=ghcr.nju.edu.cn ./start.sh pull
```

`./start.sh dev` (or `LOOPFORGE_BUILD_FROM_SOURCE=1 bash scripts/install.sh`) builds this checkout as `loopforge-platform:local` from `compose.dev.yaml`. It saves `COMPOSE_FILE=compose.yaml:compose.dev.yaml` in `.env`, so later runs and plain `docker compose` commands keep using the source build until `./start.sh release`. Source builds reuse npm, Go module and Go compiler caches.

Pushing a `vX.Y.Z` tag on `main` runs the `release` workflow once the `platform` workflow has passed on that commit. The tagged `compose.yaml` must already pin `loopforge:X.Y.Z`. It publishes `ghcr.io/<owner>/loopforge` as `X.Y.Z`, `X.Y`, `latest` and `sha-<commit>`, smoke-tests the pushed digest with `./start.sh`, and creates the GitHub Release. Forks publish under their own owner; set the GHCR package visibility/access for your team before distributing the image. Images are pulled only when missing by default; use an immutable tag/digest, or set `LOOPFORGE_PULL_POLICY=always` when updating a mutable tag. Existing `.env` values take precedence; change the saved image or URL there when updating an installation. Compose waits for the API's health check with a bounded timeout before installing schema extensions and seeds.

Open the platform and create the first account in the browser; it receives platform administration. Until that account exists the login page only offers this setup step.

During initial setup, restrict the cloud security group or firewall to the operator's IP. Do not expose the first-account setup page to arbitrary Internet clients before the administrator account has been created.

The platform listens on `LOOPFORGE_BIND`, `0.0.0.0` by default, without prompting for an access URL. The installer prints the public, LAN and local addresses; on Tencent Cloud, Alibaba Cloud, AWS, GCP and Azure it reads the public IP from the provider's link-local metadata service, and elsewhere behind NAT you open the public IP yourself. `LOOPFORGE_PUBLIC_URL` is optional and only changes the printed address. Allow the HTTP port in the cloud security group/firewall, or use an HTTPS reverse proxy. Existing `.env` files are preserved; older installations without a bind setting now also listen on `0.0.0.0`. Local-only installations can set `LOOPFORGE_BIND=127.0.0.1`. Proxy WebSocket and streaming requests. Set `AGENTFLOW_SECURE_COOKIES=true` when HTTPS terminates at the proxy. Keep `AGENTFLOW_SESSION_SECRET` stable across API instances and restarts.

Enterprise authentication gateways can sign users in by header with `AGENTFLOW_TRUST_IDENTITY_HEADERS=true`. This option is off by default. The API then accepts `Staffname` / `x-staff-name` only from a loopback peer, and only when it can tell the request came from the gateway:

- Set `AGENTFLOW_IDENTITY_HEADER_SECRET` to a random value, and have the gateway send it in `X-AgentFlow-Gateway-Secret`. The gateway must overwrite, never pass through, the client's `Staffname`, `Staffid`, `x-staff-name` and `X-AgentFlow-Gateway-Secret` headers.
- Without a secret, any request carrying `X-Forwarded-For`, `Forwarded` or `X-Real-IP` is treated as a forwarded client, and its identity headers are ignored. This only protects proxies that add one of those headers: a same-host proxy that passes client headers through without adding them (for example nginx `proxy_pass` without `proxy_set_header X-Forwarded-For`), and any other process on the host, can still sign in as any user. Set the secret whenever header sign-in is on; the platform logs a warning at startup when it is missing.

### Docker sandboxes

The platform exposes a built-in default sandbox (`local`) even when the catalog SQL has not been installed. It is an in-memory fallback and does not insert database rows. An existing saved `local` profile takes priority, including its configured driver and environment. The default cannot be deleted; edit its configuration or create another profile to change how tasks run. The full installer still imports starter SOPs and the welcome project.

For native services running as an ordinary host user, default/root Docker images run with that service user's UID/GID so private workspaces remain accessible without relaxing their permissions or adding capabilities. Images that explicitly select a non-root user retain that identity; operators must arrange compatible bind-mount ownership for those images.

The Compose deployment mounts the host Docker socket into the platform container so built-in AI SOPs run out of the box. `AGENTFLOW_ISOLATED_IMAGE` points to the installed platform image: default sandboxes start separate sibling containers from that same image, with no second CLI download or image build. They do not mount the Docker socket. Custom Dockerfiles still build and cache their own images. The run workspace is mounted at `/workspace` on the Compose network. Because bind mounts are resolved by the host Docker daemon, `LOOPFORGE_DATA_DIR` (written to `.env` by `scripts/configure.sh`, default `./.data`) is mounted into the platform container at the same absolute path; workspaces and CLI state live under it. Native conversation workspaces bind-mount their conversation roots the same way, so `compose.yaml` sets `AGENTFLOW_CONVERSATION_DIR` to `${LOOPFORGE_DATA_DIR}/conversations`. Attachments and other per-user assistant state stay in `AGENTFLOW_ASSISTANT_SESSION_DIR` (`/data/assistant` in the `platform-data` volume), which is never mounted into a workspace. A platform container whose `AGENTFLOW_CONVERSATION_DIR` is unset keeps conversation roots there too, and opening a conversation workspace then fails with `mkdirat ... read-only file system`.

Access to the Docker socket is equivalent to root on the host. Two safeguards follow from that: the platform runs every task in a sandbox container, including SOPs configured for the `local` driver (`AGENTFLOW_LOCAL_SANDBOX=docker`), and only platform administrators can point a repository at a host source path. Even so, anyone who can run SOPs can execute code in containers on this host, so only give accounts to people you trust and do not expose the service to untrusted networks. To run without the socket, remove the `docker.sock` volume and the `AGENTFLOW_LOCAL_SANDBOX` setting from `compose.yaml`, then connect a separate Docker-capable worker or execution machine for AI SOPs; the script-only welcome SOP then runs inside the platform container.

### Slow networks: clone and image build limits

Cloning a repository and building a custom sandbox image report progress while they work, and their errors keep a bounded tail of git or Docker output (at most 20 lines / 16 KiB). On the platform host the progress is recorded on the run. Execution-machine clones also write progress to the runner's log. Workspace-opening requests forward live output to the platform and chat through the same command observer; upgrade both platform and runner for this protocol. Progress is emitted on the first line, at most every 10 seconds thereafter, and with the final output when the command exits. A step that prints nothing for its stall window is treated as hung and stopped. A step that keeps printing may be slow but is stopped only by a total limit. All settings below take Go durations such as `90s` or `20m`; `0` turns that command limit off. Each host reads its own environment, so set the clone limits on execution machines too.

| Setting | Default | Applies to |
|---|---|---|
| `AGENTFLOW_GIT_CLONE_STALL_TIMEOUT` | `5m` | `git clone` with no output. Not applied when the platform clones from a repository's local source path, whose final repack is silent. |
| `AGENTFLOW_GIT_CLONE_TIMEOUT` | off on the platform, `10m` on execution machines | Whole `git clone`. Execution machines keep their earlier fixed 10 minute limit as the default. |
| `AGENTFLOW_DOCKER_BUILD_STALL_TIMEOUT` | `10m` | `docker build` with no output. A quiet `RUN` step (a long compile) counts as no output. |
| `AGENTFLOW_DOCKER_BUILD_TIMEOUT` | off | Whole `docker build`. |

A clone that is stopped this way is not retried, since a retry would wait on the same network. When the execution host reaches GitHub or Debian mirrors slowly, point git at a proxy (`git config --global http.proxy`) and use a nearby package mirror in the Dockerfile rather than raising these limits.

### Git repositories over SSH

Use Git SSH for GitLab.com, self-managed GitLab, Gitea, Gogs, GitHub, and other Git hosts that offer this transport. In **Project settings → Code repositories** or **Quick access**, enter the clone address, such as `git@gitlab.com:group/repo.git` or `ssh://git@git.example.org:2222/group/repo.git`, and select the existing default branch. GitHub HTTPS continues to use GitHub authorization or a PAT.

1. The deployment administrator creates a dedicated OpenSSH `known_hosts` file. Verify the host keys against the provider's published fingerprints or with the server administrator before adding them. Use exact hostnames or IP addresses, with `[host]:port` for a nondefault port. Wildcards and hashed entries do not grant access. For a trusted team deployment, add internal hosts explicitly to this same file.
2. Set `AGENTFLOW_GIT_SSH_KNOWN_HOSTS` to the file's absolute path on the platform, workers and execution machines. For Compose, put it under the existing `LOOPFORGE_DATA_DIR` bind mount and set the absolute path in `.env`. Install Git and the OpenSSH client on execution machines and in custom sandbox images. Upgrade execution machines to Runner **0.9.3** or later. Docker sandbox commands need the sandbox and execution process to share the Linux kernel for the signing agent socket. With a Docker VM separate from a native runner, use managed checkout and delivery, or use a native sandbox for interactive Git commands.
3. Generate dedicated SSH keys with no passphrase for unattended execution. Add each public key to your hosting account and grant access to the repositories it needs. Grant write access if the run must push. Under **Personal settings → Git SSH keys**, add one private key per SSH host, such as `gitlab.com` or `git.example.org:2222`. An omitted port means 22. Edit that host's entry to replace its key.

Repositories automatically use the current user's key for their SSH host and port; repositories on the same host and port share that key. The repository form only shows a red warning when that host has no personal key. This reminder does not block saving the repository or completing setup; credentials and host trust can be configured before the first Git operation. There is no repository-specific or default fallback key. Checkout and SOP execution use the responsible user's keys. Conversation submission and the workspace terminal use the requesting user's keys. The temporary signing agent closes when the command or terminal ends. A terminal's agent never lives in the shared workspace: with the `bwrap` driver it sits in a private directory bound only into that terminal, and with the `local` driver (no isolation) it stays outside the workspace in a 0700 directory. Docker and seatbelt workspaces share one filesystem view among all their processes, so their terminals get no personal SSH key; push from there with the platform's submit action. `git@github.com:` and `ssh://git@github.com/` addresses are saved as GitHub repositories (HTTPS + token, automatic pull requests) unless `provider` is explicitly `ssh`. To rotate a key, replace it in settings and revoke the old public key at the host.

SSH handles clone, fetch and push. A conversation delivery returns the branch and commit SHA. You can optionally add a repository web URL to open the host and create a PR/MR manually. Issue import and PR/MR automation need a hosting API credential. To automate those actions, add a hosting-specific MCP in **Settings → Assets → MCP**, give it its own scoped token, and attach it to the relevant agent or workflow. [CNB does not support Git SSH](https://docs.cnb.cool/en/guide/git-access.html); it uses HTTPS with a CNB access token, which is outside this integration.

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

A platform administrator can share one CodeBuddy API Key under **Platform administration → Shared CodeBuddy Key** (平台共享 CodeBuddy Key): pick the network environment that issued it and the models members may use (the first is the default). Members without a key of their own then chat and run SOPs on it, limited to those models; a role configured with another model runs on the default and the run says so. Members with their own key keep using it with any model. Scheduled tasks, clarification, reports, bots and run evaluation still need the runner's own key. A scheduled task uses the key for its selected CLI and does not borrow the shared CodeBuddy key. The key is stored in the database like personal keys and is never shown again after saving. `AGENTFLOW_ASSISTANT_SPONSOR_LOGIN`, which lent one account's key limited to `deepseek-v4.1-flash`, still applies while no shared key is configured.

Configure provider credentials in the platform UI, not the host's global CLI configuration. Credentials are bound to the responsible actor by the upstream runtime. The home composer and SOP roles each choose a built-in CLI independently. Select the executor beside the home model selector; its API key (and optional Codex/Claude base URL) comes from your personal settings. Changing the executor preserves the draft; saved conversations record the executor used. Main Bot writes still pass through the platform tool gateway and its approval checks. Requirement clarification and notifications continue to use CodeBuddy. A scheduled assistant task can select any supported CLI and runs as an ordinary chat turn; Codex and Claude Code still need that account's own credential. Live model tests are opt-in; set `AGENTFLOW_LIVE_TESTS=1` only for the acceptance suites that need to load local credentials.

## Slow workspace opening and restricted networks

Opening a chat workspace reports sandbox/image preparation, repository cloning,
branch checkout and initialization in the chat. Git transfer output includes its
reported bytes and speed. The same bounded, redacted output is retained in the
run events and logs; preparation failures also appear in `runs_get.problems`,
including workspaces with no SOP nodes. The run stays `waiting` because it is an
idle workspace, while its latest `workspace.opening.*` event records readiness
or failure separately.

Configure these Go-duration values on the process that executes the work (the
local API/worker or the connected runner), then restart that process:

| Variable | Default | Scope |
| --- | --- | --- |
| `AGENTFLOW_WORKSPACE_BUILD_TIMEOUT` | `15m` | Sandbox acquisition, including image build |
| `AGENTFLOW_WORKSPACE_CLONE_TIMEOUT` | `10m` | Each repository clone, including reference-cache fallback |
| `AGENTFLOW_WORKSPACE_BRANCH_TIMEOUT` | `2m` | Each repository's branch preparation |
| `AGENTFLOW_WORKSPACE_PREPARE_TIMEOUT` | `5m` | Runner workspace initialization scripts |

These phase deadlines apply in addition to the command limits above: the first
deadline or stall window reached stops the command. Setting a command limit to
`0` does not disable the opening phase deadline. For example, to allow a runner
clone up to 20 minutes, set both `AGENTFLOW_WORKSPACE_CLONE_TIMEOUT=20m` and
`AGENTFLOW_GIT_CLONE_TIMEOUT=20m` on that runner. Ordinary task checkouts retain
the configurable `AGENTFLOW_GIT_CLONE_TIMEOUT` limit (10 minutes by default).
Invalid or nonpositive workspace phase values use the default. Canceling the opening request also
cancels its runner request and active Git/build command. Upgrade both platform
and runner to receive live runner progress and cancellation; older runners
still return their final result and retain their previous preparation timeout.

A failed open retains its workspace binding and completed checkouts. Retry the
current workspace without `new=true`; this avoids creating a second run and
recloning repositories that already completed. A missing requested base branch
still abandons a newly created workspace so its branch configuration can be
corrected. Git cannot resume an interrupted pack download: an empty incomplete
clone is retried, but local files, refs and indexes are preserved. For local
execution, an administrator-configured repository `localPath` alongside
`cloneUrl` supplies the existing `--reference --dissociate` cache. It must be
readable on the execution host. Docker reuses completed image layers. This does
not introduce a shared cache across users' credentials.

For restricted networks:

- Configure `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`, or Git's `http.proxy`,
  for the service account performing the clone. A proxy on the host loopback
  address is not automatically reachable from a container or remote runner.
- Configure registry access/proxies on the Docker daemon separately. Package
  downloads inside a Dockerfile use that build's network and package sources;
  the platform's Git proxy does not configure `apt`, npm or pip for it.
- In the project Dockerfile, use your organization's approved apt mirror and
  package indexes. For Debian images, update the appropriate `URIs` in
  `/etc/apt/sources.list.d/debian.sources` (or entries in `/etc/apt/sources.list`
  on older images) before `apt-get update`. Keep signature verification enabled.
  Prebuild a reusable base image when dependencies are large.

An existing workspace freezes its Dockerfile content and additional files.
Opening it after the project Dockerfile changes shows a warning; retrying does
not replace that environment. Save or push work first, then explicitly open a
new workspace to use the updated Dockerfile or mirror configuration.

## Observability schema

After the base tables have been created, run `bash scripts/install-schema-extensions.sh` for the Compose database. It installs absent `run_telemetry.adapter` and `run_logs.level` columns and their indexes from upstream migration `001_run_obs_generated_columns.sql`, then replays the `INSERT IGNORE` starter catalog. Both steps can be repeated without touching existing rows. The installer runs it before printing the address, and `./start.sh pull` runs it again after recreating containers, so an older data volume upgraded to a newer image picks up the columns the new code reads. Application startup does not run data migrations.

For an external MySQL deployment, review that migration and check column existence before applying its corresponding statements. Stored generated columns can rebuild large existing tables; schedule the operation accordingly.

### Which migrations an upgrade needs

`migrations/` holds three kinds of script. Check the release notes for the files a given version adds, then read the script's own header before running it.

| Kind | Example | What an operator does |
| --- | --- | --- |
| Already applied by the installer and `./start.sh pull` | `001_run_obs_generated_columns.sql`, `migrations/seed/*.sql`, `031_generic_backend_openssh.sql` | Nothing; the script above covers it |
| Must run once when upgrading | `009_bot_interactions_utf8mb4.sql` (`CONVERT TO` an existing table), new tables and columns added by hand | Run it on the database after reviewing prerequisites |
| Backfills and one-time rewrites of existing rows | `016_public_optional_repositories.sql`, `021_assistant_session_sources.sql` | Run it if the history matters; skipping it changes no schema but leaves old rows unconverted |

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

Run `python3 scripts/test-install.py -v` for installer regression tests covering sudo selection, direct Docker access, unattended permission failures, repeated installation, image mode and access URLs. With a locally built platform image, `LOOPFORGE_INSTALL_TEST_IMAGE=your-image bash scripts/test-install-live.sh` verifies real root and UID 65534 installations, non-loopback HTTP access, first-account creation and repeated installation in isolated Compose projects on random ports. This live test requires a Linux root test runner to drop privileges; it removes its own containers and volumes afterwards. To also exercise real sudo inside a disposable UID 1000 installer container, build `scripts/testdata/install-sudo.Dockerfile` with `--build-arg LOOPFORGE_TEST_BASE=your-image`, then pass that test image as `LOOPFORGE_INSTALL_SUDO_TEST_IMAGE`. The production installer itself does not require root.

From the repository root, run `bash scripts/test-container.sh` after installing `e2e/` dependencies and Chromium. It builds the production image, creates an isolated Compose project and database on ports 8093/33317, provisions test accounts, explicitly applies the observability baseline and public catalog, checks bundled CLIs and cross-platform runner/gate binaries, and runs the public browser suite. Its containers and volumes are removed afterwards. Override `LOOPFORGE_CONTAINER_TEST_PORT` and `LOOPFORGE_CONTAINER_MYSQL_PORT` when those ports are in use. Model calls are disabled for this clean-install check.
