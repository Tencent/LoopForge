# Deployment

## Native development

`bash scripts/dev.sh` explicitly configures local secrets, starts only this checkout's MySQL service, builds the platform, provisions the `admin` account, installs the missing observability generated columns through an explicit script, applies `migrations/seed/*.sql`, and starts the server on port 8090. Existing catalog definitions are preserved by `INSERT IGNORE`. Keep `.env`, `.local/` and database volumes outside version control.

For a native installation, use Node.js 22+ on the API host and install the CLI you plan to use. Example with Codex:

```bash
npm install --global @openai/codex@0.162.1
codex --version
```

Configure the CLI's credential in personal settings. If you select CodeBuddy, use its network environment and put its binary on PATH or set `AGENTFLOW_CODEBUDDY_BIN`. The production image bundles Codex, DeepSeek Harness and, by default, CodeBuddy. If an on-demand CLI is missing, the conversation shows the installation action. SOP sandboxes that use the published image share the first-use installation for Claude Code, OpenCode, Kimi Code and Pi. Connected runners manage their own CLI installations.

WeCom notifications require `wecom-cli` on the platform service's PATH. The platform image built from this Dockerfile includes `@wecom/cli@1.3.4`; published images gain it with the next release. For a native installation, install it separately with `npm install --global @wecom/cli@1.3.4`, ensure system CA certificates are installed, and verify `wecom-cli --version` as the service user. Configuring a WeCom bot is still required to send notifications.

Set `AGENTFLOW_MANAGED_CLI_ROOT` to a persistent absolute directory to enable the daily CLI updater on platform processes that execute runs (`worker` or `all`). By default it checks Claude Code, CodeBuddy, and Codex. Set `AGENTFLOW_MANAGED_CLIS` to the full comma-separated list you want to update, for example `claude-code,codebuddy,codex,pi`. The additional provider IDs are `agy`, `cursor`, `dsh`, `kimi`, `opencode`, and `pi`. The updater ignores empty entries, including trailing commas. A list with no names uses the default, and any unknown ID rejects the update. This list applies to scheduled checks, the bulk CLI update command, and managed CLI updates on Runners. Existing installations and rollback remain available. Set the same list on API and worker processes that share a managed root. The API uses the list when resolving CodeBuddy and workers claim the daily check once per root.

The updater checks upstream metadata daily and downloads an eligible release when needed. It verifies npm package integrity using Subresource Integrity (SRI) before running npm lifecycle scripts. Antigravity archives are checked against the official manifest's SHA512. The official Cursor installer supplies a versioned URL without a content checksum. Cursor download integrity relies on TLS and a matching strong ETag, an opaque identifier from the archive server. [Cursor install script](https://cursor.com/install)

Workers need access to the sources for the selected CLIs and can configure `HTTPS_PROXY` when needed. The updater reports download failures per provider.

All candidates pass a 24-hour observation window and a version and CLI argument check before activation. The updater keeps installed versions on disk for rollback and workflow pins, so storage use grows as it installs releases. Before enabling additional providers, back up the managed root if you plan to roll back the platform release. Older platform versions accept inventories containing the original three providers. Restore the compatible backup at the original absolute path to preserve recorded executable paths. After a CLI rollback, automatic updates wait for a release newer than the rejected version.

For native execution, the platform prefers explicit binary overrides, then the managed inventory. Platform-owned native workflows pin the inventory within their managed root. A worker that adopts a run can reuse its pin when it accesses the same persistent managed directory at the same absolute path with compatible binaries. Workers with separate roots resolve the CLI from their own inventory.

The updater keeps observations of Cursor and Antigravity releases across daily checks so each release can age even while the vendor keeps publishing. Docker sandboxes use the CLI supplied by their image or sandbox installer. ZCode uses the executable supplied by the deployment.

