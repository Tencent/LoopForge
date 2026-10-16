"""Materialize MCP tools/list from a Selection. Used by UI and the generated server."""

from __future__ import annotations

from typing import Any

from app.catalog import TOOL_WARN_THRESHOLD, ToolSpec, tools_for_products
from app.models import BoundResources, Selection


def aliases_for(resources: BoundResources, kind: str) -> list[str]:
    if kind == "redis":
        return [r.alias for r in resources.redis if r.alias]
    if kind == "cdb":
        return [r.alias for r in resources.cdb if r.alias]
    if kind == "tdsqlc":
        return [r.alias for r in resources.tdsqlc if r.alias]
    if kind == "tke":
        return [r.alias for r in resources.tke if r.alias]
    if kind == "cos":
        return [r.alias for r in resources.cos if r.alias]
    if kind in ("cls", "topics"):
        out: list[str] = []
        for logset in resources.cls:
            for topic in logset.topics:
                if topic.alias:
                    out.append(topic.alias)
        return out
    return []


def has_resources(resources: BoundResources, kind: str) -> bool:
    return bool(aliases_for(resources, kind))


def tool_ready(tool: ToolSpec, products: list[str], resources: BoundResources) -> tuple[bool, str]:
    if tool.requires_products:
        missing = [p for p in tool.requires_products if p not in products]
        if missing:
            return False, f"需要同时启用产品: {', '.join(missing)}"
    for kind in tool.requires_resources:
        if not has_resources(resources, kind):
            return False, f"缺少已绑定的 {kind} 资源，生成时不会注册"
    if tool.name in ("k8s_events", "k8s_audit"):
        topics = aliases_for(resources, "topics")
        need = "events" if tool.name == "k8s_events" else "audit"
        if need not in topics:
            return False, f"需要绑定别名为 {need} 的 CLS 主题"
    return True, ""


def input_schema(tool: ToolSpec, selection: Selection) -> dict[str, Any]:
    props: dict[str, Any] = {}
    required: list[str] = []
    target_name = selection.project.target or "prod"
    for param in tool.params:
        schema: dict[str, Any] = {"type": param.type, "description": param.description}
        if param.type == "array":
            schema["items"] = {"type": param.items_type or "string"}
        values: list[str] | None = None
        if param.enum:
            values = list(param.enum)
        elif param.enum_from == "targets":
            values = [target_name]
        elif param.enum_from:
            values = aliases_for(selection.resources, param.enum_from)
        if values:
            schema["enum"] = values
        if param.default is not None:
            schema["default"] = param.default
        props[param.name] = schema
        if param.required:
            required.append(param.name)
    out: dict[str, Any] = {"type": "object", "properties": props}
    if required:
        out["required"] = required
    return out


def materialize(selection: Selection) -> dict[str, Any]:
    """Return tools/list-shaped payload plus grouping metadata for the UI."""
    products = list(dict.fromkeys(selection.products))
    disabled = set(selection.disabled_tools)
    candidates = tools_for_products(products)
    tools: list[dict[str, Any]] = []
    skipped: list[dict[str, Any]] = []
    registered: list[str] = []

    for spec in candidates:
        ready, reason = tool_ready(spec, products, selection.resources)
        enabled = spec.name not in disabled
        item = {
            "name": spec.name,
            "description": spec.description,
            "product": spec.product,
            "inputSchema": input_schema(spec, selection),
            "annotations": {"readOnlyHint": spec.read_only},
            "enabled": enabled,
            "ready": ready,
            "skipReason": "" if ready else reason,
        }
        if enabled and ready:
            tools.append(item)
            registered.append(spec.name)
        else:
            skipped.append(item)

    grouped: dict[str, list[dict[str, Any]]] = {}
    for item in tools + skipped:
        grouped.setdefault(item["product"], []).append(item)

    instructions = _instructions(products, registered)
    return {
        "instructions": instructions,
        "tools": tools,
        "skipped": skipped,
        "grouped": grouped,
        "registered": registered,
        "counts": {
            "products": len(products),
            "resources": _resource_count(selection.resources),
            "tools": len(tools),
            "threshold": TOOL_WARN_THRESHOLD,
        },
        "warn": len(tools) > TOOL_WARN_THRESHOLD,
        "warnMessage": (
            f"将生成 {len(tools)} 个工具，超过 {TOOL_WARN_THRESHOLD} 容易让模型幻觉。"
            "建议少选产品或在右侧关掉不常用工具。"
            if len(tools) > TOOL_WARN_THRESHOLD
            else ""
        ),
    }


def _resource_count(resources: BoundResources) -> int:
    n = (
        len(resources.redis)
        + len(resources.cdb)
        + len(resources.tdsqlc)
        + len(resources.tke)
        + len(resources.cos)
    )
    for logset in resources.cls:
        n += len(logset.topics)
    return n


def _instructions(products: list[str], tools: list[str]) -> str:
    lines = [
        "腾讯云运维 MCP（只读云 API）。资源 ID 已绑定在 config，工具入参只用别名。",
        "时间参数用 RFC3339。limit 超上限会报错，不会静默截断。",
    ]
    if "tdsqlc" in products:
        lines.append(
            "TDSQL-C：库表用 db_*；慢 SQL 用 tdsqlc_slow_queries 看明细、"
            "dbbrain_slow_top_sqls 看模板聚合。DBBrain Product 已固定为 cynosdb。"
        )
    if "cdb" in products:
        lines.append("CDB：cdb_* 工具，DBBrain Product 已固定为 mysql。")
    if "cls" in products:
        lines.append("日志：cls_search_log / cls_histogram；topic 必须用已绑定别名。")
    if "tke" in products:
        lines.append(
            "TKE：tke_list_pods + tke_pod_logs 看当前 Pod 日志；"
            "tke_list_nodes / tke_list_addons 看节点和组件。"
        )
    if "tke" in products and "cls" in products:
        lines.append("容器日志与 k8s 事件走 CLS 历史数据，不是实时 tail。")
    if "redis" in products:
        lines.append("Redis 只读云 API，不是 redis-cli 数据面。")
    if "cos" in products:
        lines.append("COS 只返回桶、对象列表和对象元数据，不下载对象内容。")
    if tools:
        lines.append("已注册工具: " + ", ".join(tools))
    return "\n".join(lines)
