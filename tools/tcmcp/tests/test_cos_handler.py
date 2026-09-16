import sys
import zipfile
from io import BytesIO
from pathlib import Path

from app.generate import build_zip
from app.models import BoundResources, ProjectSpec, ResourceItem, Selection


def test_generated_cos_handler_lists_objects_and_metadata(tmp_path: Path, monkeypatch):
    selection = Selection(
        project=ProjectSpec(name="cos-ops", target="prod", region="ap-guangzhou"),
        products=["cos"],
        resources=BoundResources(
            cos=[
                ResourceItem(
                    id="assets-1250000000",
                    alias="assets",
                    region="ap-guangzhou",
                )
            ]
        ),
    )
    with zipfile.ZipFile(BytesIO(build_zip(selection))) as zf:
        zf.extractall(tmp_path)

    src = str(tmp_path / "cos-ops" / "src")
    sys.path.insert(0, src)
    for key in list(sys.modules):
        if key == "tcmcp_server" or key.startswith("tcmcp_server."):
            del sys.modules[key]

    class FakeClient:
        def list_objects(self, **kwargs):
            assert kwargs == {
                "Bucket": "assets-1250000000",
                "MaxKeys": 20,
                "Prefix": "logs/",
                "Delimiter": "/",
            }
            return {
                "Prefix": "logs/",
                "Contents": [
                    {
                        "Key": "logs/app.json",
                        "Size": "42",
                        "ETag": '"abc"',
                        "LastModified": "2026-09-14T00:00:00Z",
                        "StorageClass": "STANDARD",
                    }
                ],
                "CommonPrefixes": {"Prefix": "logs/archive/"},
                "IsTruncated": "true",
                "NextMarker": "logs/app.json",
            }

        def head_object(self, **kwargs):
            assert kwargs == {
                "Bucket": "assets-1250000000",
                "Key": "logs/app.json",
                "versionId": "v1",
            }
            return {"Content-Length": "42", "ETag": '"abc"'}

    try:
        from tcmcp_server.config import load
        from tcmcp_server.handlers import cos

        monkeypatch.setattr(cos, "cos_client", lambda *args: FakeClient())
        data, generated_selection = load(tmp_path / "cos-ops" / "config.yaml")
        listed = cos.handle_list_objects(
            {"bucket": "assets", "prefix": "logs/", "delimiter": "/", "limit": 20},
            data,
            generated_selection,
        )
        assert listed["items"][0]["key"] == "logs/app.json"
        assert listed["commonPrefixes"] == ["logs/archive/"]
        assert listed["isTruncated"] is True
        metadata = cos.handle_object_metadata(
            {"bucket": "assets", "key": "logs/app.json", "versionId": "v1"},
            data,
            generated_selection,
        )
        assert metadata["metadata"]["Content-Length"] == "42"
    finally:
        sys.path.remove(src)
        for key in list(sys.modules):
            if key == "tcmcp_server" or key.startswith("tcmcp_server."):
                del sys.modules[key]
