"""Standalone installer shipped to the user's machine; bootstrap uses stdlib only."""

from __future__ import annotations

import base64
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
import uuid
import zipfile


def prompt(label: str, variable: str) -> str:
    value = os.environ.get(variable, "").strip()
    if value:
        return value
    try:
        with open("/dev/tty", "w") as tty:
            tty.write(label)
            tty.flush()
            with open("/dev/tty", "r") as input_tty:
                value = input_tty.readline().strip()
    except OSError:
        raise ValueError(f"没有交互终端，请先 export {variable} 再运行安装命令") from None
    if not value:
        raise ValueError(f"{variable} 不能为空")
    return value


def extract_package(data: bytes, destination: Path) -> Path:
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        members = archive.infolist()
        roots = set()
        total = 0
        for member in members:
            path = PurePosixPath(member.filename)
            if (path.is_absolute() or ".." in path.parts or "\\" in member.filename
                    or not path.parts or (member.external_attr >> 16) & 0o170000 == 0o120000):
                raise ValueError("安装包含不安全的路径")
            roots.add(path.parts[0])
            total += member.file_size
        if len(roots) != 1 or total > 100 * 1024 * 1024:
            raise ValueError("安装包目录或大小无效")
        root = destination / roots.pop()
        archive.extractall(destination)
    for name in ("pyproject.toml", "config.yaml", "run.sh"):
        if not (root / name).is_file():
            raise ValueError(f"安装包缺少 {name}")
    return root


def config_path(client: str) -> Path:
    override = os.environ.get("TCMCP_CLIENT_CONFIG")
    if override:
        return Path(override).expanduser().resolve()
    if client == "codex":
        return Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex"))).expanduser() / "config.toml"
    if client not in ("cursor", "codebuddy", "workbuddy"):
        raise ValueError("不支持的客户端")
    return Path.home() / f".{client}" / "mcp.json"


def merge_config(client: str, previous: str, name: str, entry: dict) -> str:
    if client == "codex":
        import tomlkit

        document = tomlkit.parse(previous)
        servers = document.setdefault("mcp_servers", tomlkit.table())
        if not isinstance(servers, dict):
            raise ValueError("mcp_servers 必须是一个表")
        servers[name] = entry
        return tomlkit.dumps(document)
    import json5

    document = json5.loads(previous) if previous.strip() else {}
    if not isinstance(document, dict) or not isinstance(document.get("mcpServers", {}), dict):
        raise ValueError("MCP 配置及 mcpServers 必须是 JSON 对象")
    document.setdefault("mcpServers", {})[name] = {"type": "stdio", **entry}
    return json.dumps(document, indent=2, ensure_ascii=False) + "\n"


def empty_config(client: str) -> bytes:
    return b"" if client == "codex" else b'{\n  "mcpServers": {}\n}\n'


def write_rollback_artifacts(
    root: Path,
    client: str,
    path: Path,
    backup: Path,
    before_existed: bool,
    updated: bytes,
) -> Path:
    helper = root / ".tcmcp-installer.py"
    helper.write_text(Path(__file__).read_text(), encoding="utf-8")
    os.chmod(helper, 0o600)
    metadata = root / ".tcmcp-rollback.json"
    metadata.write_text(
        json.dumps(
            {
                "client": client,
                "config": str(path),
                "backup": str(backup),
                "beforeExisted": before_existed,
                "installedSha256": hashlib.sha256(updated).hexdigest(),
            },
            indent=2,
            ensure_ascii=False,
        )
        + "\n",
        encoding="utf-8",
    )
    os.chmod(metadata, 0o600)
    script = root / "rollback.sh"
    script.write_text(
        """#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$ROOT/.venv/bin/python" "$ROOT/.tcmcp-installer.py" --rollback "$ROOT/.tcmcp-rollback.json"
""",
        encoding="utf-8",
    )
    os.chmod(script, 0o700)
    return script