A conversation workspace on a custom Dockerfile without Node.js, npm or Python 3 uses the platform's own copies. On first use the platform copies its Node.js, npm and a relocatable Python into `$LOOPFORGE_DATA_DIR/cli/toolchain/<build>/`, plus each bundled CLI (CodeBuddy, Codex, DeepSeek Harness) the first time a sandbox needs it, and mounts the directory read-only at the end of the sandbox `PATH`. Tools the image ships take precedence. Only glibc images of the platform's architecture use it; Alpine (musl) images need their own Node.js, npm and Python 3. A new platform build creates a new directory and leaves the previous ones in place: after an upgrade, delete the other `cli/toolchain/<build>/` directories (about 200 MB each plus the CLIs copied into them).

Stop the foreground service with Ctrl-C. Start it again with the same command. MySQL data stays in its Compose volume. `docker compose stop mysql` stops the database without deleting it.

Use `AGENTFLOW_MYSQL_DSN` for your own MySQL deployment. A fresh account command or server connection creates missing schema objects. Apply existing-installation data migrations individually after reviewing their prerequisites; do not blindly replay all historical migrations against a new database.

## Choosing an executor

- The model pickers for requirement clarification, Bot replies, SOP notification reports, run evaluation, and scheduled AI tasks accept models from the supported CLIs. API clients can send `codex:<model>`, `claude-code:<model>` or another CLI-prefixed model ID. Legacy bare model IDs remain CodeBuddy IDs. An omitted model or bare `default` uses the execution account's personal default when available. The existing CodeBuddy default takes precedence in the fallback order. A concrete model is passed unchanged to its CLI.
- In **Personal configuration**, add Git credentials for each repository host and executor credentials for each CLI you use. Git selects credentials by the repository protocol, host, and port. The first configured executor becomes the personal default. You can change it and choose a default model, reasoning effort, and chat approval mode. CodeBuddy accepts an optional Base URL alongside its network environment. Leave the URL blank to use the environment's official endpoint. A custom CodeBuddy URL on a remote execution machine requires Runner 0.11.2 or later.
- Clarification uses the assignee's credentials, falling back to the project owner when unassigned. Bot replies use the bound sender, reports use the responsible account on the run, and evaluation uses the requesting account. Assisted approval uses the turn's CLI and selected model.
- SOP roles and static previews can each select a CLI. CodeBuddy remains the default for newly created roles and static previews. To use a built-in template with another CLI, copy it into a project and update its roles, or edit the template as a platform administrator.
- Set `AGENTFLOW_BASELINE_ALIGN_EXECUTOR=codex` on the platform to run the scheduled baseline conflict resolver with Codex. The setting takes a built-in executor ID, including `claude` for Claude Code. The resolver defaults to CodeBuddy when this setting is empty. New runs record the configured executor in their snapshot.
- To omit CodeBuddy from a source build, pass `--build-arg CODEBUDDY_PACKAGE=` to Docker or prefix the development Compose build with `CODEBUDDY_PACKAGE=`. Use that image as `AGENTFLOW_ISOLATED_IMAGE` for platform sandboxes. Without a shared image, set `AGENTFLOW_SKIP_CODEBUDDY_INSTALL=1` for the built-in fallback Dockerfile. Custom Dockerfiles manage their own packages. With that flag the default managed CLI list also omits CodeBuddy. Set `AGENTFLOW_MANAGED_CLIS` to manage an explicit list of providers.
- Tracker MCP authentication uses explicit catalog headers or the deployment's `AGENTFLOW_MCP_TOKEN`. Project-scoped trackers use their own MCP authorization headers.
- Set `AGENTFLOW_GONGFENG_HOST` to the managed Git host (hostname and optional HTTPS port) to enable its repository and merge request links and token checks. Gongfeng OAuth also needs its application credentials and callback. Existing OAuth endpoint overrides still take precedence.
- Set `AGENTFLOW_TAPD_HOST` to a deployment-specific TAPD web host when needed. Generated TAPD work item links use `www.tapd.cn` by default.

## Container installation

```bash
./start.sh                        # install or start in the background (same as bash scripts/install.sh)
./start.sh logs                   # follow platform logs
./start.sh stop                   # stop; ./start.sh down removes containers but keeps data volumes
./start.sh restart                # restart the platform container
./start.sh pull                   # refresh images; times ghcr.io against ghcr.nju.edu.cn
MIRROR=ghcr.io ./start.sh         # install from ghcr.io without the speed test
MIRROR=ghcr.nju.edu.cn ./start.sh # force that host instead of the speed test
./start.sh dev                    # build this checkout instead (compose.dev.yaml)
./start.sh release                # switch a source build back to the published image
```

