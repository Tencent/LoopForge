"""Render a scoped Python MCP project into a zip."""

from __future__ import annotations

import io
import json
import re
import zipfile
from pathlib import Path

import yaml

from app.cam import cam_policy
from app.models import Selection
from app.preview import materialize

ROOT = Path(__file__).resolve().parent
RUNTIME = ROOT / "mcp_runtime"


def _slug(name: str) -> str:
    s = re.sub(r"[^a-zA-Z0-9._-]+", "-", name.strip()).strip("-._")
    return s or "tcmcp-project"


def config_dict(selection: Selection) -> dict:
    target = selection.project.target or "prod"
    region = selection.project.region or "ap-guangzhou"
    t: dict = {"description": selection.project.description}
    if selection.resources.redis:
        t["redis"] = [
            {"alias": r.alias, "id": r.id, "name": r.name, "region": r.region or region}
            for r in selection.resources.redis
        ]
    if selection.resources.cdb:
        t["cdb"] = [
            {"alias": r.alias, "id": r.id, "name": r.name, "region": r.region or region}
            for r in selection.resources.cdb
        ]
    if selection.resources.tdsqlc:
        t["tdsqlc"] = [
            {
                "alias": r.alias,
                "clusterId": r.cluster_id,
                "instanceId": r.instance_id,
                "name": r.name,
                "region": r.region or region,
                "product": "cynosdb",
            }
            for r in selection.resources.tdsqlc
        ]
    if selection.resources.tke:
        t["tke"] = [
            {"alias": r.alias, "id": r.id, "name": r.name, "region": r.region or region}
            for r in selection.resources.tke
        ]
    if selection.resources.cls:
        t["cls"] = []
        for logset in selection.resources.cls:
            t["cls"].append(
                {
                    "logsetId": logset.logset_id,
                    "logsetName": logset.logset_name,
                    "region": logset.region or region,
                    "topics": [
                        {"alias": topic.alias, "id": topic.id, "name": topic.name}
                        for topic in logset.topics
                    ],
                }
            )
    if selection.resources.cos:
        t["cos"] = [
            {"alias": r.alias, "id": r.id, "name": r.name, "region": r.region or region}
            for r in selection.resources.cos
        ]
    return {
        "server": {
            "listen": "127.0.0.1:8099",
            "endpointPath": "/mcp",
        },
        "project": {
            "name": selection.project.name,
            "description": selection.project.description,
            "target": target,
        },
        "products": list(selection.products),
        "disabledTools": list(selection.disabled_tools),
        "limits": {"maxRows": 200, "maxBytes": 262144, "queryTimeout": "30s"},
        "auth": {"tokens": ["${TCMCP_AUTH_TOKEN}"]},
        "tencent": {
            "secretId": "${TENCENT_SECRET_ID}",
            "secretKey": "${TENCENT_SECRET_KEY}",
            "region": region,
        },
        "targets": {target: t},
    }


def rewrite_app_imports(src: str) -> str:
    return src.replace("from app.", "from tcmcp_server.").replace("import app.", "import tcmcp_server.")


