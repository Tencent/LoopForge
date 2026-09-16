# Tencent Cloud MCP Initializr

English | [简体中文](README.zh-CN.md)

MIT. Copyright (C) 2026 Tencent. This directory is an optional LoopForge tool; the repository root [`LICENSE`](../../LICENSE) applies.

A [start.spring.io](https://start.spring.io/)-style service: pick Tencent Cloud products and **real resources**, then download a scoped Python MCP project.

Resource IDs go into the generated `config.yaml`. Tool arguments use alias enums (`prod` / `api` / `cache`). The Schema Inspector `tools/list` preview matches the tools registered in the zip, so the model is not flooded with unused tools.

Supported products: Redis, CDB, TDSQL-C, TKE (TKEX), CLS, COS. The first release is read-only. COS lists buckets and objects and reads object metadata; it does not download object bodies.

This tool is not installed by `loopforge install`. Run it from this directory or pull the published image.

## Run locally

Requires Python 3.11+ and Node.js 20+ (for the frontend build).

```bash
cd tools/tcmcp
python3 -m venv .venv && source .venv/bin/activate
pip install -e '.[dev]'
cd web && npm install && npm run build && cd ..
python -m app.main
```

Open http://127.0.0.1:8088 . The server listens on localhost by default. CORS allows the Vite dev origin `http://127.0.0.1:5173`; set `TCMCP_CORS_ORIGINS` (comma-separated) for extra origins. For split development:

```bash
python -m app.main          # API :8088
cd web && npm run dev       # UI :5173, proxies /api
```

## Docker

LoopForge `vX.Y.Z` releases also publish this image to GHCR (same version as the CLI):

```bash
docker pull ghcr.io/tencent/loopforge-tcmcp:0.2.0
docker run --rm -p 127.0.0.1:8088:8088 ghcr.io/tencent/loopforge-tcmcp:0.2.0
```

Build from this directory (Node builds the UI; Python serves the API and `web/dist`):

```bash
docker build -t tcmcp .
docker run --rm -p 127.0.0.1:8088:8088 tcmcp
```

The container listens on `0.0.0.0:8088`. Map the port with `-p`. Use `-e TCMCP_PORT=9000` to change the port, and `-e TCMCP_CORS_ORIGINS=https://your-host` for extra CORS origins.

The service generates zips and one-click install links. Cloud API keys are used only for that listing request; they are not written to disk or the image. Install packages contain selected resource IDs and are kept for 24 hours. Keys are entered on the user machine and written only to the local client config.
When exposing this beyond localhost, put TLS and authentication in front of it.

## Workflow

1. Enter the project name, target alias, region, and a cloud API key used only for this request (**not written into the zip**).
2. Select products, list resources, select instances / log topics, and optionally rename aliases.
3. Inspect each tool JSON Schema and enum; disable unused tools.
4. Generate an install command for Cursor / CodeBuddy / Codex / WorkBuddy and paste it into a terminal.
5. Enter SecretId / SecretKey in the terminal (input is shown as typed). The script downloads the project, creates a venv, installs dependencies, checks MCP, and merges client config.
6. Refresh MCP or restart the client; confirm any trust prompt. The page can still download a ZIP or copy a Docker deploy command.

## curl one-click install

Each selection gets an install ID valid for 24 hours. The page prints a full command:

```bash
curl -fsSL 'http://127.0.0.1:8088/codebuddy?project=<install-id>' | bash
curl -fsSL 'http://127.0.0.1:8088/cursor?project=<install-id>' | bash
curl -fsSL 'http://127.0.0.1:8088/codex?project=<install-id>' | bash
curl -fsSL 'http://127.0.0.1:8088/workbuddy?project=<install-id>' | bash
```

Short entry points prompt for the install ID:

```bash
curl -fsSL http://127.0.0.1:8088/codebuddy | bash
```

macOS / Linux only. Requires Bash, Python 3.11+ (venv / pip), and network access to a Python package index.
Native Windows is not supported; WSL installs only change WSL user config.
No sudo. Projects land in `~/.local/share/tcmcp/install-*/<project-name>`. Client config is written only after dependencies install.

| Client | Default user config |
| --- | --- |
| Cursor | `~/.cursor/mcp.json` |
| CodeBuddy | `~/.codebuddy/mcp.json` |
| Codex | `${CODEX_HOME:-~/.codex}/config.toml` |
| WorkBuddy | `~/.workbuddy/mcp.json` |

The server name is `tcmcp-<project-name>`. Reinstalling replaces that server and leaves other MCP entries alone.
Each install writes a sibling pre-install snapshot `*.bak-<random-id>` (including an empty snapshot when no config existed). Config and snapshots are `0600`.
The terminal prints a `rollback.sh` command. Rollback keeps `*.before-rollback-<random-id>` before restoring.
If the client config changed after install, the script refuses to overwrite and asks for a manual merge. JSON allows comments and trailing commas and is written back as standard JSON; Codex TOML keeps other settings and comments.
Old install directories are kept until you delete them. Unreadable existing config stops the install.

Override with environment variables (`export` before the pipe):

- `TCMCP_PYTHON`: Python 3.11+ executable.
- `TCMCP_INSTALL_ID`: install ID for the short entry (required without a TTY).
- `TENCENT_SECRET_ID` / `TENCENT_SECRET_KEY`: non-interactive keys; written only to local client config, not uploaded to the generator.
- `TCMCP_INSTALL_DIR`: local install directory.
- `TCMCP_CLIENT_CONFIG`: client config path when it is not the default.

Server settings:

- `TCMCP_PUBLIC_URL`: public URL such as `https://mcp.example.com` so install scripts can download packages behind a reverse proxy.
- `TCMCP_INSTALL_STORE`: package directory (default: `tcmcp-installations` under the system temp dir). Share it across workers; mount a volume if containers must keep old links.
- At most 200 valid packages, 10 MiB each. Expired packages are removed when creating a new one.

Share install links only with people you trust. Use HTTPS for any non-local deploy, and proxy the four script entry points plus `/api/installations`.
If the gateway requires auth, the terminal must be able to authenticate; a browser session is not sent to curl or the Python downloader.

Config formats: [Cursor MCP](https://prod.cursor.com/docs/mcp), [CodeBuddy](https://www.codebuddy.cn/docs/cli/codebuddy-dir), [Codex MCP](https://developers.openai.com/codex/mcp), [WorkBuddy MCP](https://staging.codebuddy.cn/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/MCP-Guide).

## Generated project

```bash
export TENCENT_SECRET_ID=AKID...
export TENCENT_SECRET_KEY=...
pip install -e .
python -m tcmcp_server --config ./config.yaml
```

Copy the `mcp.json` snippet into Cursor. The generated zip includes a CAM policy draft at `deploy/cam-policy.json` (replace `$uin` with the account UIN). That file is not in this source tree.

## Tests

```bash
pytest
```
