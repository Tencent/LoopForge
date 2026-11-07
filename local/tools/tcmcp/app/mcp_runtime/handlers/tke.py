from __future__ import annotations

import json
from urllib.parse import quote, urlencode

from tcmcp_server.config import resolve_alias, secret_pair
from tcmcp_server.handlers.dispatch import register as _register
from tcmcp_server.sdk import cloud_client, fail, ok


def _cluster(selection, alias: str):
    return resolve_alias(selection.resources.tke, alias)


def _client(data, selection, item):
    sid, skey, region = secret_pair(data)
    return cloud_client("tke", sid, skey, item.region or region)


def _client2022(data, selection, item):
    sid, skey, region = secret_pair(data)
    return cloud_client("tke2022", sid, skey, item.region or region)


def _regular_pool_row(p) -> dict:
    summary = getattr(p, "NodeCountSummary", None)
    manual = getattr(summary, "ManuallyAdded", None) if summary else None
    auto = getattr(summary, "AutoscalingAdded", None) if summary else None
    return {
        "nodePoolId": p.NodePoolId,
        "name": p.Name,
        "type": "Regular",
        "lifeState": p.LifeState,
        "autoscaling": bool(getattr(p, "AutoscalingGroupId", None)),
        "desired": getattr(p, "DesiredNodesNum", None),
        "min": getattr(p, "MinNodesNum", None),
        "max": getattr(p, "MaxNodesNum", None),
        "os": getattr(p, "NodePoolOs", None),
        "autoscalingGroupId": getattr(p, "AutoscalingGroupId", None),
        "autoscalingGroupStatus": getattr(p, "AutoscalingGroupStatus", None),
        "manuallyAdded": getattr(manual, "Total", None) if manual else None,
        "autoscalingAdded": getattr(auto, "Total", None) if auto else None,
    }


def _native_scaling(native) -> dict:
    scaling = getattr(native, "Scaling", None) if native else None
    return {
        "desired": getattr(native, "Replicas", None) if native else None,
        "ready": getattr(native, "ReadyReplicas", None) if native else None,
        "min": getattr(scaling, "MinReplicas", None) if scaling else None,
        "max": getattr(scaling, "MaxReplicas", None) if scaling else None,
        "autoscaling": bool(getattr(native, "EnableAutoscaling", None)) if native else False,
        "instanceTypes": list(getattr(native, "InstanceTypes", None) or []) if native else [],
        "machineType": getattr(native, "MachineType", None) if native else None,
        "chargeType": getattr(native, "InstanceChargeType", None) if native else None,
    }


def _v2022_pool_row(p) -> dict:
    extra = _native_scaling(getattr(p, "Native", None))
    return {
        "nodePoolId": p.NodePoolId,
        "name": p.Name,
        "type": p.Type,
        "lifeState": p.LifeState,
        "createdAt": getattr(p, "CreatedAt", None),
        "deletionProtection": bool(getattr(p, "DeletionProtection", None)),
        "unschedulable": bool(getattr(p, "Unschedulable", None)),
        "autoscaling": extra["autoscaling"],
        "desired": extra["desired"],
        "ready": extra["ready"],
        "min": extra["min"],
        "max": extra["max"],
        "instanceTypes": extra["instanceTypes"],
        "machineType": extra["machineType"],
        "chargeType": extra["chargeType"],
    }