def build_files(selection: Selection) -> dict[str, bytes]:
    preview = materialize(selection)
    registered = set(preview["registered"])
    products = set(selection.products)
    slug = _slug(selection.project.name)
    cfg = config_dict(selection)
    files: dict[str, bytes] = {}

    files[f"{slug}/config.yaml"] = yaml.safe_dump(cfg, allow_unicode=True, sort_keys=False).encode()
    files[f"{slug}/config.example.yaml"] = files[f"{slug}/config.yaml"]
    files[f"{slug}/deploy/cam-policy.json"] = json.dumps(
        cam_policy(selection), indent=2, ensure_ascii=False
    ).encode()
    files[f"{slug}/pyproject.toml"] = _pyproject(selection).encode()
    files[f"{slug}/README.md"] = _readme(selection, preview).encode()
    files[f"{slug}/mcp.json"] = _mcp_json(selection).encode()
    files[f"{slug}/run.sh"] = _run_script().encode()
    files[f"{slug}/Dockerfile"] = _dockerfile().encode()
    files[f"{slug}/.dockerignore"] = _dockerignore().encode()

    # Shared catalog / models / preview — same source the Initializr uses.
    for name in ("catalog.py", "models.py", "preview.py", "regions.py"):
        text = rewrite_app_imports((ROOT / name).read_text())
        files[f"{slug}/src/tcmcp_server/{name}"] = text.encode()

    handler_keep = {"__init__.py", "dispatch.py", "ping.py"}
    if "redis" in products:
        handler_keep.add("redis.py")
    if "cdb" in products:
        handler_keep.update({"cdb.py", "dbbrain.py"})
    if "tdsqlc" in products:
        handler_keep.update({"tdsqlc.py", "dbbrain.py"})
    if "tke" in products:
        handler_keep.add("tke.py")
    if "cls" in products:
        handler_keep.add("cls.py")
    if "cos" in products:
        handler_keep.add("cos.py")

    for path in RUNTIME.rglob("*"):
        if not path.is_file():
            continue
        if "__pycache__" in path.parts or path.suffix == ".pyc":
            continue
        rel = path.relative_to(RUNTIME)
        if rel.parts[0] == "handlers" and rel.name not in handler_keep:
            continue
        dest = f"{slug}/src/tcmcp_server/{rel.as_posix()}"
        files[dest] = path.read_bytes()

    files[f"{slug}/src/tcmcp_server/generated.json"] = json.dumps(
        {"registered": sorted(registered), "products": list(selection.products)},
        indent=2,
    ).encode()
    return files


def build_zip(selection: Selection) -> bytes:
    files = build_files(selection)
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zf:
        for name, data in sorted(files.items()):
            zf.writestr(name, data)
    return buf.getvalue()


def _pyproject(selection: Selection) -> str:
    name = _slug(selection.project.name)
    dependencies = [
        "mcp>=1.12.0,<2",
        "pyyaml>=6.0.2",
        "pydantic>=2.10.0",
        "tencentcloud-sdk-python>=3.0.1400",
    ]
    if "cos" in selection.products:
        dependencies.append("cos-python-sdk-v5>=1.9.38")
    dependency_lines = "\n".join(f'    "{dependency}",' for dependency in dependencies)
    return f"""[project]
name = "{name}"
version = "0.1.0"
description = {json.dumps(selection.project.description, ensure_ascii=False)}
requires-python = ">=3.11"
dependencies = [
{dependency_lines}
]

[project.scripts]
{json.dumps(name)} = "tcmcp_server.server:main"

[build-system]
requires = ["setuptools>=68"]
build-backend = "setuptools.build_meta"

[tool.setuptools.packages.find]
where = ["src"]
"""


def _mcp_json(selection: Selection) -> str:
    name = _slug(selection.project.name)
    body = {
        "mcpServers": {
            name: {
                "command": "/bin/bash",
                "args": [f"/absolute/path/to/{name}/run.sh"],
                "env": {
                    "TENCENT_SECRET_ID": "",
                    "TENCENT_SECRET_KEY": "",
                },
            }
        }
    }
    return json.dumps(body, indent=2)


def _run_script() -> str:
    return """#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PYTHON="${TCMCP_PYTHON:-python3}"
VENV="${TCMCP_VENV:-$ROOT/.venv}"
MARKER="$VENV/.tcmcp-installed"

if ! command -v "$PYTHON" >/dev/null 2>&1; then
  echo "tcmcp: Python 3.11+ is required; set TCMCP_PYTHON if it is installed elsewhere." >&2
  exit 127
fi

if ! "$PYTHON" -c 'import sys; raise SystemExit(sys.version_info < (3, 11))'; then
  echo "tcmcp: Python 3.11+ is required." >&2
  exit 1
fi

if [[ ! -x "$VENV/bin/python" ]]; then
  echo "tcmcp: creating virtual environment at $VENV" >&2
  "$PYTHON" -m venv "$VENV"
fi

if [[ ! -f "$MARKER" || "$ROOT/pyproject.toml" -nt "$MARKER" ]]; then
  echo "tcmcp: installing dependencies (first start may take a moment)" >&2
  "$VENV/bin/python" -m pip install -e "$ROOT" >&2
  touch "$MARKER"
fi

exec "$VENV/bin/python" -m tcmcp_server --config "$ROOT/config.yaml" "$@"
"""


