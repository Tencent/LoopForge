"""Draft a resource-scoped CAM policy for the selected cloud objects."""

from __future__ import annotations

from typing import Any

from app.models import Selection


def cam_policy(selection: Selection) -> dict[str, Any]:
    region = selection.project.region or "ap-guangzhou"
    statements: list[dict[str, Any]] = []
    products = set(selection.products)

    if "tdsqlc" in products:
        resources = [
            f"qcs::cynosdb:{r.region or region}:uin/$uin:instance/{r.cluster_id}"
            for r in selection.resources.tdsqlc
        ]
        if resources:
            statements.append(
                {
                    "_comment": "cynosdb 资源六段式填集群 ID，资源类型名是 instance。",
                    "effect": "allow",
                    "action": [
                        "cynosdb:DescribeClusterDatabases",
                        "cynosdb:SearchClusterTables",
                        "cynosdb:DescribeClusterDetail",
                        "cynosdb:DescribeInstances",
                        "cynosdb:DescribeInstanceSlowQueries",
                        "cynosdb:DescribeInstanceErrorLogs",
                    ],
                    "resource": resources,
                }
            )
            inst = [
                f"qcs::dbbrain:{r.region or region}:uin/$uin:instanceId/{r.instance_id}"
                for r in selection.resources.tdsqlc
            ]
            statements.append(
                {
                    "_comment": "DBBrain 资源级接口，填实例 ID。",
                    "effect": "allow",
                    "action": [
                        "dbbrain:DescribeMySqlProcessList",
                        "dbbrain:DescribeSlowLogTopSqls",
                        "dbbrain:DescribeSlowLogTimeSeriesStats",
                        "dbbrain:DescribeUserSqlAdvice",
                        "dbbrain:DescribeHealthScoreTimeSeries",
                    ],
                    "resource": inst,
                }
            )
            statements.append(
                {
                    "_comment": "DBBrain 操作级接口，resource 只能是 *。",
                    "effect": "allow",
                    "action": [
                        "dbbrain:DescribeDiagDBInstances",
                        "dbbrain:DescribeDBDiagEvents",
                        "dbbrain:DescribeDBDiagEvent",
                        "dbbrain:DescribeHealthScore",
                        "dbbrain:DescribeTopSpaceTables",
                    ],
                    "resource": ["*"],
                }
            )

    if "cdb" in products:
        resources = [
            f"qcs::cdb:{r.region or region}:uin/$uin:instanceId/{r.id}"
            for r in selection.resources.cdb
        ]
        if resources:
            statements.append(
                {
                    "effect": "allow",
                    "action": [
                        "cdb:DescribeDBInstances",
                        "cdb:DescribeDatabases",
                        "cdb:DescribeSlowLogs",
                        "cdb:DescribeErrorLog",
                    ],
                    "resource": resources,
                }
            )
            statements.append(
                {
                    "effect": "allow",
                    "action": [
                        "dbbrain:DescribeMySqlProcessList",
                        "dbbrain:DescribeSlowLogTopSqls",
                        "dbbrain:DescribeUserSqlAdvice",
                        "dbbrain:DescribeHealthScoreTimeSeries",
                        "dbbrain:DescribeDBDiagEvents",
                        "dbbrain:DescribeDBDiagEvent",
                        "dbbrain:DescribeHealthScore",
                    ],
                    "resource": ["*"],
                }
            )

    if "redis" in products:
        resources = [
            f"qcs::redis:{r.region or region}:uin/$uin:instance/{r.id}"
            for r in selection.resources.redis
        ]
        if resources:
            statements.append(
                {
                    "effect": "allow",
                    "action": [
                        "redis:DescribeInstances",
                        "redis:DescribeSlowLog",
                        "redis:DescribeInstanceParams",
                    ],
                    "resource": resources,
                }
            )

    if "tke" in products:
        resources = [
            f"qcs::tke:{r.region or region}:uin/$uin:cluster/{r.id}"
            for r in selection.resources.tke
        ]
        if resources:
            statements.append(
                {
                    "effect": "allow",
                    "action": [
                        "tke:DescribeClusters",
                        "tke:DescribeClusterStatus",
                        "tke:DescribeClusterInstances",
                        "tke:DescribeClusterNodePools",
                        "tke:DescribeClusterNodePoolDetail",
                        "tke:DescribeNodePools",
                        "tke:DescribeAddon",
                        "tke:DescribeClusterEndpointStatus",
                        "tke:DescribeLogSwitches",
                        "tke:ForwardApplicationRequestV3",
                    ],
                    "resource": resources,
                }
            )

    if "cls" in products:
        topic_ids: list[str] = []
        for logset in selection.resources.cls:
            topic_ids.extend(t.id for t in logset.topics)
        resources = [f"qcs::cls:{region}:uin/$uin:topic/{tid}" for tid in topic_ids]
        if resources:
            statements.append(
                {
                    "effect": "allow",
                    "action": [
                        "cls:SearchLog",
                        "cls:DescribeTopics",
                        "cls:DescribeIndex",
                        "cls:DescribeLogHistogram",
                    ],
                    "resource": resources,
                }
            )

    if "cos" in products:
        resources: list[str] = []
        for bucket in selection.resources.cos:
            appid = bucket.id.rsplit("-", 1)[-1]
            resources.append(
                f"qcs::cos:{bucket.region or region}:uid/{appid}:{bucket.id}/*"
            )
        if resources:
            statements.append(
                {
                    "_comment": "COS 桶名须包含 APPID；只授权列对象、检查桶和读取对象元数据。",
                    "effect": "allow",
                    "action": [
                        "cos:HeadBucket",
                        "cos:GetBucket",
                        "cos:HeadObject",
                    ],
                    "resource": resources,
                }
            )

    return {"version": "2.0", "statement": statements}
