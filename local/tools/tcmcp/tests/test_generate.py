import json
import zipfile
from io import BytesIO
from pathlib import Path

import yaml

from app.generate import build_files, build_zip
from app.models import BoundResources, CLSResource, CLSTopic, ProjectSpec, ResourceItem, Selection
from app.preview import materialize


def _selection() -> Selection:
    return Selection(
        project=ProjectSpec(name="skill-ops", target="prod", transport="stdio"),
        products=["cls"],
        resources=BoundResources(
            cls=[
                CLSResource(
                    logsetId="logset-1",
                    topics=[
                        CLSTopic(id="aaaa", alias="api", name="api"),
                        CLSTopic(id="bbbb", alias="audit", name="audit"),
                    ],
                )
            ]
        ),
    )


def test_zip_omits_other_product_handlers():
    files = build_files(_selection())
    names = list(files)
    assert any(n.endswith("src/tcmcp_server/handlers/cls.py") for n in names)
    assert not any(n.endswith("src/tcmcp_server/handlers/redis.py") for n in names)
    assert not any(n.endswith("src/tcmcp_server/handlers/tdsqlc.py") for n in names)
    assert not any(n.endswith("src/tcmcp_server/handlers/dbbrain.py") for n in names)
    assert "cos-python-sdk-v5" not in files["skill-ops/pyproject.toml"].decode()


def test_config_only_has_selected_topics():
    files = build_files(_selection())
    cfg = yaml.safe_load(files["skill-ops/config.yaml"])
    topics = cfg["targets"]["prod"]["cls"][0]["topics"]
    assert {t["alias"] for t in topics} == {"api", "audit"}
    assert "redis" not in cfg["targets"]["prod"]
    assert cfg["tencent"]["secretId"] == "${TENCENT_SECRET_ID}"
    assert cfg["server"]["listen"] == "127.0.0.1:8099"
    assert "transport" not in cfg["server"]
    assert cfg["auth"]["tokens"] == ["${TCMCP_AUTH_TOKEN}"]
    mcp_json = json.loads(files["skill-ops/mcp.json"])
    server = next(iter(mcp_json["mcpServers"].values()))
    assert server["command"] == "/bin/bash"
    assert server["args"] == ["/absolute/path/to/skill-ops/run.sh"]


def test_bootstrap_script_installs_without_polluting_stdout():
    files = build_files(_selection())
    script = files["skill-ops/run.sh"].decode()
    assert "python3" in script
    assert "sys.version_info < (3, 11)" in script
    assert 'python" -m venv' not in script
    assert '"$PYTHON" -m venv "$VENV"' in script
    assert 'pip install -e "$ROOT" >&2' in script
    assert 'exec "$VENV/bin/python" -m tcmcp_server' in script


def test_docker_server_uses_http_and_runtime_secrets():
    files = build_files(_selection())
    dockerfile = files["skill-ops/Dockerfile"].decode()
    assert "TENCENT_SECRET" not in dockerfile
    assert '"--transport", "http"' in dockerfile
    assert '"--listen", "0.0.0.0:8099"' in dockerfile
    assert "EXPOSE 8099" in dockerfile
    assert "skill-ops/.dockerignore" in files
    assert "skill-ops/src/tcmcp_server/http_auth.py" in files
    assert "TCMCP_AUTH_TOKEN" not in dockerfile


def test_preview_matches_generated_json():
    sel = _selection()
    preview = materialize(sel)
    files = build_files(sel)
    gen = json.loads(files["skill-ops/src/tcmcp_server/generated.json"])
    assert set(gen["registered"]) == set(preview["registered"])
    assert gen["products"] == ["cls"]


def test_zip_roundtrip_materialize(tmp_path: Path):
    sel = _selection()
    preview = materialize(sel)
    raw = build_zip(sel)
    with zipfile.ZipFile(BytesIO(raw)) as zf:
        zf.extractall(tmp_path)
    cfg = yaml.safe_load((tmp_path / "skill-ops" / "config.yaml").read_text())
    from app.models import Selection as S
    from app.preview import materialize as mat

    again = mat(
        S.model_validate(
            {
                "project": {
                    **cfg["project"],
                    "region": cfg["tencent"]["region"],
                },
                "products": cfg["products"],
                "disabled_tools": cfg["disabledTools"],
                "resources": {"cls": cfg["targets"]["prod"]["cls"]},
            }
        )
    )
    assert again["registered"] == preview["registered"]
    search = next(t for t in again["tools"] if t["name"] == "cls_search_log")
    assert search["inputSchema"]["properties"]["topic"]["enum"] == ["api", "audit"]
    assert (tmp_path / "skill-ops" / "deploy" / "cam-policy.json").exists()
    copied = (tmp_path / "skill-ops" / "src" / "tcmcp_server" / "preview.py").read_text()
    assert "from tcmcp_server." in copied
    assert "from app." not in copied


def test_cos_selection_generates_handler_config_and_dependency():
    sel = Selection(
        project=ProjectSpec(name="cos-ops", target="prod", region="ap-guangzhou"),
        products=["cos"],
        resources=BoundResources(
            cos=[
                ResourceItem(
                    id="assets-1250000000",
                    alias="assets",
                    name="assets-1250000000",
                    region="ap-guangzhou",
                )
            ]
        ),
    )
    files = build_files(sel)
    cfg = yaml.safe_load(files["cos-ops/config.yaml"])
    assert cfg["targets"]["prod"]["cos"] == [
        {
            "alias": "assets",
            "id": "assets-1250000000",
            "name": "assets-1250000000",
            "region": "ap-guangzhou",
        }
    ]
    assert "cos-ops/src/tcmcp_server/handlers/cos.py" in files
    assert "cos-python-sdk-v5" in files["cos-ops/pyproject.toml"].decode()