The installer checks Docker, Compose, Python 3 and curl. It offers to install missing Python/curl packages using apt, dnf or yum on Linux (Homebrew on macOS), Docker using its official installer, and the missing Linux x86_64/arm64 Compose plugin using the pinned official release with SHA-256 verification. A plugin is installed for the active Docker user, or system-wide when Docker requires sudo. macOS Compose comes from Docker Desktop. Set `LOOPFORGE_YES=1` to accept installation prompts; internet access and package-manager/administrator permissions are still required. The installer generates `.env` with unique secrets, pulls and starts the containers, installs the observability columns and applies `migrations/seed/*.sql`. It is safe to re-run. Set `AGENTFLOW_HTTP_PORT` or `LOOPFORGE_MYSQL_PORT` in the environment before the first run (or edit `.env` later) when the default ports are taken.

Run it as the installation owner, including an ordinary Linux user. If that user cannot access Docker directly, the installer validates sudo and uses `sudo -n docker` consistently for Compose, schema installation and seeds. Before each operation it checks whether sudo credentials expired during a long build, prompting again in a terminal or failing with instructions in unattended mode. It does not require adding the user to the Docker group or logging out. For unattended installations run `sudo -v` first; `LOOPFORGE_YES=1` accepts installation prompts but does not bypass operating-system permissions. Without sudo or Docker access it fails immediately with instructions. Rootless Docker uses its active context's Unix socket; remote Docker contexts need a separately configured deployment because sandbox workspaces are local bind mounts.

`compose.yaml` is the published-image stack and never builds: `./start.sh`, `scripts/install.sh` and a plain `docker compose --profile platform up` all pull the GHCR release pinned there (currently `ghcr.io/tencent/loopforge:0.4.0`). The pin matches the checkout's migrations and seeds, so check out a release tag when you install. Set `LOOPFORGE_IMAGE` (for example to an `@sha256:` digest) only to override it; a value saved in `.env` takes precedence over later pin changes.

To upgrade, update the checkout first, then refresh the containers:

```bash
git fetch --tags && git checkout vX.Y.Z
./start.sh pull
```

The new `compose.yaml` brings the image pin and any new platform settings together, and `./start.sh pull` recreates the platform container when either changes. A source build from `./start.sh dev` is not rebuilt by `./start.sh pull`; run `./start.sh dev` again after updating the checkout. Running `./start.sh pull` alone keeps the pinned version. With `LOOPFORGE_IMAGE` saved in `.env`, change it to the checked-out release as well; a newer image with an older `compose.yaml` can miss settings that release needs.

