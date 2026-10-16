from pathlib import Path

WEB = Path(__file__).resolve().parents[1] / "web" / "src"


def test_wizard_and_schema_inspector_exist():
    app = (WEB / "App.tsx").read_text()
    inspector = (WEB / "components" / "SchemaInspector.tsx").read_text()
    assert "项目与产品" in app
    assert "云资源绑定" in app
    assert "绑定 Redis" in app
    assert "MCP Schema" in app
    assert "previewSchema" in app
    assert "disabled_tools" in app
    assert 'label="传输"' not in app
    assert "setTransport" not in app
    assert "安装与部署" in app
    assert "Docker Server" in app
    assert "TCMCP_AUTH_TOKEN" in app
    assert "复制启动命令" in app
    assert "复制 HTTP 配置" in app
    assert "复制配置" in app
    assert "mcpConfigCopy" in app
    assert "点复制会带上刚才填写的密钥" in app
    assert "按产品" in inspector
    assert "扁平列表" in inspector
    assert "查看原始 JSON" in inspector
    assert "enum" in inspector
