"""Parse config.yaml server.listen into a bind address."""

from __future__ import annotations

DEFAULT_LISTEN = "127.0.0.1:8099"


def parse_listen(listen: str | None) -> tuple[str, int]:
    """Bind localhost unless the config names another host."""
    raw = (listen or "").strip() or DEFAULT_LISTEN
    if raw.startswith(":"):
        host, port = "127.0.0.1", raw[1:]
    elif ":" in raw:
        host, _, port = raw.rpartition(":")
        host = host or "127.0.0.1"
    else:
        host, port = "127.0.0.1", raw
    return host, int(port or 8099)


def resolve_transport(*candidates: str | None) -> str:
    """First of CLI / env / leftover config; default stdio."""
    for value in candidates:
        if value in ("stdio", "http"):
            return value
    return "stdio"
