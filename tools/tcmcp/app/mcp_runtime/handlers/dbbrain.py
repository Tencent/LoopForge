from __future__ import annotations

from datetime import datetime, timezone

from tcmcp_server.config import resolve_alias, secret_pair
from tcmcp_server.handlers.dispatch import register as _register
from tcmcp_server.sdk import cloud_client, fail, ok


def _instance_id(selection, alias: str, kind: str) -> tuple[str, str]:
    if kind == "cdb":
        item = resolve_alias(selection.resources.cdb, alias)
        return item.id, item.region
    item = resolve_alias(selection.resources.tdsqlc, alias)
    return item.instance_id, item.region


def _fmt(rfc: str | None) -> str | None:
    if not rfc:
        return None
    dt = datetime.fromisoformat(rfc.replace("Z", "+00:00"))
    return dt.astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M:%S")


def _client(data, region: str):
    sid, skey, fallback = secret_pair(data)
    return cloud_client("dbbrain", sid, skey, region or fallback)


def handle_health(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    start, end = _fmt(args.get("startTime")), _fmt(args.get("endTime"))
    if start and end:
        req = models.DescribeHealthScoreTimeSeriesRequest()
        req.InstanceId = instance_id
        req.Product = product
        req.StartTime = start
        req.EndTime = end
        resp = client.DescribeHealthScoreTimeSeries(req)
        return ok({"series": [x.__dict__ for x in (resp.Data or [])]})
    req = models.DescribeHealthScoreRequest()
    req.InstanceId = instance_id
    req.Product = product
    resp = client.DescribeHealthScore(req)
    d = resp.Data
    return ok({"score": getattr(d, "HealthScore", None), "level": getattr(d, "HealthLevel", None)})


def handle_events(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    req = models.DescribeDBDiagEventsRequest()
    req.InstanceIds = [instance_id]
    req.Product = product
    if args.get("startTime"):
        req.StartTime = _fmt(args["startTime"])
    if args.get("endTime"):
        req.EndTime = _fmt(args["endTime"])
    if args.get("limit"):
        req.Limit = int(args["limit"])
    resp = client.DescribeDBDiagEvents(req)
    items = []
    for ev in resp.Items or []:
        items.append(
            {
                "eventId": ev.EventId,
                "diagType": ev.DiagType,
                "severity": ev.Severity,
                "startTime": ev.StartTime,
                "outline": ev.Outline,
            }
        )
    return ok({"items": items, "count": len(items), "total": resp.TotalCount})


def handle_event_detail(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    req = models.DescribeDBDiagEventRequest()
    req.InstanceId = instance_id
    req.EventId = int(args["eventId"])
    req.Product = product
    resp = client.DescribeDBDiagEvent(req)
    return ok(
        {
            "diagItem": resp.DiagItem,
            "diagType": resp.DiagType,
            "explanation": resp.Explanation,
            "outline": resp.Outline,
            "problem": resp.Problem,
            "severity": resp.Severity,
            "suggestions": resp.Suggestions,
        }
    )


def handle_process(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    req = models.DescribeMySqlProcessListRequest()
    req.InstanceId = instance_id
    req.Product = product
    if args.get("db"):
        req.DB = args["db"]
    if args.get("user"):
        req.User = args["user"]
    if args.get("host"):
        req.Host = args["host"]
    if args.get("minSeconds"):
        req.Time = int(args["minSeconds"])
    if args.get("limit"):
        req.Limit = int(args["limit"])
    resp = client.DescribeMySqlProcessList(req)
    items = []
    for p in resp.ProcessList or []:
        items.append(
            {
                "id": p.ID,
                "user": p.User,
                "host": p.Host,
                "db": p.DB,
                "command": p.Command,
                "time": p.Time,
                "state": p.State,
                "info": p.Info,
            }
        )
    return ok({"items": items, "count": len(items)})


def handle_top_sqls(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    if not args.get("startTime") or not args.get("endTime"):
        return fail("startTime 与 endTime 必填，间隔不得超过 7 天")
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    req = models.DescribeSlowLogTopSqlsRequest()
    req.InstanceId = instance_id
    req.Product = product
    req.StartTime = _fmt(args["startTime"])
    req.EndTime = _fmt(args["endTime"])
    if args.get("limit"):
        req.Limit = int(args["limit"])
    if args.get("sortBy"):
        req.SortBy = args["sortBy"]
    resp = client.DescribeSlowLogTopSqls(req)
    items = []
    for s in resp.Rows or []:
        items.append(
            {
                "sqlTemplate": s.SqlTemplate,
                "queryTime": s.QueryTime,
                "execTimes": s.ExecTimes,
                "schema": s.Schema,
            }
        )
    return ok({"items": items, "count": len(items), "total": resp.TotalCount})


def handle_advice(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    req = models.DescribeUserSqlAdviceRequest()
    req.InstanceId = instance_id
    req.Product = product
    req.SqlText = args["sql"]
    if args.get("schema"):
        req.Schema = args["schema"]
    resp = client.DescribeUserSqlAdvice(req)
    return ok({"advice": resp.Advices, "comments": resp.Comments, "tables": resp.Tables})


def handle_space(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    req = models.DescribeTopSpaceTablesRequest()
    req.InstanceId = instance_id
    req.Product = product
    if args.get("limit"):
        req.Limit = int(args["limit"])
    if args.get("sortBy"):
        req.SortBy = args["sortBy"]
    resp = client.DescribeTopSpaceTables(req)
    items = []
    for t in resp.TopSpaceTables or []:
        items.append(
            {
                "tableName": t.TableName,
                "tableSchema": t.TableSchema,
                "dataLength": t.DataLength,
                "indexLength": t.IndexLength,
                "dataFree": t.DataFree,
                "totalLength": t.TotalLength,
            }
        )
    return ok({"items": items, "count": len(items)})


def handle_series(args, data, selection, *, product: str, kind: str):
    instance_id, region = _instance_id(selection, args.get("instance", ""), kind)
    from tencentcloud.dbbrain.v20210527 import models

    client = _client(data, region)
    req = models.DescribeSlowLogTimeSeriesStatsRequest()
    req.InstanceId = instance_id
    req.Product = product
    req.StartTime = _fmt(args["startTime"])
    req.EndTime = _fmt(args["endTime"])
    resp = client.DescribeSlowLogTimeSeriesStats(req)
    return ok(
        {
            "period": resp.Period,
            "timeSeries": [x.__dict__ for x in (resp.TimeSeries or [])],
        }
    )


def _bind(name: str, fn, product: str, kind: str):
    _register(name, lambda args, data, selection, fn=fn, product=product, kind=kind: fn(args, data, selection, product=product, kind=kind))


def register(products: list[str]) -> None:
    if "tdsqlc" in products:
        _bind("dbbrain_health_score", handle_health, "cynosdb", "tdsqlc")
        _bind("dbbrain_diag_events", handle_events, "cynosdb", "tdsqlc")
        _bind("dbbrain_diag_event_detail", handle_event_detail, "cynosdb", "tdsqlc")
        _bind("dbbrain_process_list", handle_process, "cynosdb", "tdsqlc")
        _bind("dbbrain_slow_top_sqls", handle_top_sqls, "cynosdb", "tdsqlc")
        _bind("dbbrain_sql_advice", handle_advice, "cynosdb", "tdsqlc")
        _bind("dbbrain_top_space_tables", handle_space, "cynosdb", "tdsqlc")
        _bind("dbbrain_slow_log_time_series", handle_series, "cynosdb", "tdsqlc")
    if "cdb" in products:
        _bind("cdb_dbbrain_health_score", handle_health, "mysql", "cdb")
        _bind("cdb_dbbrain_diag_events", handle_events, "mysql", "cdb")
        _bind("cdb_dbbrain_diag_event_detail", handle_event_detail, "mysql", "cdb")
        _bind("cdb_dbbrain_process_list", handle_process, "mysql", "cdb")
        _bind("cdb_dbbrain_slow_top_sqls", handle_top_sqls, "mysql", "cdb")
        _bind("cdb_dbbrain_sql_advice", handle_advice, "mysql", "cdb")
