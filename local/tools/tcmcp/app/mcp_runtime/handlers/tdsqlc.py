from __future__ import annotations

from tcmcp_server.config import resolve_alias, secret_pair
from tcmcp_server.handlers.dispatch import register as _register
from tcmcp_server.sdk import cloud_client, fail, ok


def _cluster(selection, alias: str):
    return resolve_alias(selection.resources.tdsqlc, alias)


def handle_detail(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cynosdb.v20190107 import models

    client = cloud_client("cynosdb", sid, skey, item.region or region)
    req = models.DescribeClusterDetailRequest()
    req.ClusterId = item.cluster_id
    resp = client.DescribeClusterDetail(req)
    d = resp.Detail
    return ok(
        {
            "clusterId": d.ClusterId,
            "name": d.ClusterName,
            "status": d.Status,
            "dbVersion": d.DbVersion,
            "vip": d.Vip,
            "vport": d.Vport,
            "configuredInstanceId": item.instance_id,
            "instanceNum": d.ServerlessInstanceNum or d.InstanceNum,
        }
    )


def handle_databases(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cynosdb.v20190107 import models

    client = cloud_client("cynosdb", sid, skey, item.region or region)
    req = models.DescribeClusterDatabasesRequest()
    req.ClusterId = item.cluster_id
    req.Limit = 100
    resp = client.DescribeClusterDatabases(req)
    items = [str(d) for d in (resp.Databases or [])]
    return ok({"items": items, "count": len(items), "total": resp.TotalCount})


def handle_tables(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cynosdb.v20190107 import models

    client = cloud_client("cynosdb", sid, skey, item.region or region)
    req = models.SearchClusterTablesRequest()
    req.ClusterId = item.cluster_id
    if args.get("database"):
        req.Database = args["database"]
    if args.get("table"):
        req.Table = args["table"]
    if args.get("tableType"):
        req.TableType = args["tableType"]
    resp = client.SearchClusterTables(req)
    items = []
    for t in resp.Tables or []:
        items.append(
            {
                "database": getattr(t, "Database", None),
                "table": getattr(t, "Table", None),
                "tableType": getattr(t, "TableType", None),
            }
        )
    return ok({"items": items, "count": len(items)})


def handle_slow(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cynosdb.v20190107 import models

    client = cloud_client("cynosdb", sid, skey, item.region or region)
    req = models.DescribeInstanceSlowQueriesRequest()
    req.InstanceId = item.instance_id
    if args.get("startTime"):
        req.StartTime = args["startTime"]
    if args.get("endTime"):
        req.EndTime = args["endTime"]
    if args.get("limit"):
        req.Limit = int(args["limit"])
    if args.get("sqlText"):
        req.Query = args["sqlText"]
    resp = client.DescribeInstanceSlowQueries(req)
    rows = []
    for q in resp.SlowQueries or []:
        rows.append(
            {
                "sql": getattr(q, "QuerySql", None) or getattr(q, "SqlText", None),
                "queryTime": getattr(q, "QueryTime", None),
                "user": getattr(q, "User", None),
                "host": getattr(q, "UserHost", None),
                "database": getattr(q, "Database", None),
                "timestamp": getattr(q, "Timestamp", None),
            }
        )
    return ok({"items": rows, "count": len(rows), "total": getattr(resp, "TotalCount", len(rows))})


def handle_errors(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cynosdb.v20190107 import models

    client = cloud_client("cynosdb", sid, skey, item.region or region)
    req = models.DescribeInstanceErrorLogsRequest()
    req.InstanceId = item.instance_id
    if args.get("startTime"):
        req.StartTime = args["startTime"]
    if args.get("endTime"):
        req.EndTime = args["endTime"]
    if args.get("limit"):
        req.Limit = int(args["limit"])
    resp = client.DescribeInstanceErrorLogs(req)
    rows = []
    for log in resp.ErrorLogs or []:
        rows.append(
            {
                "timestamp": getattr(log, "Timestamp", None),
                "content": getattr(log, "Content", None),
            }
        )
    return ok({"items": rows, "count": len(rows), "total": getattr(resp, "TotalCount", len(rows))})


def register() -> None:
    _register("db_cluster_detail", handle_detail)
    _register("db_list_databases", handle_databases)
    _register("db_search_tables", handle_tables)
    _register("tdsqlc_slow_queries", handle_slow)
    _register("tdsqlc_error_logs", handle_errors)
