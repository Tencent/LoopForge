"""Short-lived, per-selection installation packages and curl entrypoints."""

from __future__ import annotations

import base64
import fcntl
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import tempfile
import time

from fastapi import APIRouter, HTTPException, Request
from fastapi.responses import Response

from app.generate import build_zip
from app.models import Selection

router = APIRouter()
CLIENTS = ("cursor", "codebuddy", "codex", "workbuddy")
TTL = 24 * 60 * 60
MAX_PACKAGES = 200
TOKEN = re.compile(r"^[a-f0-9]{32}$")
NO_CACHE = {"Cache-Control": "no-store", "Referrer-Policy": "no-referrer"}


def store_dir() -> Path:
    path = Path(os.environ.get("TCMCP_INSTALL_STORE", str(Path(tempfile.gettempdir()) / "tcmcp-installations")))
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    return path


def package_path(token: str) -> Path:
    if not TOKEN.fullmatch(token):
        raise HTTPException(404, "安装 ID 无效，请在页面重新生成")
    path = store_dir() / f"{token}.zip"
    try:
        expired = time.time() - path.stat().st_mtime >= TTL
    except FileNotFoundError:
        raise HTTPException(404, "安装链接不存在或已过期，请在页面重新生成")
    if expired:
        path.unlink(missing_ok=True)
        raise HTTPException(410, "安装链接已过期，请在页面重新生成")
    return path


@router.post("/api/installations")
def create_installation(selection: Selection):
    if not selection.products:
        raise HTTPException(400, "至少选择一个产品")
    data = build_zip(selection)
    if len(data) > 10 * 1024 * 1024:
        raise HTTPException(413, "项目过大，请减少资源数量")
    directory = store_dir()
    with (directory / ".lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        now = time.time()
        for path in directory.glob("*.zip"):
            if now - path.stat().st_mtime >= TTL:
                path.unlink(missing_ok=True)
        if len(list(directory.glob("*.zip"))) >= MAX_PACKAGES:
            raise HTTPException(503, "安装包存储已满，请稍后重试或联系管理员")
        token = secrets.token_hex(16)
        path = directory / f"{token}.zip"
        with path.open("xb") as output:
            os.chmod(path, 0o600)
            output.write(data)
    return Response(
        json.dumps({"id": token, "expiresAt": int(now + TTL),
                    "paths": {client: f"/{client}?project={token}" for client in CLIENTS}}),
        media_type="application/json", headers=NO_CACHE,
    )


@router.get("/api/installations/{token}/starter.zip")
def installation_zip(token: str):
    return Response(package_path(token).read_bytes(), media_type="application/zip", headers=NO_CACHE)


def render_script(client: str, base_url: str, token: str = "") -> str:
    settings = base64.b64encode(json.dumps({"client": client, "base_url": base_url, "project": token}).encode()).decode()
    source = Path(__file__).with_name("installer.py").read_text()
    # The function is invoked only after its closing brace arrives: a truncated curl
    # response cannot start installing part of a shell script.
    return f'''#!/usr/bin/env bash
set -euo pipefail
tcmcp_install() {{
  umask 077
  local tcmcp_python="${{TCMCP_PYTHON:-python3}}"
  command -v "$tcmcp_python" >/dev/null 2>&1 || {{ echo '需要 Python 3.11+，可通过 TCMCP_PYTHON 指定路径。' >&2; return 1; }}
  "$tcmcp_python" -c 'import sys; raise SystemExit(sys.version_info < (3, 11))' || {{ echo '需要 Python 3.11+。' >&2; return 1; }}
  tcmcp_tmp="$(mktemp -d)"
  trap 'if [[ -n "${{tcmcp_tmp:-}}" ]]; then rm -rf -- "$tcmcp_tmp"; fi' EXIT
  cat > "$tcmcp_tmp/installer.py" <<'TCMCP_INSTALLER_PY'
{source}
TCMCP_INSTALLER_PY
  "$tcmcp_python" "$tcmcp_tmp/installer.py" {shlex.quote(settings)}
  rm -rf -- "$tcmcp_tmp"
  trap - EXIT
}}
tcmcp_install
'''


@router.get("/cursor")
@router.get("/codebuddy")
@router.get("/codex")
@router.get("/workbuddy")
def installer(request: Request, project: str = ""):
    if project:
        package_path(project)
    client = request.url.path.rsplit("/", 1)[-1]
    base_url = os.environ.get("TCMCP_PUBLIC_URL", str(request.base_url)).rstrip("/")
    return Response(render_script(client, base_url, project), media_type="text/x-shellscript", headers=NO_CACHE)
