"""MCP server: list_tools comes from preview.materialize (same as the Initializr)."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from mcp.server.fastmcp import FastMCP
from mcp.types import TextContent, Tool

from starlette.requests import Request
from starlette.responses import JSONResponse

from tcmcp_server.config import load
from tcmcp_server.handlers.dispatch import call, load_handlers
from tcmcp_server.http_auth import protect_http_app, resolve_http_tokens
from tcmcp_server.listen import parse_listen, resolve_transport
from tcmcp_server.preview import materialize

_STATE: dict = {}


def _preview():
    return materialize(_STATE["selection"])


class ScopedMCP(FastMCP):
    """list_tools is the materialized catalog, not inferred function signatures."""

    async def list_tools(self) -> list[Tool]:
        tools: list[Tool] = []
        for item in _preview()["tools"]:
            tools.append(
                Tool(
                    name=item["name"],
                    description=item["description"],
                    inputSchema=item["inputSchema"],
                    annotations={"readOnlyHint": item["annotations"]["readOnlyHint"]},
                )
            )
        return tools

    async def call_tool(self, name: str, arguments: dict):
        registered = {t["name"] for t in _preview()["tools"]}
        if name not in registered:
            payload = {"error": f"工具未注册: {name}"}
        else:
            try:
                payload = call(name, arguments or {}, _STATE["data"], _STATE["selection"])
            except Exception as exc:  # noqa: BLE001
                payload = {"error": str(exc)}
        text = json.dumps(payload, ensure_ascii=False, default=str)
        return [TextContent(type="text", text=text)]


def build_mcp(config_path: str, listen_override: str | None = None) -> FastMCP:
    data, selection = load(config_path)
    _STATE["data"] = data
    _STATE["selection"] = selection
    load_handlers(selection.products)
    preview = _preview()
    host, port = parse_listen(listen_override or (data.get("server") or {}).get("listen"))
    endpoint = (data.get("server") or {}).get("endpointPath") or "/mcp"
    mcp = ScopedMCP(
        selection.project.name or "tcmcp",
        instructions=preview["instructions"],
        host=host,
        port=port,
        streamable_http_path=endpoint,
    )

    @mcp.custom_route("/healthz", methods=["GET"])
    async def healthz(_request: Request) -> JSONResponse:
        return JSONResponse({"ok": True})

    return mcp


def main(argv: list[str] | None = None) -> None:
    parser = argparse.ArgumentParser(description="Tencent Cloud ops MCP")
    parser.add_argument("-config", "--config", default=os.environ.get("TCMCP_CONFIG", "config.yaml"))
    parser.add_argument(
        "-t",
        "--transport",
        choices=("stdio", "http"),
        default=None,
        help="stdio (default) or streamable HTTP. Overrides TCMCP_TRANSPORT.",
    )
    parser.add_argument(
        "--listen",
        default=None,
        help="HTTP bind address, for example 0.0.0.0:8099. Overrides TCMCP_LISTEN and config.",
    )
    parser.add_argument(
        "--auth-token",
        default=None,
        help="Bearer token for HTTP mode. Overrides / adds to TCMCP_AUTH_TOKEN.",
    )
    args = parser.parse_args(argv)
    path = Path(args.config)
    if not path.exists():
        raise SystemExit(f"config not found: {path}")
    data, selection = load(path)
    mcp = build_mcp(str(path), args.listen or os.environ.get("TCMCP_LISTEN"))
    transport = resolve_transport(
        args.transport,
        os.environ.get("TCMCP_TRANSPORT"),
        (data.get("server") or {}).get("transport"),
        selection.project.transport,
    )
    if transport == "http":
        tokens = resolve_http_tokens(args.auth_token, (data.get("auth") or {}).get("tokens") or [])
        if not tokens:
            raise SystemExit(
                "HTTP mode requires TCMCP_AUTH_TOKEN (or --auth-token). "
                "Do not start a Docker / HTTP server without a Bearer token."
            )
        protect_http_app(mcp, tokens)
        mcp.run(transport="streamable-http")
    else:
        mcp.run(transport="stdio")


if __name__ == "__main__":
    main()
