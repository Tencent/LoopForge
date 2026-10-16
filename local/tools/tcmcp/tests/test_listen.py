from app.mcp_runtime.listen import parse_listen, resolve_transport


def test_parse_listen_defaults_to_localhost():
    assert parse_listen(None) == ("127.0.0.1", 8099)
    assert parse_listen("") == ("127.0.0.1", 8099)
    assert parse_listen(":8099") == ("127.0.0.1", 8099)
    assert parse_listen("8099") == ("127.0.0.1", 8099)
    assert parse_listen("127.0.0.1:9000") == ("127.0.0.1", 9000)
    assert parse_listen("0.0.0.0:8099") == ("0.0.0.0", 8099)


def test_resolve_transport_cli_wins():
    assert resolve_transport() == "stdio"
    assert resolve_transport("http", "stdio", "stdio") == "http"
    assert resolve_transport(None, "http") == "http"
    assert resolve_transport(None, None, "stdio") == "stdio"
    assert resolve_transport("bogus", "http") == "http"
