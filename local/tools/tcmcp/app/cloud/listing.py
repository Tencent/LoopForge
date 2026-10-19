"""Describe* wrappers. Secrets never leave the request scope."""

from __future__ import annotations

from typing import Any, Callable

from app.models import (
    CloudCreds,
    ListedLogset,
    ListedResource,
    ListedTopic,
    ResourceCatalog,
)


def _client(product: str, secret_id: str, secret_key: str, region: str):
    from tencentcloud.common import credential
    from tencentcloud.common.profile.client_profile import ClientProfile
    from tencentcloud.common.profile.http_profile import HttpProfile

    cred = credential.Credential(secret_id, secret_key)
    http = HttpProfile(endpoint=f"{product}.tencentcloudapi.com")
    profile = ClientProfile(httpProfile=http)
    if product == "redis":
        from tencentcloud.redis.v20180412.redis_client import RedisClient

        return RedisClient(cred, region, profile)
    if product == "cdb":
        from tencentcloud.cdb.v20170320.cdb_client import CdbClient

        return CdbClient(cred, region, profile)
    if product == "cynosdb":
        from tencentcloud.cynosdb.v20190107.cynosdb_client import CynosdbClient

        return CynosdbClient(cred, region, profile)
    if product == "tke":
        from tencentcloud.tke.v20180525.tke_client import TkeClient

        return TkeClient(cred, region, profile)
    if product == "cls":
        from tencentcloud.cls.v20201016.cls_client import ClsClient

        return ClsClient(cred, region, profile)
    raise ValueError(f"unknown product {product}")


def _safe(label: str, errors: dict[str, str], fn: Callable[[], Any]) -> Any:
    try:
        return fn()
    except Exception as exc:  # noqa: BLE001 — surface any SDK/auth error to the UI
        errors[label] = str(exc)
        return None


def _cos_client(secret_id: str, secret_key: str, region: str):
    from qcloud_cos import CosConfig, CosS3Client

    config = CosConfig(Region=region, SecretId=secret_id, SecretKey=secret_key, Scheme="https")
    return CosS3Client(config)


def list_redis(creds: CloudCreds) -> list[ListedResource]:
    from tencentcloud.redis.v20180412 import models

    client = _client("redis", creds.secret_id, creds.secret_key, creds.region)
    req = models.DescribeInstancesRequest()
    req.Limit = 100
    if creds.project_id is not None:
        req.ProjectIds = [creds.project_id]
    resp = client.DescribeInstances(req)
    out: list[ListedResource] = []
    for inst in resp.InstanceSet or []:
        pid = int(inst.ProjectId) if getattr(inst, "ProjectId", None) is not None else None
        out.append(
            ListedResource(
                id=inst.InstanceId or "",
                name=inst.InstanceName or inst.InstanceId or "",
                region=getattr(inst, "Region", None) or creds.region,
                projectId=pid,
                extra={"status": inst.Status, "size": inst.Size, "engine": inst.Type},
            )
        )
    return out


def list_cdb(creds: CloudCreds) -> list[ListedResource]:
    from tencentcloud.cdb.v20170320 import models

    client = _client("cdb", creds.secret_id, creds.secret_key, creds.region)
    req = models.DescribeDBInstancesRequest()
    req.Limit = 100
    if creds.project_id is not None:
        req.ProjectId = creds.project_id
    resp = client.DescribeDBInstances(req)
    out: list[ListedResource] = []
    for inst in resp.Items or []:
        out.append(
            ListedResource(
                id=inst.InstanceId or "",
                name=inst.InstanceName or inst.InstanceId or "",
                region=inst.Region or creds.region,
                projectId=int(inst.ProjectId) if inst.ProjectId is not None else None,
                extra={
                    "status": inst.Status,
                    "engine": inst.EngineVersion,
                    "vip": inst.Vip,
                    "instanceType": inst.InstanceType,
                },
            )
        )
    return out


def list_tdsqlc(creds: CloudCreds) -> list[ListedResource]:
    from tencentcloud.cynosdb.v20190107 import models

    client = _client("cynosdb", creds.secret_id, creds.secret_key, creds.region)
    req = models.DescribeClustersRequest()
    req.Limit = 100
    if creds.project_id is not None:
        filt = models.QueryFilter()
        filt.Names = ["ProjectId"]
        filt.Values = [str(creds.project_id)]
        filt.ExactMatch = True
        req.Filters = [filt]
    resp = client.DescribeClusters(req)
    out: list[ListedResource] = []
    for cluster in resp.ClusterSet or []:
        instance_id = ""
        members = getattr(cluster, "InstanceSet", None) or []
        for member in members:
            role = (getattr(member, "InstanceRole", None) or "").lower()
            if role in ("master", "rw", "primary"):
                instance_id = member.InstanceId or ""
                break
        if not instance_id and members:
            instance_id = members[0].InstanceId or ""
        if not instance_id:
            instance_id = _first_cynos_instance(client, cluster.ClusterId)
        out.append(
            ListedResource(
                id=cluster.ClusterId or "",
                name=cluster.ClusterName or cluster.ClusterId or "",
                region=getattr(cluster, "Region", None) or creds.region,
                projectId=int(cluster.ProjectId) if getattr(cluster, "ProjectId", None) is not None else None,
                extra={
                    "status": cluster.Status,
                    "instanceId": instance_id,
                    "dbVersion": getattr(cluster, "DbVersion", None),
                    "vip": getattr(cluster, "Vip", None),
                },
            )
        )
    return out