Where `ghcr.io` downloads stall, `./start.sh` and `./start.sh pull` time `ghcr.io` against [ghcr.nju.edu.cn](https://doc.nju.edu.cn/books/e1654/page/ghcr). Nanjing is used only when its manifest digest matches the digest just read from ghcr.io; that digest is what gets pulled, then tagged back to the name in `compose.yaml`, which trusts that host. A missing official digest, a mismatch, or a failed mirror pull stays on `ghcr.io`. `MIRROR=ghcr.io` forces the upstream registry; any other `MIRROR` host forces that registry, skips the speed test, and does not fall back. The choice is not written to `.env`. The pull policy stays `missing` on install, so a local image is kept and is not timed again; `./start.sh pull` times the registries and refreshes from the winner. The GitHub Release records the image digest; `docker image inspect --format '{{index .RepoDigests 0}}' ghcr.io/tencent/loopforge:<pin>` should show the same digest after `@`. Docker's `registry-mirrors` setting, including `https://docker.nju.edu.cn`, only applies to Docker Hub and cannot be used as `MIRROR`. A `LOOPFORGE_IMAGE` value that is not a `ghcr.io` name:tag is left unchanged.

```bash
./start.sh
MIRROR=ghcr.io ./start.sh pull
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

The Compose deployment mounts the host Docker socket into the platform container so built-in AI SOPs run out of the box. `AGENTFLOW_ISOLATED_IMAGE` points to the installed platform image: default sandboxes start separate sibling containers from that same image, with no second CLI download or image build. They do not mount the Docker socket. Custom Dockerfiles still build and cache their own images. The run workspace is mounted at `/workspace` on the Compose network. Because bind mounts are resolved by the host Docker daemon, `LOOPFORGE_DATA_DIR` (written to `.env` by `scripts/configure.sh`, default `./.data`) is mounted into the platform container at the same absolute path; workspaces and CLI state live under it. Native conversation workspaces bind-mount their conversation roots the same way, so `compose.yaml` sets `AGENTFLOW_CONVERSATION_DIR` to `${LOOPFORGE_DATA_DIR}/conversations`. Conversation workspaces in a Compose deployment use Docker sandboxes: the native (bwrap) sandbox needs user and PID namespaces, which the unprivileged platform container cannot create, and the image does not ship bubblewrap. Use a native installation or a connected execution machine for native sandboxes. Attachments and other per-user assistant state stay in `AGENTFLOW_ASSISTANT_SESSION_DIR` (`/data/assistant` in the `platform-data` volume), which is never mounted into a workspace. A platform container whose `AGENTFLOW_CONVERSATION_DIR` is unset keeps conversation roots there too, and opening a conversation workspace then fails with `mkdirat ... read-only file system`.

Docker SOP nodes using a standard CLI executor first look for the CLI on the sandbox user's PATH. When it is missing, LoopForge uses a shared installation for the platform image where available or runs the same installer as conversation workspaces in the container. The install uses the sandbox's configured proxy, and later nodes in that container reuse the CLI. In-container installation requires a writable filesystem and access to the official package source. Read-only sandboxes can use a CLI from the image or the mounted toolchain. ZCode uses the official executable supplied by the image.

Access to the Docker socket is equivalent to root on the host. Two safeguards follow from that: the platform runs every task in a sandbox container, including SOPs configured for the `local` driver (`AGENTFLOW_LOCAL_SANDBOX=docker`), and only platform administrators can point a repository at a host source path. Even so, anyone who can run SOPs can execute code in containers on this host, so only give accounts to people you trust and do not expose the service to untrusted networks. Agent CLIs also apply a bound repository's own configuration as their native rules define, including unattended SOP runs: `AGENTS.md`/`CLAUDE.md` instructions, `.codebuddy/`, `.claude/settings.json` (hooks and environment) and `.mcp.json` can start commands or MCP servers inside the sandbox. Bind only repositories whose contents you trust. To run without the socket, remove the `docker.sock` volume and the `AGENTFLOW_LOCAL_SANDBOX` setting from `compose.yaml`, then connect a separate Docker-capable worker or execution machine for AI SOPs; the script-only welcome SOP then runs inside the platform container.

### Slow networks: clone and image build limits

Cloning a repository and building a custom sandbox image report progress while they work, and their errors keep the last lines of Git or Docker output. Conversations show the current Docker build step on the platform and on connected runners. A long, quiet Dockerfile step can continue until its caller or a configured limit stops the build. All settings take Go durations such as `90s` or `20m`. `0` turns a limit off. Set the variables on the platform process or execution machine that runs the command.

Runner conversation workspace preparation follows the conversation request's lifetime. Stopping the turn or losing the runner control connection cancels preparation.

| Setting | Default | Applies to |
|---|---|---|
| `AGENTFLOW_GIT_CLONE_STALL_TIMEOUT` | `5m` | `git clone` with no output. Not applied when the platform clones from a repository's local source path, whose final repack is silent. |
| `AGENTFLOW_GIT_CLONE_TIMEOUT` | off on the platform, `10m` on execution machines | Whole `git clone`. Execution machines keep their earlier fixed 10 minute limit as the default. |
| `AGENTFLOW_DOCKER_BUILD_STALL_TIMEOUT` | off | Optional no-output limit for `docker build`. A quiet `RUN` step counts as no output. |
| `AGENTFLOW_DOCKER_BUILD_TIMEOUT` | off | Whole `docker build`. |

A clone that is stopped this way is not retried, since a retry would wait on the same network. When the execution host reaches GitHub or Debian mirrors slowly, point git at a proxy (`git config --global http.proxy`) and use a nearby package mirror in the Dockerfile rather than raising these limits.

### Git repositories over SSH

Use Git SSH for GitLab.com, self-managed GitLab, Gitea, Gogs, GitHub, and other Git hosts that offer this transport. In **Project settings → Code repositories** or **Quick access**, enter the clone address, such as `git@gitlab.com:group/repo.git` or `ssh://git@git.example.org:2222/group/repo.git`, and select the existing default branch. GitHub HTTPS continues to use GitHub authorization or a PAT.

1. GitHub (`github.com:22` and `ssh.github.com:443`) uses built-in host keys. For another host, a platform administrator opens **Platform administration → Git SSH Host Trust**, enters the hostname and optional port, and selects **Read key and review fingerprint**. The platform reads the public key through an SSH handshake subject to `AGENTFLOW_GIT_EGRESS_ALLOW_CIDRS`. For a private host, select an online personal Runner with access to the server and support for host-key discovery. You can also enter OpenSSH public keys manually, including multiple keys for a host. Check the displayed SHA256 fingerprints against the Git provider or server administrator before confirming. The registration applies across the platform to that exact host and port. Edits and removals are picked up by the next platform-managed Git operation or newly started workspace CLI or terminal session.
2. Runner **0.11.1** and later receive the matching platform entries when a task or workspace request needs Git SSH. Deployments can also point `AGENTFLOW_GIT_SSH_KNOWN_HOSTS` at a dedicated OpenSSH `known_hosts` file. For Compose, put the file under the `LOOPFORGE_DATA_DIR` bind mount and set its absolute path in `.env`. Entries in the platform file and administration page are combined. Use exact hostnames or IP addresses in the file, with `[host]:port` for a nondefault port. Platform-supplied trusted keys take precedence for the same host and port. Runner-local keys apply when the platform supplies only revocations or has no entries for that endpoint. Both sources apply explicit `@revoked` entries. The encoded entries have a 1 MiB allowance per dispatch. Runners **0.9.3** through **0.11.0** still need their own file and variable. A separate worker can read the registry from the shared database or use its own deployment file.

   If the database registry is unavailable, the platform logs the error and continues using the deployment file and built-in GitHub keys. Runner-local keys remain available under the same precedence rules. An unreadable configured `known_hosts` file still blocks Git SSH operations because it can carry revocations. The administration list marks invalid entries for removal, and an entry with a valid hostname can be repaired in place. Invalid entries contribute no trusted keys.

   Install Git and the OpenSSH client on execution machines and in custom sandbox images. Docker sandbox commands need the sandbox and execution process to share the Linux kernel for the signing agent socket. With a Docker VM separate from a native runner, use managed checkout and delivery, or use a native sandbox for interactive Git commands.
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

Docker workspaces on macOS use a Linux Runner executable for Git credentials and MCP calls. The installer chooses the architecture of the installed macOS Runner, including Intel binaries running under Rosetta. For a universal Runner on Apple Silicon it installs the arm64 helpers and adds the amd64 helpers when available. Foreground starts and the command printed by `--no-start` select the native arm64 slice, including from a Rosetta shell. On Intel Macs the installer installs the amd64 helpers. By default, it places the helpers next to the host binary. A Runner-side `AGENTFLOW_RUNNER_BINARY` template with `{os}` selects the installation and runtime path. To install both helpers, use `{arch}` and give each architecture a distinct destination. The installer saves the template for upgrades and includes it in launchd and in the startup command printed by `--no-start`. Helper paths must resolve to files separate from the macOS Runner.

The installer checks the helper destinations against the staged macOS binary and installs the required Gate MCP helpers before replacing an installed Runner. When the existing Runner has a working Docker helper, an upgrade requires a compatible helper at the new Runner's configured path before replacing the binary and restarting the service. Relative templates in the installed launchd configuration use the job's working directory. The installer and updater validate the Linux executable's format, architecture, and load segments before replacing it. Custom installs keep a compatible existing helper. A Runner built from a local checkout can build its companion from that checkout. A download URL with an `{os}` placeholder can supply both binaries when used as the Runner's download source.

When platform updates are enabled, macOS runners synchronize the helper in the background at startup. A failed version check is retried during rescans, including when an older helper remains usable. A readable, compatible local ELF or a locally configured script wrapper can be copied into a Docker workspace with executable permissions while the sync runs. Operations that need a missing helper for Git or MCP can wait up to five seconds for the initial sync. The background download continues within its own transfer timeout.

A platform administrator can share one CodeBuddy API Key under **Platform administration → Shared CodeBuddy Key** (平台共享 CodeBuddy Key): pick the network environment that issued it and the models members may use (the first is the default). Members without a key of their own then chat and run SOPs on it, limited to those models; a role configured with another model runs on the default and the run says so. Members with their own key keep using it with any model. Clarification, reports, bots and run evaluation use the personal credentials of the relevant account. Scheduled prompts follow the selected CLI's credential and sponsorship settings. The key is stored in the database like personal keys and is never shown again after saving. `AGENTFLOW_ASSISTANT_SPONSOR_LOGIN`, which lent one account's key limited to `deepseek-v4.1-flash`, still applies while no shared key is configured.

Configure provider credentials in the platform UI, not the host's global CLI configuration. Credentials are bound to the responsible actor by the upstream runtime. The home composer and SOP roles each choose a built-in CLI independently. Select the executor beside the home model selector; its API key (and optional Codex/Claude base URL) comes from your personal settings. Changing the executor preserves the draft; saved conversations record the executor used. Main Bot writes still pass through the platform tool gateway and its approval checks. A scheduled assistant task can select any supported CLI and runs as an ordinary chat turn; Codex and Claude Code still need that account's own credential. Live model tests are opt-in; set `AGENTFLOW_LIVE_TESTS=1` only for the acceptance suites that need to load local credentials.

## Observability schema

After the base tables have been created, run `bash scripts/install-schema-extensions.sh` for the Compose database. It installs absent `run_telemetry.adapter` and `run_logs.level` columns and their indexes from upstream migration `001_run_obs_generated_columns.sql`, then replays the `INSERT IGNORE` starter catalog and the built-in catalog rewrites from `031_generic_backend_openssh.sql`, `033_generic_sop_mr_node.sql`, and `038_generic_sop_host_credentials.sql` in that order. The rewrites are repeatable and apply only to matching defaults. The installer runs it before printing the address, and `./start.sh pull` runs it again after recreating containers, so an older data volume upgraded to a newer image picks up the columns the new code reads and the built-in SOP graphs the new code ships. Application startup does not run data migrations.

For an external MySQL deployment, review that migration and check column existence before applying its corresponding statements. Stored generated columns can rebuild large existing tables; schedule the operation accordingly.

### Which migrations an upgrade needs

`migrations/` holds three kinds of script. Check the release notes for the files a given version adds, then read the script's own header before running it.

| Kind | Example | What an operator does |
| --- | --- | --- |
| Already applied by the installer and `./start.sh pull` | `001_run_obs_generated_columns.sql`, `migrations/seed/*.sql`, `031_generic_backend_openssh.sql`, `033_generic_sop_mr_node.sql`, `038_generic_sop_host_credentials.sql` | Nothing; the script above covers it |
| Required upgrade migrations | `009_bot_interactions_utf8mb4.sql`, `036_git_http_credentials.sql` | Follow each migration's timing and prerequisites. [Migrating existing GitHub credentials](#migrating-existing-github-credentials) |
| Optional backfills and rewrites of existing rows | `016_public_optional_repositories.sql`, `021_assistant_session_sources.sql` | Run them if the installation needs the converted rows |

## Updating existing public SOPs

Repository binding is optional for all built-in generic SOPs. To update the original catalog instructions on an existing installation, explicitly apply:

```bash
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/016_public_optional_repositories.sql
```

This repeatable SQL changes only instruction fields that still match the original public seed. It preserves operator edits and project copies. New installations already receive these instructions from the public catalog seed; application startup does not migrate data.

On Compose installs, the installer and `./start.sh pull` add the 「发起 MR」 node to the built-in backend, frontend, and defect-fix SOPs. They apply `033_generic_sop_mr_node.sql` to add the step, then `038_generic_sop_host_credentials.sql` to update the original MR role instructions. To apply the SQL manually, run both migrations in the same order.

```bash
cat migrations/033_generic_sop_mr_node.sql migrations/038_generic_sop_host_credentials.sql \
  | docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge'
```

Migration 033 checks that the node graph and 交付汇总 position still match the shipped template. It updates the 交付汇总 prompt only when it is unedited. Migration 038 replaces the original Git authentication instruction with guidance to use a hosting CLI, a configured MCP, or `git credential fill` for API access. Both migrations preserve project copies and can be repeated. The Git credentials of the run's responsible user need push access for the new step to complete.

## GitHub credentials

Users can choose **Connect GitHub** in personal credentials or while adding a repository, copy the displayed PIN, and approve it on GitHub. The platform saves the grant to that signed-in user's existing Git credential slot. The repository picker lists repositories owned by the account, shared with it, or accessible through its organizations, supports pagination and search over loaded repositories, and copies the selected repository's real default branch into the form. PAT entry remains available.

New installations use the built-in LoopForge OAuth App. Users do not create applications, configure a Client ID, or request a PAT: choose **Connect GitHub**, copy the PIN and approve the LoopForge application on GitHub. GitHub's confirmation cannot be bypassed; device authorization is not a zero-interaction login.

Application configuration is a deployment concern and is not shown in personal credentials. Operators who need their own application can register an OAuth App, enable **Device Flow**, keep token expiration disabled until refresh support is available, and set its public Client ID with `AGENTFLOW_GITHUB_CLIENT_ID` in `.env`. No client secret is needed. The deployment environment overrides the built-in application and any database override. The administrator-only `PUT /api/auth/github/config` endpoint remains available for an explicit database override; an empty Client ID explicitly disables device authorization rather than silently restoring the default. Existing explicit configurations are preserved.

The flow requests the OAuth `repo` scope so private cloning, pushes, Issues and PR operations work. GitHub's OAuth scope covers accessible repositories; choosing a repository in LoopForge binds it to the project and does not narrow the OAuth grant. Organization SSO or third-party-app restrictions may require organization approval. See [GitHub's device authorization documentation](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow). Pending grants expire and are held only in API memory: use session affinity for `/api/auth/github/*` behind multiple API instances, and restart authorization after an API restart. Tokens and GitHub device codes are never sent to the browser.

GitHub Issue import and PR status synchronization use the project owner's Git token. Git operations and the optional GitHub MCP integration use the responsible user's token. Public reads may work without a token; private repositories and writes require repository access. GitHub MCP must be installed explicitly and sends credentials only to its official HTTPS endpoint.

Git commits created by the platform use the responsible local account name and a non-deliverable `@users.noreply.loopforge.invalid` email. A local account does not establish a corporate email or a verified GitHub identity.

### Migrating existing GitHub credentials

When upgrading from v0.4.0 to a version that reads HTTPS credentials by host, stop the old API and workers and keep MySQL running. Apply the target release's `036_git_http_credentials.sql` manually before starting the upgraded platform or running `./start.sh pull`. For Compose, use:

```bash
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/036_git_http_credentials.sql
```

The backfill copies existing GitHub personal access tokens and device-flow grants into `github.com` entries. If an installation already upgraded and a user's token is still in the legacy column, applying the backfill restores the missing entry. An existing `github.com` entry takes precedence.

Verify the credentials after upgrading. After the rollback window, run `037_retire_plain_git_token.sql` manually using the same command with the cleanup filename. It clears a legacy token only when the corresponding `github.com` entry holds the same value. If the values differ, both are kept.

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
