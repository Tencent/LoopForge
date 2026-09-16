"""Load config.yaml and expose it as a Selection for preview.materialize."""

from __future__ import annotations

import os
import re
from pathlib import Path

import yaml

from tcmcp_server.models import (
    BoundResources,
    CLSResource,
    CLSTopic,
    ProjectSpec,
    ResourceItem,
    Selection,
    TDSQLCResource,
)

_ENV = re.compile(r"\$\{([A-Za-z_][A-Za-z0-9_]*)\}")


def expand_env(text: str) -> str:
    def repl(match: re.Match[str]) -> str:
        return os.environ.get(match.group(1), "")

    return _ENV.sub(repl, text)


def load(path: str | Path) -> tuple[dict, Selection]:
    raw = Path(path).read_text()
    data = yaml.safe_load(expand_env(raw)) or {}
    return data, selection_from_config(data)


def selection_from_config(data: dict) -> Selection:
    project_raw = data.get("project") or {}
    tencent = data.get("tencent") or {}
    target_name = project_raw.get("target") or "prod"
    targets = data.get("targets") or {}
    t = targets.get(target_name) or {}

    redis = [
        ResourceItem(id=x["id"], alias=x["alias"], name=x.get("name", ""), region=x.get("region", ""))
        for x in t.get("redis") or []
    ]
    cdb = [
        ResourceItem(id=x["id"], alias=x["alias"], name=x.get("name", ""), region=x.get("region", ""))
        for x in t.get("cdb") or []
    ]
    tdsqlc = [
        TDSQLCResource(
            clusterId=x["clusterId"],
            instanceId=x["instanceId"],
            alias=x["alias"],
            name=x.get("name", ""),
            region=x.get("region", ""),
        )
        for x in t.get("tdsqlc") or []
    ]
    tke = [
        ResourceItem(id=x["id"], alias=x["alias"], name=x.get("name", ""), region=x.get("region", ""))
        for x in t.get("tke") or []
    ]
    cos = [
        ResourceItem(id=x["id"], alias=x["alias"], name=x.get("name", ""), region=x.get("region", ""))
        for x in t.get("cos") or []
    ]
    cls: list[CLSResource] = []
    for logset in t.get("cls") or []:
        cls.append(
            CLSResource(
                logsetId=logset["logsetId"],
                logsetName=logset.get("logsetName", ""),
                region=logset.get("region", ""),
                topics=[
                    CLSTopic(id=tp["id"], alias=tp["alias"], name=tp.get("name", ""))
                    for tp in logset.get("topics") or []
                ],
            )
        )

    project = ProjectSpec(
        name=project_raw.get("name") or "demo",
        description=project_raw.get("description") or "",
        target=target_name,
        transport=(data.get("server") or {}).get("transport") or "stdio",
        region=tencent.get("region") or "ap-guangzhou",
    )
    return Selection(
        project=project,
        products=list(data.get("products") or []),
        disabled_tools=list(data.get("disabledTools") or []),
        resources=BoundResources(redis=redis, cdb=cdb, tdsqlc=tdsqlc, tke=tke, cls=cls, cos=cos),
    )


def secret_pair(data: dict) -> tuple[str, str, str]:
    tencent = data.get("tencent") or {}
    return (
        tencent.get("secretId") or "",
        tencent.get("secretKey") or "",
        tencent.get("region") or "ap-guangzhou",
    )


def resolve_alias(items: list, alias: str, id_attr: str = "id"):
    for item in items:
        if getattr(item, "alias", None) == alias:
            return item
    names = [getattr(i, "alias", "") for i in items]
    raise ValueError(f"未知别名 {alias!r}，可用: {names}")
