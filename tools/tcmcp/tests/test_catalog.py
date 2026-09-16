from app.catalog import PRODUCTS, TOOLS, metadata, tools_for_products


def test_metadata_lists_six_products():
    meta = metadata()
    ids = [p["id"] for p in meta["products"]]
    assert ids == ["redis", "cdb", "tdsqlc", "tke", "cls", "cos"]
    assert meta["toolWarnThreshold"] == 15
    ids_region = [r["id"] for r in meta["regions"]]
    assert "ap-guangzhou" in ids_region
    assert "ap-shanghai" in ids_region


def test_cls_wrappers_need_tke():
    names = {t.name for t in tools_for_products(["cls"])}
    assert "cls_search_log" in names
    assert "container_logs" not in names
    both = {t.name for t in tools_for_products(["cls", "tke"])}
    assert "container_logs" in both
    assert "k8s_events" in both


def test_dbbrain_product_split():
    cdb = {t.name for t in tools_for_products(["cdb"])}
    cyn = {t.name for t in tools_for_products(["tdsqlc"])}
    assert "cdb_dbbrain_health_score" in cdb
    assert "dbbrain_health_score" in cyn
    assert "dbbrain_health_score" not in cdb


def test_every_tool_has_product():
    ids = {p.id for p in PRODUCTS} | {"common"}
    assert all(t.product in ids for t in TOOLS)


def test_tke_has_cluster_and_node_tools():
    names = {t.name for t in tools_for_products(["tke"])}
    assert names >= {
        "ping",
        "tke_cluster_detail",
        "tke_cluster_status",
        "tke_list_nodes",
        "tke_list_node_pools",
        "tke_node_pool_detail",
        "tke_list_addons",
        "tke_endpoint_status",
        "tke_log_switches",
        "tke_list_pods",
        "tke_pod_logs",
    }


def test_cos_has_read_only_bucket_and_object_tools():
    names = {t.name for t in tools_for_products(["cos"])}
    assert names == {
        "ping",
        "cos_bucket_detail",
        "cos_list_objects",
        "cos_object_metadata",
    }
    assert all(t.read_only for t in tools_for_products(["cos"]))