def _dockerfile() -> str:
    return """FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 \\
    PYTHONUNBUFFERED=1

WORKDIR /app
COPY pyproject.toml ./
COPY src ./src
RUN python -m pip install --no-cache-dir .

COPY config.yaml ./config.yaml
EXPOSE 8099

CMD ["python", "-m", "tcmcp_server", "--config", "/app/config.yaml", "--transport", "http", "--listen", "0.0.0.0:8099"]
"""


def _dockerignore() -> str:
    return """.venv
__pycache__
*.pyc
.git
.env
"""


def _readme(selection: Selection, preview: dict) -> str:
    tools = "\n".join(f"- `{n}`" for n in preview["registered"])
    products = ", ".join(selection.products) or "(none)"
    return f"""# {selection.project.name}

由 Tencent Cloud MCP Initializr 生成的只读运维 MCP。

- 产品: {products}
- 环境别名: `{selection.project.target}`

资源 ID 写在 `config.yaml`，工具入参只有别名 enum，避免模型猜 UUID。

## 已注册工具

{tools or "(未绑定资源，没有可注册工具)"}

## 运行

`run.sh` 会检查 Python 3.11+，首次启动自动创建 `.venv` 并安装依赖。
默认 stdio（Cursor / Claude Code）；HTTP 用启动参数切换。

```bash
export TENCENT_SECRET_ID=AKID...
export TENCENT_SECRET_KEY=...
bash ./run.sh
bash ./run.sh --transport http
```

### Cursor 本机接入

配置见 `mcp.json`。把 `/absolute/path/to/{_slug(selection.project.name)}` 改成解压后的绝对路径，
并在 Cursor 配置的 `env` 中填写密钥。第一次连接会自动完成环境安装。

### Docker Server

HTTP 模式强制 Bearer 鉴权。没有 `TCMCP_AUTH_TOKEN` 时进程不会启动。
密钥和鉴权 token 都用环境变量注入，不要写进镜像。

```bash
export TCMCP_AUTH_TOKEN=$(python -c 'import secrets; print(secrets.token_urlsafe(32))')
docker build -t {_slug(selection.project.name)} .
docker run --rm -p 127.0.0.1:8099:8099 \\
  -e TENCENT_SECRET_ID \\
  -e TENCENT_SECRET_KEY \\
  -e TCMCP_AUTH_TOKEN \\
  {_slug(selection.project.name)}
curl -fsS http://127.0.0.1:8099/healthz
```

Cursor 连这个 HTTP Server：

```json
{{
  "mcpServers": {{
    "{_slug(selection.project.name)}-http": {{
      "url": "http://127.0.0.1:8099/mcp",
      "headers": {{
        "Authorization": "Bearer 你的TCMCP_AUTH_TOKEN"
      }}
    }}
  }}
}}
```

`/healthz` 不鉴权。`/mcp` 必须带 `Authorization: Bearer <token>`。
生产环境仍应放在 TLS 网关后面；token 当作共享密钥轮换。

密钥用 `${{TENCENT_SECRET_ID}}` 占位，不要把真实密钥写进仓库。

CAM 草稿见 `deploy/cam-policy.json`，把 `$uin` 换成主账号 UIN。

一期只有只读能力，没有 `sql_query` / `redis-cli`，COS 也不会下载对象内容。
"""
