from __future__ import annotations

from tcmcp_server.config import resolve_alias, secret_pair
from tcmcp_server.handlers.dispatch import register as _register
from tcmcp_server.sdk import cloud_client, fail, ok


def _inst(selection, alias: str):
    return resolve_alias(selection.resources.redis, alias)


def handle_detail(args, data, selection):
    item = _inst(selection, args.get("instance", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.redis.v20180412 import models

    client = cloud_client("redis", sid, skey, item.region or region)
    req = models.DescribeInstancesRequest()
    req.InstanceId = item.id
    resp = client.DescribeInstances(req)
    items = resp.InstanceSet or []
    if not items:
        return fail(f"找不到实例 {item.id}")
    inst = items[0]
    return ok(
        {
            "instanceId": inst.InstanceId,
            "name": inst.InstanceName,
            "status": inst.Status,
            "size": inst.Size,
            "type": inst.Type,
            "vip": getattr(inst, "WanIp", None) or getattr(inst, "Vip", None),
        }
    )


def handle_slow(args, data, selection):
    item = _inst(selection, args.get("instance", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.redis.v20180412 import models

    client = cloud_client("redis", sid, skey, item.region or region)
    req = models.DescribeSlowLogRequest()
    req.InstanceId = item.id
    if args.get("startTime"):
        req.BeginTime = args["startTime"]
    if args.get("endTime"):
        req.EndTime = args["endTime"]
    if args.get("limit"):
        req.Limit = int(args["limit"])
    resp = client.DescribeSlowLog(req)
    rows = []
    for log in resp.InstanceSlowlogDetail or []:
        rows.append(
            {
                "command": getattr(log, "Command", None),
                "duration": getattr(log, "Duration", None),
                "executeTime": getattr(log, "ExecuteTime", None),
                "client": getattr(log, "Client", None),
            }
        )
    return ok({"items": rows, "count": len(rows), "total": resp.TotalCount})


def handle_params(args, data, selection):
    item = _inst(selection, args.get("instance", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.redis.v20180412 import models

    client = cloud_client("redis", sid, skey, item.region or region)
    req = models.DescribeInstanceParamsRequest()
    req.InstanceId = item.id
    resp = client.DescribeInstanceParams(req)
    items = []
    for group in resp.InstanceEnumParam or []:
        items.append({"name": group.ParamName, "value": group.CurrentValue, "kind": "enum"})
    for group in resp.InstanceIntegerParam or []:
        items.append({"name": group.ParamName, "value": group.CurrentValue, "kind": "int"})
    for group in resp.InstanceTextParam or []:
        items.append({"name": group.ParamName, "value": group.CurrentValue, "kind": "text"})
    return ok({"items": items, "count": len(items)})


def register() -> None:
    _register("redis_instance_detail", handle_detail)
    _register("redis_slow_logs", handle_slow)
    _register("redis_params", handle_params)
