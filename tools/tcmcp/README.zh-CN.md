# 腾讯云 MCP Initializr

[English](README.md) | 简体中文

MIT。Copyright (C) 2026 Tencent。本目录是 LoopForge 的可选工具，适用仓库根目录 [`LICENSE`](../../LICENSE)。

类似 [start.spring.io](https://start.spring.io/)：在页面上勾选腾讯云产品与**真实资源**，下载一个精简的 Python MCP 项目。

资源 ID 写入生成项目的 `config.yaml`，工具入参只有别名 enum（`prod` / `api` / `cache`）。右侧 Schema Inspector 预览的 `tools/list` 与 zip 里注册的工具同源，避免一次挂上过多工具让模型幻觉。

支持的产品：Redis、CDB、TDSQL-C、TKE（TKEX）、CLS、COS。一期只走只读 API；COS 仅列桶、列对象和查询对象元数据，不下载对象内容。

`loopforge install` **不会**安装本工具。在本目录运行，或拉取已发布镜像。

## 本地运行

需要 Python 3.11+，以及用于前端构建的 Node.js 20+。

```bash
cd tools/tcmcp
python3 -m venv .venv && source .venv/bin/activate
pip install -e '.[dev]'
cd web && npm install && npm run build && cd ..
python -m app.main
```

打开 http://127.0.0.1:8088 。默认只监听本机。跨域只放行 Vite 开发源 `http://127.0.0.1:5173`；需要额外源时设 `TCMCP_CORS_ORIGINS`（逗号分隔）。开发时也可以分开跑：

```bash
python -m app.main          # API :8088
cd web && npm run dev       # UI :5173，代理 /api
```

## Docker

LoopForge 打 `vX.Y.Z` tag 发版时，会把本镜像推到 GHCR（版本号与 CLI 相同）：

```bash
docker pull ghcr.io/tencent/loopforge-tcmcp:0.2.0
docker run --rm -p 127.0.0.1:8088:8088 ghcr.io/tencent/loopforge-tcmcp:0.2.0
```

在本目录本地构建（Node 打前端，Python 跑 API 并托管 `web/dist`）：

```bash
docker build -t tcmcp .
docker run --rm -p 127.0.0.1:8088:8088 tcmcp
```

容器内监听 `0.0.0.0:8088`，用 `-p` 决定对外暴露到哪。改端口用 `-e TCMCP_PORT=9000`，
需要额外跨域来源用 `-e TCMCP_CORS_ORIGINS=https://your-host`。

这个服务生成 zip 和一键安装链接，云 API 密钥仅在单次资源查询请求里透传，不落盘也不写进镜像。
一键安装包包含所选资源信息，服务端临时保留 24 小时；密钥在用户终端输入，仅写入用户本机客户端配置。
放到本机以外时，前面要加带 TLS 和认证的网关。

## 工作流

1. 填项目名、target 别名、地域，以及仅用于当次请求的云 API 密钥（**不写入 zip**）。
2. 勾选产品，拉取资源，勾选实例 / 日志主题，可改别名。
3. 右侧查看每个工具的 JSON Schema 与 enum；可关掉单个工具。
4. 点击「生成安装命令」，选择 Cursor / CodeBuddy / Codex / WorkBuddy，将命令粘贴到终端运行。
5. 在终端输入 SecretId / SecretKey（输入内容正常显示），自动下载项目、创建虚拟环境、安装依赖、检查 MCP 并合并客户端配置。
6. 在客户端刷新 MCP 或重启；如出现信任提示，在客户端确认。安装页仍可下载 ZIP 或复制 Docker 部署命令。

## curl 一键安装

在页面选择资源后，每份配置生成独立安装 ID，有效期 24 小时。页面直接提供完整命令：

```bash
curl -fsSL 'http://127.0.0.1:8088/codebuddy?project=<安装ID>' | bash
curl -fsSL 'http://127.0.0.1:8088/cursor?project=<安装ID>' | bash
curl -fsSL 'http://127.0.0.1:8088/codex?project=<安装ID>' | bash
curl -fsSL 'http://127.0.0.1:8088/workbuddy?project=<安装ID>' | bash
```

也可以使用短入口，脚本会通过终端提示输入页面给出的安装 ID：

```bash
curl -fsSL http://127.0.0.1:8088/codebuddy | bash
```

支持 macOS / Linux，需 Bash、Python 3.11+（含 venv / pip）及访问 Python 包索引的网络。
原生 Windows 暂不支持；WSL 安装只修改 WSL 用户配置。
不需要 sudo。项目装到 `~/.local/share/tcmcp/install-*/<项目名>`，依赖安装完成后才写入客户端。

| 客户端 | 默认用户级配置 |
| --- | --- |
| Cursor | `~/.cursor/mcp.json` |
| CodeBuddy | `~/.codebuddy/mcp.json` |
| Codex | `${CODEX_HOME:-~/.codex}/config.toml` |
| WorkBuddy | `~/.workbuddy/mcp.json` |

服务名为 `tcmcp-<项目名>`。重复安装替换同名服务，保留其他 MCP 和客户端设置。
每次安装都会生成相邻的安装前快照 `*.bak-<随机ID>`，即使此前没有配置文件也会生成空配置快照；配置和快照权限为 `0600`。
安装完成后终端会打印该版本的 `rollback.sh` 命令。执行回滚时，原配置恢复前还会保留一份 `*.before-rollback-<随机ID>`。
如果安装后客户端配置又被修改，脚本会拒绝自动覆盖，提示人工合并。JSON 配置支持注释和尾逗号，写回标准 JSON；Codex TOML 保留其他设置与注释。
旧安装目录保留，方便恢复备份后继续运行；确认新版本可用后可手动清理旧目录。
已有配置无法解析时停止，不覆盖原文件。

可通过环境变量覆盖行为（先 `export`，再执行管道命令）：

- `TCMCP_PYTHON`：Python 3.11+ 可执行文件路径。
- `TCMCP_INSTALL_ID`：短入口使用的安装 ID，无终端环境必须提供。
- `TENCENT_SECRET_ID` / `TENCENT_SECRET_KEY`：免交互输入密钥；仅写入本机配置，不上传到生成服务。
- `TCMCP_INSTALL_DIR`：本机安装目录。
- `TCMCP_CLIENT_CONFIG`：指定客户端配置路径，适用于非默认安装位置。

服务端设置：

- `TCMCP_PUBLIC_URL`：外部访问地址，例如 `https://mcp.example.com`，反向代理后建议配置，脚本以此下载安装包。
- `TCMCP_INSTALL_STORE`：安装包目录，默认系统临时目录下的 `tcmcp-installations`。多 worker / 多实例需共享此目录；容器重建后仍需使用旧链接时挂载持久卷。
- 最多保留 200 份有效安装包，单包最大 10 MiB；创建新包时清理过期包，过期链接不能下载。无后续请求时可由系统定期清理过期临时文件。

安装链接可下载所选资源配置，请只分享给可信人员。对外部署请使用 HTTPS，网关同时转发四个脚本入口和 `/api/installations`。
如网关要求认证，应确保终端也能完成认证；浏览器登录态不会自动传给 curl 或 Python 下载器。

配置格式依据：[Cursor MCP](https://prod.cursor.com/docs/mcp)、[CodeBuddy 目录说明](https://www.codebuddy.cn/docs/cli/codebuddy-dir)、[Codex MCP](https://developers.openai.com/codex/mcp)、[WorkBuddy MCP](https://staging.codebuddy.cn/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/MCP-Guide)。

## 生成项目

```bash
export TENCENT_SECRET_ID=AKID...
export TENCENT_SECRET_KEY=...
pip install -e .
python -m tcmcp_server --config ./config.yaml
```

把 `mcp.json` 片段拷到 Cursor。生成的 zip 内有 CAM 草稿 `deploy/cam-policy.json`（把 `$uin` 换成主账号 UIN）。该文件不在本源码树中。

## 测试

```bash
pytest
```