def _first_cynos_instance(client, cluster_id: str) -> str:
    from tencentcloud.cynosdb.v20190107 import models

    req = models.DescribeInstancesRequest()
    filt = models.QueryFilter()
    filt.Names = ["ClusterId"]
    filt.Values = [cluster_id]
    filt.ExactMatch = True
    req.Filters = [filt]
    req.Limit = 20
    resp = client.DescribeInstances(req)
    items = resp.InstanceSet or []
    for inst in items:
        role = (getattr(inst, "InstanceRole", None) or "").lower()
        if role in ("master", "rw", "primary"):
            return inst.InstanceId or ""
    return items[0].InstanceId if items else ""


def list_tke(creds: CloudCreds) -> list[ListedResource]:
    from tencentcloud.tke.v20180525 import models

    client = _client("tke", creds.secret_id, creds.secret_key, creds.region)
    req = models.DescribeClustersRequest()
    req.Limit = 100
    if creds.project_id is not None:
        filt = models.Filter()
        filt.Name = "ProjectId"
        filt.Values = [str(creds.project_id)]
        req.Filters = [filt]
    resp = client.DescribeClusters(req)
    out: list[ListedResource] = []
    for cluster in resp.Clusters or []:
        pid = None
        if getattr(cluster, "ProjectId", None) not in (None, ""):
            try:
                pid = int(cluster.ProjectId)
            except (TypeError, ValueError):
                pid = None
        out.append(
            ListedResource(
                id=cluster.ClusterId or "",
                name=cluster.ClusterName or cluster.ClusterId or "",
                region=creds.region,
                projectId=pid,
                extra={
                    "status": cluster.ClusterStatus,
                    "version": cluster.ClusterVersion,
                    "type": cluster.ClusterType,
                },
            )
        )
    return out


def list_cls(creds: CloudCreds) -> list[ListedLogset]:
    from tencentcloud.cls.v20201016 import models

    client = _client("cls", creds.secret_id, creds.secret_key, creds.region)
    req = models.DescribeLogsetsRequest()
    req.Offset = 0
    req.Limit = 100
    resp = client.DescribeLogsets(req)
    out: list[ListedLogset] = []
    for logset in resp.Logsets or []:
        topics: list[ListedTopic] = []
        t_req = models.DescribeTopicsRequest()
        filt = models.Filter()
        filt.Key = "logsetId"
        filt.Values = [logset.LogsetId]
        t_req.Filters = [filt]
        t_req.Offset = 0
        t_req.Limit = 100
        t_resp = client.DescribeTopics(t_req)
        for topic in t_resp.Topics or []:
            topics.append(
                ListedTopic(
                    id=topic.TopicId or "",
                    name=topic.TopicName or topic.TopicId or "",
                    logsetId=logset.LogsetId or "",
                )
            )
        out.append(
            ListedLogset(
                id=logset.LogsetId or "",
                name=logset.LogsetName or logset.LogsetId or "",
                region=creds.region,
                topics=topics,
            )
        )
    return out


def list_cos(creds: CloudCreds) -> list[ListedResource]:
    """List COS buckets in the requested region via GET Service."""
    client = _cos_client(creds.secret_id, creds.secret_key, creds.region)
    response = client.list_buckets(Region=creds.region)
    bucket_rows = (response.get("Buckets") or {}).get("Bucket") or []
    if isinstance(bucket_rows, dict):
        bucket_rows = [bucket_rows]

    out: list[ListedResource] = []
    for bucket in bucket_rows:
        name = str(bucket.get("Name") or "")
        region = str(bucket.get("Location") or creds.region)
        if region != creds.region:
            continue
        out.append(
            ListedResource(
                id=name,
                name=name,
                region=region,
                extra={"creationDate": bucket.get("CreationDate")},
            )
        )
    return out


LISTERS = {
    "redis": list_redis,
    "cdb": list_cdb,
    "tdsqlc": list_tdsqlc,
    "tke": list_tke,
    "cls": list_cls,
    "cos": list_cos,
}


def list_resources(creds: CloudCreds) -> ResourceCatalog:
    products = creds.products or list(LISTERS)
    errors: dict[str, str] = {}
    data: dict[str, Any] = {"errors": errors}
    for product in products:
        lister = LISTERS.get(product)
        if not lister:
            errors[product] = f"unknown product {product}"
            continue
        result = _safe(product, errors, lambda lister=lister: lister(creds))
        if result is not None:
            data[product] = result
    return ResourceCatalog.model_validate(data)