def handle_detail(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    from tencentcloud.tke.v20180525 import models

    client = _client(data, selection, item)
    req = models.DescribeClustersRequest()
    req.ClusterIds = [item.id]
    resp = client.DescribeClusters(req)
    items = resp.Clusters or []
    if not items:
        return fail(f"找不到集群 {item.id}")
    c = items[0]
    return ok(
        {
            "clusterId": c.ClusterId,
            "name": c.ClusterName,
            "status": c.ClusterStatus,
            "version": c.ClusterVersion,
            "type": c.ClusterType,
            "description": c.ClusterDescription,
        }
    )


def handle_status(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    from tencentcloud.tke.v20180525 import models

    client = _client(data, selection, item)
    req = models.DescribeClusterStatusRequest()
    req.ClusterIds = [item.id]
    resp = client.DescribeClusterStatus(req)
    rows = resp.ClusterStatusSet or []
    if not rows:
        return fail(f"找不到集群状态 {item.id}")
    s = rows[0]
    return ok(
        {
            "clusterId": s.ClusterId,
            "clusterState": s.ClusterState,
            "instanceState": s.ClusterInstanceState,
            "monitor": bool(s.ClusterBMonitor),
            "runningNodes": s.ClusterRunningNodeNum,
            "failedNodes": s.ClusterFailedNodeNum,
            "initNodes": s.ClusterInitNodeNum,
            "deletionProtection": bool(s.ClusterDeletionProtection),
            "auditEnabled": bool(getattr(s, "ClusterAuditEnabled", None)),
        }
    )


def handle_nodes(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    from tencentcloud.tke.v20180525 import models

    client = _client(data, selection, item)
    req = models.DescribeClusterInstancesRequest()
    req.ClusterId = item.id
    req.Offset = int(args.get("offset") or 0)
    req.Limit = min(int(args.get("limit") or 100), 100)
    pool = (args.get("nodePoolId") or "").strip()
    if pool:
        filt = models.Filter()
        filt.Name = "nodepool-id"
        filt.Values = [pool]
        req.Filters = [filt]
    resp = client.DescribeClusterInstances(req)
    items = []
    for n in resp.InstanceSet or []:
        items.append(
            {
                "instanceId": n.InstanceId,
                "role": n.InstanceRole,
                "state": n.InstanceState,
                "lanIp": getattr(n, "LanIP", None),
                "nodePoolId": getattr(n, "NodePoolId", None),
                "drainStatus": getattr(n, "DrainStatus", None),
                "failedReason": getattr(n, "FailedReason", None),
            }
        )
    return ok({"items": items, "count": len(items), "total": resp.TotalCount})


def handle_pools(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    by_id: dict[str, dict] = {}
    errors: list[str] = []

    try:
        from tencentcloud.tke.v20220501 import models as m2022

        client = _client2022(data, selection, item)
        req = m2022.DescribeNodePoolsRequest()
        req.ClusterId = item.id
        req.Limit = 100
        resp = client.DescribeNodePools(req)
        for p in resp.NodePools or []:
            if p.NodePoolId:
                by_id[p.NodePoolId] = _v2022_pool_row(p)
    except Exception as exc:  # noqa: BLE001
        errors.append(f"DescribeNodePools: {exc}")

    try:
        from tencentcloud.tke.v20180525 import models

        client = _client(data, selection, item)
        req = models.DescribeClusterNodePoolsRequest()
        req.ClusterId = item.id
        resp = client.DescribeClusterNodePools(req)
        for p in resp.NodePoolSet or []:
            if p.NodePoolId and p.NodePoolId not in by_id:
                by_id[p.NodePoolId] = _regular_pool_row(p)
    except Exception as exc:  # noqa: BLE001
        errors.append(f"DescribeClusterNodePools: {exc}")

    payload: dict = {"items": list(by_id.values()), "count": len(by_id)}
    if not by_id and errors:
        return fail(
            "列出节点池失败: " + "; ".join(errors),
            "确认 CAM 有 tke:DescribeNodePools 和 tke:DescribeClusterNodePools",
        )
    if errors:
        payload["warnings"] = errors
    return ok(payload)


def handle_pool_detail(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    pool_id = (args.get("nodePoolId") or "").strip()
    if not pool_id:
        return fail("nodePoolId 必填")

    try:
        from tencentcloud.tke.v20180525 import models

        client = _client(data, selection, item)
        req = models.DescribeClusterNodePoolDetailRequest()
        req.ClusterId = item.id
        req.NodePoolId = pool_id
        resp = client.DescribeClusterNodePoolDetail(req)
        if resp.NodePool:
            return ok(_regular_pool_row(resp.NodePool))
    except Exception:
        pass

    try:
        from tencentcloud.tke.v20220501 import models as m2022

        client = _client2022(data, selection, item)
        req = m2022.DescribeNodePoolsRequest()
        req.ClusterId = item.id
        filt = m2022.Filter()
        filt.Name = "NodePoolsId"
        filt.Values = [pool_id]
        req.Filters = [filt]
        resp = client.DescribeNodePools(req)
        pools = [p for p in (resp.NodePools or []) if p.NodePoolId == pool_id]
        if pools:
            return ok(_v2022_pool_row(pools[0]))
    except Exception as cop_exc:  # noqa: BLE001
        return fail(f"读取节点池失败: {exc}", "确认 CAM 有 tke:DescribeNodePools")

    return fail(f"找不到节点池 {pool_id}")


def handle_addons(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    from tencentcloud.tke.v20180525 import models

    client = _client(data, selection, item)
    req = models.DescribeAddonRequest()
    req.ClusterId = item.id
    if args.get("addon"):
        req.AddonName = args["addon"]
    resp = client.DescribeAddon(req)
    items = []
    for a in resp.Addons or []:
        items.append(
            {
                "name": a.AddonName,
                "version": a.AddonVersion,
                "latest": getattr(a, "LatestVersion", None),
                "phase": a.Phase,
                "reason": getattr(a, "Reason", None),
            }
        )
    return ok({"items": items, "count": len(items)})


def handle_endpoint(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    from tencentcloud.tke.v20180525 import models

    client = _client(data, selection, item)

    def _status(extranet: bool) -> str:
        req = models.DescribeClusterEndpointStatusRequest()
        req.ClusterId = item.id
        req.IsExtranet = extranet
        return client.DescribeClusterEndpointStatus(req).Status or "Unknown"

    intranet = _status(False)
    internet = _status(True)
    return ok(
        {
            "intranet": intranet,
            "internet": internet,
            "intranetReady": str(intranet).lower() == "created",
            "internetReady": str(internet).lower() == "created",
        }
    )


def _switch_info(info):
    if not info:
        return None
    return {
        "enabled": getattr(info, "Enable", None),
        "logsetId": getattr(info, "LogsetId", None),
        "topicId": getattr(info, "TopicId", None),
        "status": getattr(info, "Status", None),
    }


def handle_log_switches(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    from tencentcloud.tke.v20180525 import models

    client = _client(data, selection, item)
    req = models.DescribeLogSwitchesRequest()
    req.ClusterIds = [item.id]
    resp = client.DescribeLogSwitches(req)
    rows = resp.SwitchSet or []
    if not rows:
        return ok({"clusterId": item.id, "audit": None, "event": None, "log": None})
    sw = rows[0]
    return ok(
        {
            "clusterId": sw.ClusterId or item.id,
            "audit": _switch_info(sw.Audit),
            "event": _switch_info(sw.Event),
            "log": _switch_info(sw.Log),
            "masterLog": _switch_info(getattr(sw, "MasterLog", None)),
        }
    )


def _k8s_get(client, cluster_id: str, path: str) -> str:
    payload = client.call_json(
        "ForwardApplicationRequestV3",
        {"ClusterName": cluster_id, "Method": "GET", "Path": path},
    )
    resp = payload.get("Response") or payload
    err = resp.get("Error")
    if err:
        raise ValueError(err.get("Message") or str(err))
    return resp.get("ResponseBody") or ""


def _pod_row(pod: dict) -> dict:
    meta = pod.get("metadata") or {}
    spec = pod.get("spec") or {}
    status = pod.get("status") or {}
    containers = []
    restarts = 0
    ready = 0
    total = 0
    for cs in status.get("containerStatuses") or []:
        total += 1
        if cs.get("ready"):
            ready += 1
        restarts += int(cs.get("restartCount") or 0)
        containers.append(cs.get("name"))
    if not containers:
        containers = [c.get("name") for c in (spec.get("containers") or []) if c.get("name")]
        total = len(containers)
    return {
        "name": meta.get("name"),
        "namespace": meta.get("namespace"),
        "phase": status.get("phase"),
        "node": spec.get("nodeName"),
        "ready": f"{ready}/{total}" if total else status.get("phase"),
        "restarts": restarts,
        "containers": containers,
    }


def handle_list_pods(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    client = _client(data, selection, item)
    ns = (args.get("namespace") or "").strip()
    path = f"/api/v1/namespaces/{quote(ns, safe='')}/pods" if ns else "/api/v1/pods"
    query: dict[str, str] = {}
    if args.get("labelSelector"):
        query["labelSelector"] = args["labelSelector"]
    limit = int(args.get("limit") or 100)
    if limit > 200:
        return fail("limit 上限 200", "缩小 limit 后重试")
    query["limit"] = str(limit)
    path = f"{path}?{urlencode(query)}"
    try:
        raw = _k8s_get(client, item.id, path)
        body = json.loads(raw) if raw else {}
    except Exception as e:
        return fail(f"列出 Pod 失败: {e}", "确认 CAM 有 tke:ForwardApplicationRequestV3，且集群 apiserver 可访问")
    items = [_pod_row(p) for p in (body.get("items") or [])]
    return ok({"items": items, "count": len(items)})


def handle_pod_logs(args, data, selection):
    item = _cluster(selection, args.get("cluster", ""))
    pod = (args.get("pod") or "").strip()
    if not pod:
        return fail("pod 必填")
    ns = (args.get("namespace") or "default").strip() or "default"
    tail = min(int(args.get("tailLines") or 200), 500)
    query = {"timestamps": "true", "tailLines": str(tail)}
    if args.get("container"):
        query["container"] = args["container"]
    if str(args.get("previous") or "").lower() == "true":
        query["previous"] = "true"
    path = f"/api/v1/namespaces/{quote(ns, safe='')}/pods/{quote(pod, safe='')}/log?{urlencode(query)}"
    client = _client(data, selection, item)
    try:
        text = _k8s_get(client, item.id, path)
    except Exception as e:
        return fail(
            f"读取 Pod 日志失败: {e}",
            "先用 tke_list_pods 确认 namespace / pod / container；多容器必须传 container",
        )
    truncated = False
    if len(text) > 200_000:
        text = text[-200_000:]
        truncated = True
    return ok(
        {
            "pod": pod,
            "namespace": ns,
            "container": args.get("container") or "",
            "tailLines": tail,
            "truncated": truncated,
            "log": text,
        }
    )


def register() -> None:
    _register("tke_cluster_detail", handle_detail)
    _register("tke_cluster_status", handle_status)
    _register("tke_list_nodes", handle_nodes)
    _register("tke_list_node_pools", handle_pools)
    _register("tke_node_pool_detail", handle_pool_detail)
    _register("tke_list_addons", handle_addons)
    _register("tke_endpoint_status", handle_endpoint)
    _register("tke_log_switches", handle_log_switches)
    _register("tke_list_pods", handle_list_pods)
    _register("tke_pod_logs", handle_pod_logs)
