"""Bearer token gate for Streamable HTTP / Docker."""

from __future__ import annotations

import hmac
import os
from collections.abc import Iterable

from starlette.responses import JSONResponse

PUBLIC_PATHS = frozenset({"/healthz", "/health"})


def resolve_http_tokens(*groups: Iterable[str] | str | None) -> list[str]:
    out: list[str] = []
    seen: set[str] = set()
    for group in groups:
        if group is None:
            continue
        values = [group] if isinstance(group, str) else list(group)
        for raw in values:
            for part in str(raw).split(","):
                token = part.strip()
                if token and token not in seen:
                    seen.add(token)
                    out.append(token)
    extra = os.environ.get("TCMCP_AUTH_TOKENS") or os.environ.get("TCMCP_AUTH_TOKEN") or ""
    for part in extra.split(","):
        token = part.strip()
        if token and token not in seen:
            seen.add(token)
            out.append(token)
    return out


def _authorized(header: bytes, tokens: list[str]) -> bool:
    text = header.decode("latin-1", errors="replace").strip()
    if not text.lower().startswith("bearer "):
        return False
    given = text[7:].strip()
    if not given:
        return False
    return any(hmac.compare_digest(given, token) for token in tokens)


class BearerTokenASGI:
    """Reject HTTP requests that do not present a configured Bearer token."""

    def __init__(self, app, tokens: list[str]):
        self.app = app
        self.tokens = tokens

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return
        path = scope.get("path") or ""
        if path.rstrip("/") in PUBLIC_PATHS or path in PUBLIC_PATHS:
            await self.app(scope, receive, send)
            return
        headers = dict(scope.get("headers") or [])
        if _authorized(headers.get(b"authorization", b""), self.tokens):
            await self.app(scope, receive, send)
            return
        response = JSONResponse(
            {"error": "unauthorized", "hint": "Authorization: Bearer <TCMCP_AUTH_TOKEN>"},
            status_code=401,
            headers={"WWW-Authenticate": "Bearer"},
        )
        await response(scope, receive, send)


def protect_http_app(mcp, tokens: list[str]) -> None:
    inner = mcp.streamable_http_app

    def wrapped():
        return BearerTokenASGI(inner(), tokens)

    mcp.streamable_http_app = wrapped  # type: ignore[method-assign]
