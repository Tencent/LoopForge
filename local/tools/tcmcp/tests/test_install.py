import io
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import tomllib
import zipfile

from fastapi.testclient import TestClient
import pytest

from app import install as api
from app import installer
from app.generate import build_zip
from app.main import app
from app.models import ProjectSpec, Selection


@pytest.fixture
def client(tmp_path, monkeypatch):
    monkeypatch.setenv("TCMCP_INSTALL_STORE", str(tmp_path / "packages"))
    return TestClient(app)


def test_installation_scoped_expiring_package(client):
    one = client.post("/api/installations", json={"project": {"name": "one"}, "products": ["cls"], "secretKey": "DO-NOT-STORE"})
    two = client.post("/api/installations", json={"project": {"name": "two"}, "products": ["redis"]})
    assert one.status_code == two.status_code == 200
    token = one.json()["id"]
    assert token != two.json()["id"]
    assert one.headers["cache-control"] == "no-store"
    package = client.get(f"/api/installations/{token}/starter.zip")
    with zipfile.ZipFile(io.BytesIO(package.content)) as z:
        assert all(name.startswith("one/") for name in z.namelist())
        assert all(b"DO-NOT-STORE" not in z.read(name) for name in z.namelist())
    for name, path in one.json()["paths"].items():
        response = client.get(path)
        assert response.status_code == 200
        assert response.text.startswith("#!/usr/bin/env bash")
        assert "text/x-shellscript" in response.headers["content-type"]
        assert response.headers["cache-control"] == "no-store"
        subprocess.run(["bash", "-n"], input=response.text, text=True, check=True)
        assert client.get(f"/{name}").status_code == 200
    expired = api.package_path(token)
    os.utime(expired, (time.time() - api.TTL - 1,) * 2)
    assert client.get(f"/codebuddy?project={token}").status_code == 410
    assert client.get(f"/api/installations/{token}/starter.zip").status_code == 404


def test_invalid_empty_and_full_store(client, monkeypatch):
    assert client.post("/api/installations", json={}).status_code == 400
    assert client.get("/cursor?project=../../etc/passwd").status_code == 404
    assert client.get(f"/codex?project={'a' * 32}").status_code == 404
    monkeypatch.setattr(api, "MAX_PACKAGES", 1)
    assert client.post("/api/installations", json={"products": ["cls"]}).status_code == 200
    assert client.post("/api/installations", json={"products": ["cls"]}).status_code == 503


@pytest.mark.parametrize("agent", api.CLIENTS)
def test_config_merge_backup_and_reinstall(tmp_path, monkeypatch, agent):
    path = tmp_path / ("config.toml" if agent == "codex" else "mcp.json")
    original = '# user comment\nmodel = "keep"\n[mcp_servers.other]\ncommand = "keep"\n' if agent == "codex" else '{// user comment\n"theme":"keep","mcpServers":{"other":{"command":"keep"}},}'
    path.write_text(original)
    monkeypatch.setenv("TCMCP_CLIENT_CONFIG", str(path))
    monkeypatch.setenv("TENCENT_SECRET_ID", "test-id")
    monkeypatch.setenv("TENCENT_SECRET_KEY", 'key-"-\\-测试')
    root = tmp_path / "path with spaces" / "demo.name"
    root.mkdir(parents=True)
    for _ in range(2):
        installer.configure(agent, root)
        data = tomllib.loads(path.read_text()) if agent == "codex" else json.loads(path.read_text())
        servers = data["mcp_servers" if agent == "codex" else "mcpServers"]
        assert len(servers) == 2
        assert servers["other"] == {"command": "keep"}
        entry = servers["tcmcp-demo.name"]
        assert entry["args"][3] == str(root / "config.yaml")
        assert entry["env"]["TENCENT_SECRET_KEY"] == 'key-"-\\-测试'
        assert entry["command"] == str(root / ".venv/bin/python")
        assert data["model" if agent == "codex" else "theme"] == "keep"
    assert len(list(tmp_path.glob(f"{path.name}.bak-*"))) == 2
    assert any(p.read_text() == original for p in tmp_path.glob(f"{path.name}.bak-*"))
    assert path.stat().st_mode & 0o777 == 0o600
    if agent == "codex":
        assert "# user comment" in path.read_text()
    assert (root / "rollback.sh").stat().st_mode & 0o777 == 0o700


@pytest.mark.parametrize("agent", api.CLIENTS)
def test_rollback_restores_existing_config(tmp_path, monkeypatch, agent):
    path = tmp_path / ("config.toml" if agent == "codex" else "mcp.json")
    original = b'[mcp_servers.other]\ncommand = "keep"\n' if agent == "codex" else b'{"mcpServers":{"other":{"command":"keep"}}}\n'
    path.write_bytes(original)
    root = tmp_path / "project" / agent
    root.mkdir(parents=True)
    (root / ".venv/bin").mkdir(parents=True)
    (root / ".venv/bin/python").symlink_to(sys.executable)
    monkeypatch.setenv("TCMCP_CLIENT_CONFIG", str(path))
    monkeypatch.setenv("TENCENT_SECRET_ID", "fake")
    monkeypatch.setenv("TENCENT_SECRET_KEY", "fake")
    installer.configure(agent, root)
    subprocess.run(["bash", str(root / "rollback.sh")], check=True)
    assert path.read_bytes() == original
    assert list(tmp_path.glob(f"{path.name}.bak-*"))
    assert list(tmp_path.glob(f"{path.name}.before-rollback-*"))


