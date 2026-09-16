from __future__ import annotations

from tcmcp_server.config import secret_pair
from tcmcp_server.handlers.dispatch import register as _register
from tcmcp_server.sdk import cloud_client, fail, ok


def _topics(selection) -> list:
    out = []
    for logset in selection.resources.cls:
        for topic in logset.topics:
            out.append((topic, logset))
    return out


def _topic(selection, alias: str):
    for topic, logset in _topics(selection):
        if topic.alias == alias:
            return topic, logset
    names = [t.alias for t, _ in _topics(selection)]
    raise ValueError(f"未知 topic 别名 {alias!r}，可用: {names}")


def handle_list_topics(args, data, selection):
    items = [
        {"alias": t.alias, "id": t.id, "name": t.name, "logsetId": ls.logset_id}
        for t, ls in _topics(selection)
    ]
    return ok({"items": items, "count": len(items)})


def handle_search(args, data, selection):
    topic, logset = _topic(selection, args.get("topic", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cls.v20201016 import models

    client = cloud_client("cls", sid, skey, logset.region or region)
    req = models.SearchLogRequest()
    req.TopicId = topic.id
    req.Query = args.get("query") or "*"
    req.From = _ms(args.get("startTime"), hours=1)
    req.To = _ms(args.get("endTime"), hours=0)
    req.Limit = int(args.get("limit") or 100)
    if req.Limit > 1000:
        return fail("limit 上限 1000", "缩小 limit 后重试")
    req.Sort = args.get("sort") or "desc"
    resp = client.SearchLog(req)
    results = []
    for item in resp.Results or []:
        results.append(
            {
                "time": getattr(item, "Time", None),
                "source": getattr(item, "Source", None),
                "logJson": getattr(item, "LogJson", None),
            }
        )
    return ok({"items": results, "count": len(results), "topic": topic.alias})


def handle_index(args, data, selection):
    topic, logset = _topic(selection, args.get("topic", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cls.v20201016 import models

    client = cloud_client("cls", sid, skey, logset.region or region)
    req = models.DescribeIndexRequest()
    req.TopicId = topic.id
    resp = client.DescribeIndex(req)
    rule = resp.Rule
    fields = []
    if rule and rule.KeyValue and rule.KeyValue.KeyValues:
        for kv in rule.KeyValue.KeyValues:
            fields.append({"key": kv.Key, "type": getattr(kv.Value, "Type", None)})
    return ok({"topic": topic.alias, "fields": fields, "count": len(fields)})


def handle_histogram(args, data, selection):
    topic, logset = _topic(selection, args.get("topic", ""))
    sid, skey, region = secret_pair(data)
    from tencentcloud.cls.v20201016 import models

    client = cloud_client("cls", sid, skey, logset.region or region)
    req = models.DescribeLogHistogramRequest()
    req.TopicId = topic.id
    req.Query = args.get("query") or "*"
    req.From = _ms(args.get("startTime"), hours=1)
    req.To = _ms(args.get("endTime"), hours=0)
    if args.get("interval"):
        req.Interval = args["interval"]
    resp = client.DescribeLogHistogram(req)
    buckets = []
    for b in resp.HistogramInfos or []:
        buckets.append({"time": b.BTime, "count": b.Count})
    return ok({"topic": topic.alias, "items": buckets, "count": len(buckets)})


def _container_query(args, extra: str) -> str:
    parts = [p for p in (extra, args.get("query")) if p]
    if args.get("workload"):
        parts.append(f'container:{args["workload"]}')
    if args.get("pod"):
        parts.append(f'pod:{args["pod"]}')
    return " AND ".join(parts) if parts else "*"


def handle_container(args, data, selection):
    args = dict(args)
    aliases = [t.alias for t, _ in _topics(selection)]
    args["topic"] = "api" if "api" in aliases else aliases[0]
    args["query"] = _container_query(args, "")
    return handle_search(args, data, selection)


def handle_events(args, data, selection):
    args = dict(args)
    args["topic"] = "events"
    return handle_search(args, data, selection)


def handle_audit(args, data, selection):
    args = dict(args)
    args["topic"] = "audit"
    return handle_search(args, data, selection)


def _ms(rfc: str | None, hours: int) -> int:
    from datetime import datetime, timedelta, timezone

    if rfc:
        dt = datetime.fromisoformat(rfc.replace("Z", "+00:00"))
    else:
        dt = datetime.now(timezone.utc) - timedelta(hours=hours)
    return int(dt.timestamp() * 1000)


def register() -> None:
    _register("cls_search_log", handle_search)
    _register("cls_list_topics", handle_list_topics)
    _register("cls_index_fields", handle_index)
    _register("cls_histogram", handle_histogram)
    _register("container_logs", handle_container)
    _register("k8s_events", handle_events)
    _register("k8s_audit", handle_audit)
