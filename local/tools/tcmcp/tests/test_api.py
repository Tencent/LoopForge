from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from app.main import app

client = TestClient(app)
WEB_DIST = Path(__file__).resolve().parents[1] / "web" / "dist"


def test_metadata_and_preview_and_zip():
    meta = client.get("/api/metadata")
    assert meta.status_code == 200
    assert [p["id"] for p in meta.json()["products"]] == ["redis", "cdb", "tdsqlc", "tke", "cls", "cos"]
    assert any(r["id"] == "ap-guangzhou" for r in meta.json()["regions"])

    body = {
        "project": {"name": "demo", "target": "prod", "transport": "stdio", "region": "ap-guangzhou"},
        "products": ["cls"],
        "disabled_tools": [],
        "resources": {
            "cls": [
                {
                    "logsetId": "ls",
                    "topics": [
                        {"id": "t-api", "alias": "api"},
                        {"id": "t-audit", "alias": "audit"},
                    ],
                }
            ]
        },
    }
    preview = client.post("/api/preview-schema", json=body)
    assert preview.status_code == 200
    data = preview.json()
    assert "cls_search_log" in data["registered"]
    assert "redis_instance_detail" not in data["registered"]
    topic_enum = next(t for t in data["tools"] if t["name"] == "cls_search_log")["inputSchema"]["properties"]["topic"]["enum"]
    assert topic_enum == ["api", "audit"]

    z = client.post("/api/starter.zip", json=body)
    assert z.status_code == 200
    assert z.headers["content-type"].startswith("application/zip")
    assert z.content[:2] == b"PK"


def test_cors_allows_vite_origin():
    res = client.get("/api/healthz", headers={"Origin": "http://127.0.0.1:5173"})
    assert res.status_code == 200
    assert res.headers.get("access-control-allow-origin") == "http://127.0.0.1:5173"


def test_cors_rejects_foreign_origin():
    res = client.get("/api/healthz", headers={"Origin": "https://evil.example"})
    assert res.status_code == 200
    assert res.headers.get("access-control-allow-origin") != "https://evil.example"


@pytest.mark.skipif(not (WEB_DIST / "index.html").exists(), reason="web/dist not built")
def test_ui_index_served():
    page = client.get("/")
    assert page.status_code == 200
    assert "Tencent Cloud MCP Initializr" in page.text