def atomic_write(path: Path, data: bytes) -> None:
    descriptor, temporary = tempfile.mkstemp(prefix=".tcmcp-rollback-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def rollback(metadata_path: Path) -> None:
    metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    path = Path(metadata["config"])
    backup = Path(metadata["backup"])
    with path.with_name(path.name + ".tcmcp.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if not path.is_file():
            raise ValueError(f"客户端配置不存在：{path}")
        current = path.read_bytes()
        if hashlib.sha256(current).hexdigest() != metadata["installedSha256"]:
            raise ValueError(f"客户端配置在安装后已发生变化，未自动回滚：{path}")
        recovery = path.with_name(f"{path.name}.before-rollback-{uuid.uuid4().hex}")
        if metadata["beforeExisted"]:
            if not backup.is_file():
                raise ValueError(f"找不到安装前备份：{backup}")
            with recovery.open("xb") as output:
                os.chmod(recovery, 0o600)
                output.write(current)
            atomic_write(path, backup.read_bytes())
            os.chmod(path, 0o600)
            print(f"已恢复安装前配置：{path}")
        else:
            os.replace(path, recovery)
            print(f"安装前没有客户端配置，已移走安装生成的配置：{recovery}")
            recovery = None
    if recovery:
        print(f"回滚前配置副本：{recovery}")


def configure(client: str, root: Path) -> Path:
    if not root.is_dir():
        raise ValueError(f"安装目录不存在：{root}")
    path = config_path(client).resolve()
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    entry = {
        "command": str(root / ".venv" / "bin" / "python"),
        "args": ["-m", "tcmcp_server", "--config", str(root / "config.yaml"), "--transport", "stdio"],
        "env": {key: os.environ[key] for key in ("TENCENT_SECRET_ID", "TENCENT_SECRET_KEY")},
    }
    # Lock cooperating installers and detect edits by the client before replacing.
    with path.with_name(path.name + ".tcmcp.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        previous = path.read_bytes() if path.exists() else None
        try:
            updated = merge_config(client, (previous or b"").decode("utf-8"), f"tcmcp-{root.name}", entry)
        except Exception:
            raise ValueError(f"无法解析已有配置 {path}，原文件未修改，请修复后重试") from None
        backup = path.with_name(f"{path.name}.bak-{uuid.uuid4().hex}")
        with backup.open("xb") as output:
            os.chmod(backup, 0o600)
            output.write(previous if previous is not None else empty_config(client))
        descriptor, temporary = tempfile.mkstemp(prefix=".tcmcp-", dir=path.parent)
        try:
            with os.fdopen(descriptor, "w", encoding="utf-8") as output:
                output.write(updated)
                output.flush()
                os.fsync(output.fileno())
            current = path.read_bytes() if path.exists() else None
            if current != previous:
                raise ValueError("客户端配置在安装过程中发生变化，请重新运行安装命令")
            rollback_script = write_rollback_artifacts(
                root, client, path, backup, previous is not None, updated.encode("utf-8")
            )
            os.replace(temporary, path)
        finally:
            Path(temporary).unlink(missing_ok=True)
    print(f"安装前配置快照：{backup}")
    print(f"已配置 {client}：{path}")
    print(f"回滚命令：bash {shlex.quote(str(rollback_script))}")
    return path


def install(settings: dict) -> None:
    token = settings["project"] or prompt("页面生成的安装 ID：", "TCMCP_INSTALL_ID")
    if not re.fullmatch(r"[a-f0-9]{32}", token):
        raise ValueError("安装 ID 无效，请复制页面生成的命令")
    url = f"{settings['base_url']}/api/installations/{token}/starter.zip"
    print("正在下载所选 MCP 项目…", flush=True)
    try:
        with urllib.request.urlopen(url, timeout=60) as response:
            data = response.read(10 * 1024 * 1024 + 1)
    except urllib.error.HTTPError as exc:
        raise ValueError(f"安装包下载失败（HTTP {exc.code}），请在页面重新生成命令") from None
    if len(data) > 10 * 1024 * 1024:
        raise ValueError("安装包过大")
    credentials = {
        "TENCENT_SECRET_ID": prompt("腾讯云 SecretId：", "TENCENT_SECRET_ID"),
        "TENCENT_SECRET_KEY": prompt("腾讯云 SecretKey：", "TENCENT_SECRET_KEY"),
    }
    base = Path(os.environ.get("TCMCP_INSTALL_DIR", str(Path.home() / ".local" / "share" / "tcmcp"))).expanduser().resolve()
    base.mkdir(parents=True, exist_ok=True, mode=0o700)
    release = Path(tempfile.mkdtemp(prefix="install-", dir=base))
    configured = False
    try:
        root = extract_package(data, release)
        python = root / ".venv" / "bin" / "python"
        print(f"正在安装依赖：{root}", flush=True)
        subprocess.run([sys.executable, "-m", "venv", str(root / ".venv")], check=True)
        subprocess.run([str(python), "-m", "pip", "install", str(root), "tomlkit>=0.13,<1", "json5>=0.10,<1"], check=True)
        # Preflight imports/config without making cloud calls or leaving a server running.
        subprocess.run([str(python), "-c", "from tcmcp_server.server import build_mcp; import sys; build_mcp(sys.argv[1])", str(root / "config.yaml")], check=True, env={**os.environ, **credentials})
        subprocess.run([str(python), str(Path(__file__).resolve()), "--configure", settings["client"], str(root)], check=True, env={**os.environ, **credentials})
        configured = True
    finally:
        if not configured:
            shutil.rmtree(release)
    print(f"安装完成。请在 {settings['client']} 刷新 MCP 或重启客户端；如提示信任，请在客户端确认。")


def main() -> None:
    os.umask(0o077)
    if sys.argv[1] == "--configure":
        configure(sys.argv[2], Path(sys.argv[3]))
    elif sys.argv[1] == "--rollback":
        rollback(Path(sys.argv[2]))
    else:
        install(json.loads(base64.b64decode(sys.argv[1])))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, zipfile.BadZipFile) as exc:
        print(f"tcmcp 安装失败：{exc}", file=sys.stderr)
        sys.exit(1)
