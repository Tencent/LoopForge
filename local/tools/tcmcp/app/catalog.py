"""Product → tool catalog. Single source of truth for UI, preview, and zip."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Literal

from app.regions import public_regions

ParamType = Literal["string", "integer", "array"]
EnumFrom = Literal[
    "targets",
    "topics",
    "redis",
    "cdb",
    "tdsqlc",
    "tke",
    "cos",
]


@dataclass(frozen=True)
class Param:
    name: str
    type: ParamType
    description: str
    required: bool = False
    enum: tuple[str, ...] | None = None
    enum_from: EnumFrom | None = None
    default: Any = None
    items_type: str | None = None


@dataclass(frozen=True)
class ToolSpec:
    name: str
    product: str
    description: str
    params: tuple[Param, ...] = ()
    requires_resources: tuple[str, ...] = ()
    requires_products: tuple[str, ...] = ()
    read_only: bool = True


@dataclass(frozen=True)
class ProductSpec:
    id: str
    name: str
    description: str
    color: str


PRODUCTS: tuple[ProductSpec, ...] = (
    ProductSpec("redis", "Redis", "实例详情、慢日志与参数（云 API 只读）", "#e85d4c"),
    ProductSpec("cdb", "CDB", "云数据库 MySQL 元数据、慢 SQL 与 DBBrain", "#3d8bfd"),
    ProductSpec("tdsqlc", "TDSQL-C", "CynosDB 集群拓扑、库表与 DBBrain（Product=cynosdb）", "#2ec27e"),
    ProductSpec("tke", "TKE", "集群、节点、Pod 日志、组件与日志开关（对应 TKEX / TKE）", "#c084fc"),
    ProductSpec("cls", "CLS", "日志检索、索引字段与直方图", "#f5a524"),
    ProductSpec("cos", "COS", "存储桶、对象列表与对象元数据（只读，不下载对象内容）", "#00a4ff"),
)

TARGET = Param(
    "target",
    "string",
    "目标环境别名（config 里的 targets 键）",
    required=True,
    enum_from="targets",
)

RFC_START = Param("startTime", "string", "开始时间，RFC3339，例如 2026-08-04T15:00:00+08:00")
RFC_END = Param("endTime", "string", "结束时间，RFC3339。省略则为当前时间")
LIMIT = Param("limit", "integer", "返回条数；超上限直接报错，不会静默截断")


def _dbbrain(prefix: str, catalog_product: str, engine: str, instance_enum: EnumFrom) -> tuple[ToolSpec, ...]:
    inst = Param(
        "instance",
        "string",
        "已绑定实例的别名（实例 ID 在 config，不由模型填写）",
        required=True,
        enum_from=instance_enum,
    )
    engine_note = (
        "DBBrain 的 Product 已写死为 "
        f"{engine}，不作为工具入参。"
    )
    return (
        ToolSpec(
            f"{prefix}dbbrain_health_score",
            catalog_product,
            f"实例健康得分与扣分项。{engine_note}",
            (TARGET, inst, RFC_START, RFC_END),
            requires_resources=(instance_enum,),
        ),
        ToolSpec(
            f"{prefix}dbbrain_diag_events",
            catalog_product,
            f"诊断事件历史（死锁、全表扫描、慢 SQL 等）。只保留最近 30 天。{engine_note}",
            (
                TARGET,
                inst,
                RFC_START,
                RFC_END,
                Param("limit", "integer", "返回条数，默认 20，上限 50"),
                Param(
                    "severities",
                    "array",
                    "风险等级：1 致命、2 严重、3 告警、4 提示、5 健康",
                    items_type="integer",
                ),
            ),
            requires_resources=(instance_enum,),
        ),
        ToolSpec(
            f"{prefix}dbbrain_diag_event_detail",
            catalog_product,
            "单个诊断事件详情：问题、严重程度与处理建议。eventId 来自 diag_events。",
            (
                TARGET,
                inst,
                Param("eventId", "integer", "事件 ID", required=True),
            ),
            requires_resources=(instance_enum,),
        ),
        ToolSpec(
            f"{prefix}dbbrain_process_list",
            catalog_product,
            "当前会话与正在执行的 SQL，用于锁等待和长事务。",
            (
                TARGET,
                inst,
                Param("db", "string", "按库名过滤"),
                Param("user", "string", "按账号过滤"),
                Param("host", "string", "按来源主机过滤"),
                Param("minSeconds", "integer", "只看执行超过该秒数的会话"),
                Param("limit", "integer", "返回条数，默认 100，上限 100"),
            ),
            requires_resources=(instance_enum,),
        ),
        ToolSpec(
            f"{prefix}dbbrain_slow_top_sqls",
            catalog_product,
            "按 SQL 模板聚合的慢查询排行。时间窗不得超过 7 天。",
            (
                TARGET,
                inst,
                Param("startTime", "string", "开始时间，RFC3339。间隔不得超过 7 天", required=True),
                Param("endTime", "string", "结束时间，RFC3339", required=True),
                Param("limit", "integer", "返回条数，默认 20，上限 100"),
                Param(
                    "sortBy",
                    "string",
                    "排序字段，默认 QueryTime",
                    enum=("QueryTime", "ExecTimes", "RowsSent", "LockTime", "RowsExamined"),
                    default="QueryTime",
                ),
            ),
            requires_resources=(instance_enum,),
        ),
        ToolSpec(
            f"{prefix}dbbrain_sql_advice",
            catalog_product,
            "对一条 SQL 给出索引建议与预估收益。不会执行 SQL。",
            (
                TARGET,
                inst,
                Param("sql", "string", "要分析的 SQL", required=True),
                Param("schema", "string", "库名"),
            ),
            requires_resources=(instance_enum,),
        ),
    )


TOOLS: tuple[ToolSpec, ...] = (
    ToolSpec(
        "ping",
        "common",
        "连通性自检。返回服务版本、已配置的产品与资源别名，不访问任何后端。",
        (TARGET,),
        requires_resources=(),
    ),
    # --- Redis ---
    ToolSpec(
        "redis_instance_detail",
        "redis",
        "已绑定 Redis 实例的规格、状态、网络与版本（DescribeInstances，按 InstanceId 过滤）。",
        (
            TARGET,
            Param("instance", "string", "实例别名", required=True, enum_from="redis"),
        ),
        requires_resources=("redis",),
    ),
    ToolSpec(
        "redis_slow_logs",
        "redis",
        "Redis 慢日志明细（DescribeSlowLog）。实例 ID 来自 config。",
        (
            TARGET,
            Param("instance", "string", "实例别名", required=True, enum_from="redis"),
            RFC_START,
            RFC_END,
            LIMIT,
        ),
        requires_resources=("redis",),
    ),
    ToolSpec(
        "redis_params",
        "redis",
        "实例当前参数列表（DescribeInstanceParams）。",
        (
            TARGET,
            Param("instance", "string", "实例别名", required=True, enum_from="redis"),
        ),
        requires_resources=("redis",),
    ),
    # --- CDB ---
    ToolSpec(
        "cdb_instance_detail",
        "cdb",
        "CDB 实例规格、状态、内网地址与版本。实例 ID 来自 config。",
        (
            TARGET,
            Param("instance", "string", "实例别名", required=True, enum_from="cdb"),
        ),
        requires_resources=("cdb",),
    ),
    ToolSpec(
        "cdb_list_databases",
        "cdb",
        "列出 CDB 实例上的数据库名（DescribeDatabases）。",
        (
            TARGET,
            Param("instance", "string", "实例别名", required=True, enum_from="cdb"),
        ),
        requires_resources=("cdb",),
    ),
    ToolSpec(
        "cdb_slow_queries",
        "cdb",
        "CDB 慢查询明细（DescribeSlowLogs）。",
        (
            TARGET,
            Param("instance", "string", "实例别名", required=True, enum_from="cdb"),
            RFC_START,
            RFC_END,
            LIMIT,
        ),
        requires_resources=("cdb",),
    ),
    ToolSpec(
        "cdb_error_logs",
        "cdb",
        "CDB 错误日志（DescribeErrorLog）。",
        (
            TARGET,
            Param("instance", "string", "实例别名", required=True, enum_from="cdb"),
            RFC_START,
            RFC_END,
            LIMIT,
        ),
        requires_resources=("cdb",),
    ),
    *_dbbrain("cdb_", "cdb", "mysql", "cdb"),
    # --- TDSQL-C ---
    ToolSpec(
        "db_cluster_detail",
        "tdsqlc",
        "TDSQL-C 集群拓扑与规格：版本、状态、存储、内网地址及实例角色。集群 ID 来自 config。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tdsqlc"),
        ),
        requires_resources=("tdsqlc",),
    ),
    ToolSpec(
        "db_list_databases",
        "tdsqlc",
        "列出 TDSQL-C 集群下的数据库名（DescribeClusterDatabases）。走云 API。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tdsqlc"),
        ),
        requires_resources=("tdsqlc",),
    ),
    ToolSpec(
        "db_search_tables",
        "tdsqlc",
        "按库名和表名模糊搜索表（SearchClusterTables）。不含列定义。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tdsqlc"),
            Param("database", "string", "库名，省略则搜索全部库"),
            Param("table", "string", "表名关键字"),
            Param(
                "tableType",
                "string",
                "表类型，默认 all",
                enum=("all", "base_table", "view"),
                default="all",
            ),
        ),
        requires_resources=("tdsqlc",),
    ),
    ToolSpec(
        "tdsqlc_slow_queries",
        "tdsqlc",
        "慢 SQL 原始明细（DescribeInstanceSlowQueries）。实例 ID 来自 config。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tdsqlc"),
            RFC_START,
            RFC_END,
            LIMIT,
            Param("sqlText", "string", "按 SQL 文本过滤"),
        ),
        requires_resources=("tdsqlc",),
    ),
    ToolSpec(
        "tdsqlc_error_logs",
        "tdsqlc",
        "实例错误日志（DescribeInstanceErrorLogs）。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tdsqlc"),
            RFC_START,
            RFC_END,
            LIMIT,
        ),
        requires_resources=("tdsqlc",),
    ),
    ToolSpec(
        "dbbrain_top_space_tables",
        "tdsqlc",
        "占用空间最大的表。DBBrain Product 已写死为 cynosdb。",
        (
            TARGET,
            Param("instance", "string", "集群/实例别名", required=True, enum_from="tdsqlc"),
            Param("limit", "integer", "返回条数，默认 20，上限 100"),
            Param(
                "sortBy",
                "string",
                "排序字段",
                enum=("TotalLength", "DataLength", "IndexLength", "DataFree", "FragRatio", "TableRows"),
                default="TotalLength",
            ),
        ),
        requires_resources=("tdsqlc",),
    ),
    ToolSpec(
        "dbbrain_slow_log_time_series",
        "tdsqlc",
        "慢日志数量随时间的分布（间隔不得超过 7 天）。Product=cynosdb。",
        (
            TARGET,
            Param("instance", "string", "集群/实例别名", required=True, enum_from="tdsqlc"),
            Param("startTime", "string", "开始时间，RFC3339", required=True),
            Param("endTime", "string", "结束时间，RFC3339", required=True),
        ),
        requires_resources=("tdsqlc",),
    ),
    *_dbbrain("", "tdsqlc", "cynosdb", "tdsqlc"),
    # --- TKE ---
    ToolSpec(
        "tke_cluster_detail",
        "tke",
        "已绑定 TKE 集群的状态、版本、网络与节点概况。集群 ID 来自 config。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_cluster_status",
        "tke",
        "集群运行状态与节点健康汇总（DescribeClusterStatus）。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_list_nodes",
        "tke",
        "列出集群节点：IP、状态、节点池、是否封锁（DescribeClusterInstances）。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
            Param("nodePoolId", "string", "按节点池 ID 过滤，可空"),
            Param("offset", "integer", "分页偏移，默认 0"),
            LIMIT,
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_list_node_pools",
        "tke",
        "列出集群节点池，含原生 / 普通 / 超级 / 第三方（DescribeNodePools + DescribeClusterNodePools）。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_node_pool_detail",
        "tke",
        "单个节点池详情。普通池走 DescribeClusterNodePoolDetail，原生池走 DescribeNodePools。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
            Param("nodePoolId", "string", "节点池 ID，例如 np-xxxx", required=True),
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_list_addons",
        "tke",
        "列出集群已安装组件及版本、状态（DescribeAddon）。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
            Param("addon", "string", "组件名；不填则返回全部"),
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_endpoint_status",
        "tke",
        "集群 apiserver 内网/外网访问端口是否已开通（DescribeClusterEndpointStatus）。不返回 kubeconfig。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_log_switches",
        "tke",
        "集群审计 / 事件 / 日志采集开关（DescribeLogSwitches）。和 CLS 的 k8s_audit / k8s_events 配套。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_list_pods",
        "tke",
        "列出集群 Pod（ForwardApplicationRequestV3 GET /api/v1/pods）。用来找 pod 名再查日志。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
            Param("namespace", "string", "命名空间；不填则全集群"),
            Param("labelSelector", "string", "标签选择器，例如 app=api"),
            LIMIT,
        ),
        requires_resources=("tke",),
    ),
    ToolSpec(
        "tke_pod_logs",
        "tke",
        "读取某个 Pod 当前容器的最近日志（k8s pods/log，实时，不是 CLS）。",
        (
            TARGET,
            Param("cluster", "string", "集群别名", required=True, enum_from="tke"),
            Param("pod", "string", "Pod 名", required=True),
            Param("namespace", "string", "命名空间，默认 default"),
            Param("container", "string", "多容器时指定容器名"),
            Param("tailLines", "integer", "最近多少行，默认 200，上限 500"),
            Param("previous", "string", "true 则读上一次崩溃容器的日志", enum=("true", "false"), default="false"),
        ),
        requires_resources=("tke",),
    ),
    # --- CLS ---
    ToolSpec(
        "cls_search_log",
        "cls",
        "检索 CLS 日志（SearchLog），CQL 语法。topic 用别名，不是 UUID。",
        (
            TARGET,
            Param("query", "string", "CQL 检索语句；查全部用 *", required=True),
            Param("topic", "string", "日志主题别名", required=True, enum_from="topics"),
            RFC_START,
            RFC_END,
            Param("limit", "integer", "返回条数，默认 100，上限 1000"),
            Param("sort", "string", "按时间排序", enum=("asc", "desc"), default="desc"),
        ),
        requires_resources=("cls",),
    ),
    ToolSpec(
        "cls_list_topics",
        "cls",
        "列出该环境已绑定的日志主题别名与配置中的 UUID 对应关系。",
        (TARGET,),
        requires_resources=("cls",),
    ),
    ToolSpec(
        "cls_index_fields",
        "cls",
        "列出某个日志主题已建索引的字段及类型。写检索语句前先看这个。",
        (
            TARGET,
            Param("topic", "string", "日志主题别名", required=True, enum_from="topics"),
        ),
        requires_resources=("cls",),
    ),
    ToolSpec(
        "cls_histogram",
        "cls",
        "按时间分桶统计命中条数（DescribeLogHistogram）。先看分布再下钻。",
        (
            TARGET,
            Param("query", "string", "CQL 检索语句；查全部用 *", required=True),
            Param("topic", "string", "日志主题别名", required=True, enum_from="topics"),
            RFC_START,
            RFC_END,
            Param("interval", "string", "分桶间隔，例如 30s / 5m / 1h", default="5m"),
        ),
        requires_resources=("cls",),
    ),
    ToolSpec(
        "container_logs",
        "cls",
        "按容器维度检索已采集进 CLS 的历史 stdout。要看当前 Pod 实时日志用 tke_pod_logs。需要同时启用 TKE。",
        (
            TARGET,
            Param("workload", "string", "workload / 容器名"),
            Param("pod", "string", "精确到某个 pod 名"),
            Param("query", "string", "附加 CQL 条件，例如 level:error"),
            RFC_START,
            RFC_END,
            LIMIT,
        ),
        requires_resources=("cls",),
        requires_products=("tke",),
    ),
    ToolSpec(
        "k8s_events",
        "cls",
        "检索已绑定的 k8s 事件主题（别名 events，若未绑定则不可用）。",
        (
            TARGET,
            Param("query", "string", "附加 CQL 条件"),
            RFC_START,
            RFC_END,
            LIMIT,
        ),
        requires_resources=("cls",),
        requires_products=("tke",),
    ),
    ToolSpec(
        "k8s_audit",
        "cls",
        "检索 kube-apiserver 审计日志（别名 audit，若未绑定则不可用）。",
        (
            TARGET,
            Param("query", "string", "例如 verb:update AND objectRef.resource:deployments"),
            RFC_START,
            RFC_END,
            LIMIT,
        ),
        requires_resources=("cls",),
        requires_products=("tke",),
    ),
    # --- COS ---
    ToolSpec(
        "cos_bucket_detail",
        "cos",
        "检查已绑定 COS 存储桶是否存在且当前身份是否有访问权限（HeadBucket）。",
        (
            TARGET,
            Param("bucket", "string", "存储桶别名", required=True, enum_from="cos"),
        ),
        requires_resources=("cos",),
    ),
    ToolSpec(
        "cos_list_objects",
        "cos",
        "列出 COS 存储桶中的对象和公共前缀（GetBucket/ListObjects），不读取对象内容。",
        (
            TARGET,
            Param("bucket", "string", "存储桶别名", required=True, enum_from="cos"),
            Param("prefix", "string", "只返回以该字符串开头的对象键"),
            Param("delimiter", "string", "目录分隔符，通常为 /"),
            Param("marker", "string", "分页起点；使用上次返回的 nextMarker"),
            Param("limit", "integer", "返回对象和公共前缀的总上限，默认 100，上限 1000"),
        ),
        requires_resources=("cos",),
    ),
    ToolSpec(
        "cos_object_metadata",
        "cos",
        "查询 COS 对象的响应头与自定义元数据（HeadObject），不下载对象内容。",
        (
            TARGET,
            Param("bucket", "string", "存储桶别名", required=True, enum_from="cos"),
            Param("key", "string", "对象键，例如 logs/2026-09-14.json", required=True),
            Param("versionId", "string", "可选的对象版本 ID"),
        ),
        requires_resources=("cos",),
    ),
)


PRODUCT_BY_ID = {p.id: p for p in PRODUCTS}
TOOL_BY_NAME = {t.name: t for t in TOOLS}

TOOL_WARN_THRESHOLD = 15


def tools_for_products(products: list[str]) -> list[ToolSpec]:
    enabled = set(products)
    out: list[ToolSpec] = []
    for tool in TOOLS:
        if tool.product == "common":
            out.append(tool)
            continue
        if tool.product not in enabled:
            continue
        if tool.requires_products and not set(tool.requires_products) <= enabled:
            continue
        out.append(tool)
    return out


def metadata() -> dict[str, Any]:
    by_product: dict[str, list[dict[str, Any]]] = {p.id: [] for p in PRODUCTS}
    by_product["common"] = []
    for tool in TOOLS:
        by_product.setdefault(tool.product, []).append(_tool_public(tool))
    return {
        "name": "Tencent Cloud MCP Initializr",
        "version": "0.1.0",
        "toolWarnThreshold": TOOL_WARN_THRESHOLD,
        "defaults": {
            "name": "demo",
            "target": "prod",
            "region": "ap-guangzhou",
        },
        "products": [
            {
                "id": p.id,
                "name": p.name,
                "description": p.description,
                "color": p.color,
                "tools": by_product.get(p.id, []),
            }
            for p in PRODUCTS
        ],
        "commonTools": by_product["common"],
        "regions": public_regions(),
    }


def _tool_public(tool: ToolSpec) -> dict[str, Any]:
    return {
        "name": tool.name,
        "description": tool.description,
        "product": tool.product,
        "requiresResources": list(tool.requires_resources),
        "requiresProducts": list(tool.requires_products),
        "readOnly": tool.read_only,
        "params": [
            {
                "name": p.name,
                "type": p.type,
                "description": p.description,
                "required": p.required,
                "enum": list(p.enum) if p.enum else None,
                "enumFrom": p.enum_from,
                "default": p.default,
            }
            for p in tool.params
        ],
    }