def test_first_install_has_snapshot_and_rollback(tmp_path, monkeypatch):
    path = tmp_path / "cursor" / "mcp.json"
    root = tmp_path / "project"
    root.mkdir()
    (root / ".venv/bin").mkdir(parents=True)
    (root / ".venv/bin/python").symlink_to(sys.executable)
    monkeypatch.setenv("TCMCP_CLIENT_CONFIG", str(path))
    monkeypatch.setenv("TENCENT_SECRET_ID", "fake")
    monkeypatch.setenv("TENCENT_SECRET_KEY", "fake")
    installer.configure("cursor", root)
    backups = list(path.parent.glob("mcp.json.bak-*"))
    assert len(backups) == 1
    assert json.loads(backups[0].read_text()) == {"mcpServers": {}}
    subprocess.run(["bash", str(root / "rollback.sh")], check=True)
    assert not path.exists()
    assert len(list(path.parent.glob("mcp.json.before-rollback-*"))) == 1


def test_rollback_refuses_changed_config(tmp_path, monkeypatch):
    path = tmp_path / "mcp.json"
    path.write_text('{"mcpServers": {}}')
    root = tmp_path / "project"
    root.mkdir()
    (root / ".venv/bin").mkdir(parents=True)
    (root / ".venv/bin/python").symlink_to(sys.executable)
    monkeypatch.setenv("TCMCP_CLIENT_CONFIG", str(path))
    monkeypatch.setenv("TENCENT_SECRET_ID", "fake")
    monkeypatch.setenv("TENCENT_SECRET_KEY", "fake")
    installer.configure("cursor", root)
    path.write_text('{"mcpServers": {"later": {"command": "keep"}}}')
    result = subprocess.run(["bash", str(root / "rollback.sh")], text=True, capture_output=True)
    assert result.returncode != 0
    assert "安装后已发生变化" in result.stderr
    assert "later" in path.read_text()


@pytest.mark.parametrize("agent,invalid", [("cursor", '{"secret":"dont-log",'), ("cursor", '{"mcpServers": []}'), ("codex", '[mcp_servers\n'), ("codex", 'mcp_servers = []')])
def test_invalid_config_untouched(tmp_path, monkeypatch, agent, invalid):
    path = tmp_path / "config"
    path.write_text(invalid)
    monkeypatch.setenv("TCMCP_CLIENT_CONFIG", str(path))
    monkeypatch.setenv("TENCENT_SECRET_ID", "fake")
    monkeypatch.setenv("TENCENT_SECRET_KEY", "fake")
    with pytest.raises(ValueError, match="原文件未修改") as exc:
        root = tmp_path / "demo"
        root.mkdir()
        installer.configure(agent, root)
    assert "dont-log" not in str(exc.value)
    assert path.read_text() == invalid
    assert not list(tmp_path.glob("*.bak-*"))


def test_default_paths_and_codex_home(monkeypatch, tmp_path):
    monkeypatch.delenv("TCMCP_CLIENT_CONFIG", raising=False)
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    for agent in ("cursor", "codebuddy", "workbuddy"):
        assert installer.config_path(agent) == tmp_path / f".{agent}/mcp.json"
    monkeypatch.setenv("CODEX_HOME", str(tmp_path / "custom-codex"))
    assert installer.config_path("codex") == tmp_path / "custom-codex/config.toml"


def test_extract_rejects_traversal(tmp_path):
    stream = io.BytesIO()
    with zipfile.ZipFile(stream, "w") as z:
        z.writestr("../escaped", "bad")
    with pytest.raises(ValueError, match="不安全"):
        installer.extract_package(stream.getvalue(), tmp_path)
    assert not (tmp_path.parent / "escaped").exists()


def test_generated_metadata_is_valid_and_safe(tmp_path):
    selection = Selection(project=ProjectSpec(name="..", description='quote\' and " and\n换行'), products=["cls"])
    root = installer.extract_package(build_zip(selection), tmp_path)
    assert root.name == "tcmcp-project"
    assert tomllib.loads((root / "pyproject.toml").read_text())["project"]["description"] == selection.project.description


def test_install_failure_leaves_client_untouched(tmp_path, monkeypatch):
    monkeypatch.setenv("TCMCP_INSTALL_DIR", str(tmp_path / "installs"))
    monkeypatch.setenv("TENCENT_SECRET_ID", "fake")
    monkeypatch.setenv("TENCENT_SECRET_KEY", "fake")
    data = build_zip(Selection(products=["cls"]))
    monkeypatch.setattr(installer.urllib.request, "urlopen", lambda *a, **kw: io.BytesIO(data))
    def fail(*args, **kwargs):
        raise subprocess.CalledProcessError(1, ["venv"])
    monkeypatch.setattr(installer.subprocess, "run", fail)
    with pytest.raises(subprocess.CalledProcessError):
        installer.install({"project": "a" * 32, "client": "cursor", "base_url": "http://testserver"})
    assert list((tmp_path / "installs").iterdir()) == []


def test_piped_script_no_tty_reports_missing_id(tmp_path):
    script = api.render_script("codebuddy", "http://testserver")
    env = {**os.environ, "TCMCP_PYTHON": sys.executable}
    env.pop("TCMCP_INSTALL_ID", None)
    result = subprocess.run(["bash"], input=script, text=True, capture_output=True, env=env, start_new_session=True)
    assert result.returncode != 0
    assert "export TCMCP_INSTALL_ID" in result.stderr
    assert "unbound variable" not in result.stderr
