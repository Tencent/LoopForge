from __future__ import annotations

from tcmcp_server.config import resolve_alias, secret_pair
from tcmcp_server.handlers.dispatch import register as _register
from tcmcp_server.sdk import cloud_client, fail, ok


def _inst(selection, alias: str):
    return resolve_alias(selection.resources.cdb, alias)


def handle_detail(args, data, selection):
    item = _inst(selection, args.get("instance", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cdb.v20170320 import models

    client = cloud_client("cdb", sid, skey, item.region or region)
    req = models.DescribeDBInstancesRequest()
    req.InstanceIds = [item.id]
    resp = client.DescribeDBInstances(req)
    items = resp.Items or []
    if not items:
        return fail(f"找不到实例 {item.id}")
    inst = items[0]
    return ok(
        {
            "instanceId": inst.InstanceId,
            "name": inst.InstanceName,
            "status": inst.Status,
            "engine": inst.EngineVersion,
            "vip": inst.Vip,
            "vport": inst.Vport,
            "memory": inst.Memory,
            "volume": inst.Volume,
        }
    )


def handle_databases(args, data, selection):
    item = _inst(selection, args.get("instance", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cdb.v20170320 import models

    client = cloud_client("cdb", sid, skey, item.region or region)
    req = models.DescribeDatabasesRequest()
    req.InstanceId = item.id
    resp = client.DescribeDatabases(req)
    items = [d.DatabaseName if hasattr(d, "DatabaseName") else str(d) for d in (resp.Items or [])]
    return ok({"items": items, "count": len(items)})


def handle_slow(args, data, selection):
    item = _inst(selection, args.get("instance", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cdb.v20170320 import models

    client = cloud_client("cdb", sid, skey, item.region or region)
    req = models.DescribeSlowLogsRequest()
    req.InstanceId = item.id
    if args.get("startTime"):
        req.StartTime = args["startTime"]
    if args.get("endTime"):
        req.EndTime = args["endTime"]
    if args.get("limit"):
        req.Limit = int(args["limit"])
    resp = client.DescribeSlowLogs(req)
    rows = []
    for log in resp.Items or []:
        rows.append(
            {
                "sql": getattr(log, "SqlText", None),
                "queryTime": getattr(log, "QueryTime", None),
                "database": getattr(log, "Database", None),
                "user": getattr(log, "UserHost", None),
                "timestamp": getattr(log, "Timestamp", None),
            }
        )
    return ok({"items": rows, "count": len(rows), "total": resp.TotalCount})


def handle_errors(args, data, selection):
    item = _inst(selection, args.get("instance", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cdb.v20170320 import models

    client = cloud_client("cdb", sid, skey, item.region or region)
    req = models.DescribeErrorLogDataRequest()
    req.InstanceId = item.id
    if args.get("startTime"):
        req.StartTime = args["startTime"]
    if args.get("endTime"):
        req.EndTime = args["endTime"]
    if args.get("limit"):
        req.Limit = int(args["limit"])
    resp = client.DescribeErrorLogData(req)
    rows = []
    for log in resp.Items or []:
        rows.append(
            {
                "timestamp": getattr(log, "Timestamp", None),
                "content": getattr(log, "Content", None),
            }
        )
    return ok({"items": rows, "count": len(rows), "total": getattr(resp, "TotalCount", len(rows))})


def register() -> None:
    _register("cdb_instance_detail", handle_detail)
    _register("cdb_list_databases", handle_databases)
    _register("cdb_slow_queries", handle_slow)
    _register("cdb_error_logs", handle_errors)
