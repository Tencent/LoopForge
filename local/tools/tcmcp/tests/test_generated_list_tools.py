import asyncio
import sys
import zipfile
from io import BytesIO
from pathlib import Path

from app.generate import build_zip
from app.models import BoundResources, CLSResource, CLSTopic, ProjectSpec, Selection
from app.preview import materialize


def test_generated_list_tools_matches_preview(tmp_path: Path):
    sel = Selection(
        project=ProjectSpec(name="skill-ops", target="prod"),
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
    preview = materialize(sel)
    with zipfile.ZipFile(BytesIO(build_zip(sel))) as zf:
        zf.extractall(tmp_path)

    src = str(tmp_path / "skill-ops" / "src")
    sys.path.insert(0, src)
    for key in list(sys.modules):
        if key == "tcmcp_server" or key.startswith("tcmcp_server."):
            del sys.modules[key]
    try:
        from tcmcp_server.server import build_mcp

        mcp = build_mcp(str(tmp_path / "skill-ops" / "config.yaml"))
        tools = asyncio.run(mcp.list_tools())
        assert [t.name for t in tools] == preview["registered"]
        by_name = {t.name: t for t in tools}
        for item in preview["tools"]:
            got = by_name[item["name"]].inputSchema
            assert got.get("required") == item["inputSchema"].get("required")
            for key, prop in item["inputSchema"]["properties"].items():
                if "enum" in prop:
                    assert got["properties"][key]["enum"] == prop["enum"]
    finally:
        sys.path.remove(src)
        for key in list(sys.modules):
            if key == "tcmcp_server" or key.startswith("tcmcp_server."):
                del sys.modules[key]
