"""Initializr HTTP API + static UI."""

from __future__ import annotations

import os
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import Response
from fastapi.staticfiles import StaticFiles

from app.catalog import metadata
from app.cloud import list_resources
from app.generate import _slug, build_zip
from app.install import router as install_router
from app.models import CloudCreds, Selection
from app.preview import materialize

WEB_DIST = Path(__file__).resolve().parent.parent / "web" / "dist"

# Same-origin 8088 does not need CORS. Vite :5173 is the only default cross-origin.
_DEFAULT_CORS = (
    "http://127.0.0.1:5173",
    "http://localhost:5173",
    "http://127.0.0.1:8088",
    "http://localhost:8088",
)


def _cors_origins() -> list[str]:
    raw = os.environ.get("TCMCP_CORS_ORIGINS", "").strip()
    if raw:
        return [item.strip() for item in raw.split(",") if item.strip()]
    return list(_DEFAULT_CORS)


app = FastAPI(title="Tencent Cloud MCP Initializr", version="0.1.0")
app.include_router(install_router)
app.add_middleware(
    CORSMiddleware,
    allow_origins=_cors_origins(),
    allow_methods=["GET", "POST", "OPTIONS"],
    allow_headers=["Content-Type"],
)


@app.get("/api/metadata")
def api_metadata():
    return metadata()


@app.post("/api/resources")
def api_resources(creds: CloudCreds):
    if not creds.secret_id or not creds.secret_key:
        raise HTTPException(400, "secretId / secretKey 必填")
    return list_resources(creds)


@app.post("/api/preview-schema")
def api_preview(selection: Selection):
    return materialize(selection)


@app.post("/api/starter.zip")
def api_starter(selection: Selection):
    if not selection.products:
        raise HTTPException(400, "至少选择一个产品")
    data = build_zip(selection)
    filename = f"{_slug(selection.project.name)}.zip"
    return Response(
        content=data,
        media_type="application/zip",
        headers={"Content-Disposition": f'attachment; filename="{filename}"'},
    )


@app.get("/api/healthz")
def healthz():
    return {"ok": True}


if WEB_DIST.is_dir():
    app.mount("/", StaticFiles(directory=WEB_DIST, html=True), name="web")


def main() -> None:
    import uvicorn

    host = os.environ.get("TCMCP_HOST", "127.0.0.1")
    port = int(os.environ.get("TCMCP_PORT", "8088"))
    uvicorn.run("app.main:app", host=host, port=port, reload=False)


if __name__ == "__main__":
    main()
