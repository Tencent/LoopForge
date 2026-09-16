import hmac

from app.mcp_runtime.http_auth import _authorized, resolve_http_tokens


def test_resolve_http_tokens_dedupes_and_skips_empty(monkeypatch):
    monkeypatch.delenv("TCMCP_AUTH_TOKEN", raising=False)
    monkeypatch.delenv("TCMCP_AUTH_TOKENS", raising=False)
    assert resolve_http_tokens("", ["", "  "], "  alpha  ", "alpha,beta") == ["alpha", "beta"]


def test_resolve_http_tokens_reads_env(monkeypatch):
    monkeypatch.setenv("TCMCP_AUTH_TOKEN", "from-env")
    monkeypatch.delenv("TCMCP_AUTH_TOKENS", raising=False)
    assert resolve_http_tokens([]) == ["from-env"]


def test_authorized_bearer_compare():
    tokens = ["secret-token"]
    assert _authorized(b"Bearer secret-token", tokens)
    assert _authorized(b"bearer secret-token", tokens)
    assert not _authorized(b"Bearer wrong", tokens)
    assert not _authorized(b"secret-token", tokens)
    assert hmac.compare_digest("secret-token", tokens[0])
