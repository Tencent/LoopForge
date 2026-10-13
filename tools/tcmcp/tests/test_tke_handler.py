import asyncio
import json
import sys
import zipfile
from io import BytesIO
from unittest.mock import Mock

import pytest
from tencentcloud.common.exception.tencent_cloud_sdk_exception import TencentCloudSDKException
from tencentcloud.tke.v20180525 import models as regular_models
from tencentcloud.tke.v20220501 import models as native_models

from app.generate import build_zip
from app.models import BoundResources, ProjectSpec, ResourceItem, Selection


@pytest.fixture
def generated_tke(tmp_path, monkeypatch):
    selection = Selection(
        project=ProjectSpec(name="tke-ops", target="prod"),
        products=["tke"],
        resources=BoundResources(tke=[ResourceItem(id="cls-example", alias="demo")]),
    )
    with zipfile.ZipFile(BytesIO(build_zip(selection))) as archive:
        archive.extractall(tmp_path)

    monkeypatch.syspath_prepend(str(tmp_path / "tke-ops" / "src"))
    for key in list(sys.modules):
        if key == "tcmcp_server" or key.startswith("tcmcp_server."):
            del sys.modules[key]
    try:
        from tcmcp_server.handlers import tke
        from tcmcp_server.server import build_mcp

        regular = Mock(spec=["DescribeClusterNodePoolDetail"])
        native = Mock(spec=["DescribeNodePools"])
        monkeypatch.setattr(tke, "_client", lambda *args: regular)
        monkeypatch.setattr(tke, "_client2022", lambda *args: native)
        mcp = build_mcp(str(tmp_path / "tke-ops" / "config.yaml"))
        yield mcp, regular, native
    finally:
        for key in list(sys.modules):
            if key == "tcmcp_server" or key.startswith("tcmcp_server."):
                del sys.modules[key]


def pool_detail(mcp):
    content = asyncio.run(mcp.call_tool(
        "tke_node_pool_detail", {"cluster": "demo", "nodePoolId": "np-example"},
    ))
    return json.loads(content[0].text)


@pytest.mark.parametrize("regular_fails", [False, True])
def test_generated_pool_detail_preserves_fallback_error(generated_tke, regular_fails):
    mcp, regular, native = generated_tke
    regular.DescribeClusterNodePoolDetail.return_value = (
        regular_models.DescribeClusterNodePoolDetailResponse()
    )
    if regular_fails:
        regular.DescribeClusterNodePoolDetail.side_effect = TencentCloudSDKException(
            "ResourceNotFound", "not a regular pool",
        )
    failure = TencentCloudSDKException(
        "UnauthorizedOperation", "node-pool access denied", "request-example",
    )
    native.DescribeNodePools.side_effect = failure

    result = pool_detail(mcp)

    assert result == {
        "error": f"读取节点池失败: {failure}", "hint": "确认 CAM 有 tke:DescribeNodePools",
    }
    request = native.DescribeNodePools.call_args.args[0]
    assert request.ClusterId == "cls-example"
    assert request.Filters[0].Values == ["np-example"]


@pytest.mark.parametrize("pool_type", ["Regular", "Native"])
def test_generated_pool_detail_success(generated_tke, pool_type):
    mcp, regular, native = generated_tke
    regular_response = regular_models.DescribeClusterNodePoolDetailResponse()
    native_response = native_models.DescribeNodePoolsResponse()
    pool = {"NodePoolId": "np-example", "Name": "example", "Type": pool_type}
    if pool_type == "Regular":
        regular_response.from_json_string(json.dumps({"NodePool": pool}))
    else:
        regular.DescribeClusterNodePoolDetail.side_effect = TencentCloudSDKException(
            "ResourceNotFound", "not a regular pool",
        )
        native_response.from_json_string(json.dumps({"NodePools": [pool]}))
    regular.DescribeClusterNodePoolDetail.return_value = regular_response
    native.DescribeNodePools.return_value = native_response

    result = pool_detail(mcp)

    assert result["nodePoolId"] == "np-example"
    assert result["type"] == pool_type
    assert native.DescribeNodePools.call_count == (pool_type == "Native")
