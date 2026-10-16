from __future__ import annotations

from typing import Any, Callable

Handler = Callable[[dict, dict, Any], Any]

REGISTRY: dict[str, Handler] = {}


def register(name: str, fn: Handler) -> None:
    REGISTRY[name] = fn


def call(name: str, args: dict, data: dict, selection) -> Any:
    fn = REGISTRY.get(name)
    if fn is None:
        raise ValueError(f"未注册的工具 {name}")
    return fn(args, data, selection)


def load_handlers(products: list[str]) -> None:
    from tcmcp_server.handlers import ping

    register("ping", ping.handle_ping)
    if "redis" in products:
        from tcmcp_server.handlers import redis as redis_h

        redis_h.register()
    if "cdb" in products:
        from tcmcp_server.handlers import cdb as cdb_h

        cdb_h.register()
    if "tdsqlc" in products:
        from tcmcp_server.handlers import tdsqlc as tdsqlc_h

        tdsqlc_h.register()
    if "tke" in products:
        from tcmcp_server.handlers import tke as tke_h

        tke_h.register()
    if "cls" in products:
        from tcmcp_server.handlers import cls as cls_h

        cls_h.register()
    if "cos" in products:
        from tcmcp_server.handlers import cos as cos_h

        cos_h.register()
    if "cdb" in products or "tdsqlc" in products:
        from tcmcp_server.handlers import dbbrain as dbbrain_h

        dbbrain_h.register(products)
