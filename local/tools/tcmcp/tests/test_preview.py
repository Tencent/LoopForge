from app.models import BoundResources, CLSResource, CLSTopic, ProjectSpec, Selection
from app.preview import materialize


def _cls_selection(topics: list[tuple[str, str]], products=None, disabled=None) -> Selection:
    return Selection(
        project=ProjectSpec(name="demo", target="prod"),
        products=products or ["cls"],
        disabled_tools=disabled or [],
        resources=BoundResources(
            cls=[
                CLSResource(
                    logsetId="logset-1",
                    logsetName="app",
                    topics=[CLSTopic(id=tid, alias=alias, name=alias) for alias, tid in topics],
                )
            ]
        ),
    )


def test_cls_only_registers_log_tools_with_topic_enum():
    preview = materialize(_cls_selection([("api", "topic-api"), ("audit", "topic-audit")]))
    names = preview["registered"]
    assert "ping" in names
    assert "cls_search_log" in names
    assert "redis_instance_detail" not in names
    assert "dbbrain_health_score" not in names
    search = next(t for t in preview["tools"] if t["name"] == "cls_search_log")
    assert search["inputSchema"]["properties"]["topic"]["enum"] == ["api", "audit"]
    assert search["inputSchema"]["properties"]["target"]["enum"] == ["prod"]


def test_k8s_audit_requires_audit_alias():
    preview = materialize(_cls_selection([("api", "topic-api")], products=["cls", "tke"]))
    skipped = {t["name"]: t for t in preview["skipped"]}
    assert "k8s_audit" in skipped
    assert "audit" in skipped["k8s_audit"]["skipReason"]


def test_disable_tool_drops_from_registered():
    preview = materialize(
        _cls_selection([("api", "t1")], disabled=["cls_histogram"]),
    )
    assert "cls_histogram" not in preview["registered"]
    assert "cls_search_log" in preview["registered"]


def test_warn_when_too_many_tools():
    from app.models import ResourceItem, TDSQLCResource

    sel = Selection(
        project=ProjectSpec(name="big"),
        products=["redis", "cdb", "tdsqlc", "tke", "cls"],
        resources=BoundResources(
            redis=[ResourceItem(id="crs-1", alias="cache")],
            cdb=[ResourceItem(id="cdb-1", alias="mysql")],
            tdsqlc=[TDSQLCResource(clusterId="c1", instanceId="i1", alias="primary")],
            tke=[ResourceItem(id="cls-xx", alias="k8s")],
            cls=[
                CLSResource(
                    logsetId="ls",
                    topics=[
                        CLSTopic(id="1", alias="api"),
                        CLSTopic(id="2", alias="events"),
                        CLSTopic(id="3", alias="audit"),
                    ],
                )
            ],
        ),
    )
    preview = materialize(sel)
    assert preview["warn"] is True
    assert preview["counts"]["tools"] > 15


def test_cos_tools_use_bound_bucket_aliases():
    from app.models import ResourceItem

    sel = Selection(
        project=ProjectSpec(name="cos-ops", target="prod"),
        products=["cos"],
        resources=BoundResources(
            cos=[
                ResourceItem(
                    id="assets-1250000000",
                    alias="assets",
                    name="assets-1250000000",
                    region="ap-guangzhou",
                )
            ]
        ),
    )
    preview = materialize(sel)
    assert set(preview["registered"]) == {
        "ping",
        "cos_bucket_detail",
        "cos_list_objects",
        "cos_object_metadata",
    }
    tool = next(t for t in preview["tools"] if t["name"] == "cos_list_objects")
    assert tool["inputSchema"]["properties"]["bucket"]["enum"] == ["assets"]
    assert preview["counts"]["resources"] == 1
