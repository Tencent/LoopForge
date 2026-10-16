from __future__ import annotations

from tcmcp_server.preview import aliases_for, materialize


def handle_ping(args: dict, data: dict, selection) -> dict:
    preview = materialize(selection)
    target = args.get("target") or selection.project.target
    return {
        "ok": True,
        "service": selection.project.name,
        "target": target,
        "products": selection.products,
        "tools": preview["registered"],
        "resources": {
            "redis": aliases_for(selection.resources, "redis"),
            "cdb": aliases_for(selection.resources, "cdb"),
            "tdsqlc": aliases_for(selection.resources, "tdsqlc"),
            "tke": aliases_for(selection.resources, "tke"),
            "topics": aliases_for(selection.resources, "topics"),
        },
    }
