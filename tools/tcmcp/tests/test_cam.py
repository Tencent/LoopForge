from app.cam import cam_policy
from app.models import BoundResources, ProjectSpec, ResourceItem, Selection


def test_tke_cam_covers_native_pools_and_forward():
    sel = Selection(
        project=ProjectSpec(name="demo", region="ap-nanjing"),
        products=["tke"],
        resources=BoundResources(
            tke=[ResourceItem(id="cls-xxxxxxxx", alias="demo-tke", name="x", region="ap-nanjing")]
        ),
    )
    actions = {a for stmt in cam_policy(sel)["statement"] for a in stmt["action"]}
    assert "tke:DescribeNodePools" in actions
    assert "tke:DescribeClusterNodePools" in actions
    assert "tke:ForwardApplicationRequestV3" in actions


def test_cos_cam_is_scoped_to_selected_bucket():
    sel = Selection(
        project=ProjectSpec(name="demo", region="ap-guangzhou"),
        products=["cos"],
        resources=BoundResources(
            cos=[
                ResourceItem(
                    id="assets-1250000000",
                    alias="assets",
                    region="ap-guangzhou",
                )
            ]
        ),
    )
    policy = cam_policy(sel)
    assert policy["statement"] == [
        {
            "_comment": "COS 桶名须包含 APPID；只授权列对象、检查桶和读取对象元数据。",
            "effect": "allow",
            "action": ["cos:HeadBucket", "cos:GetBucket", "cos:HeadObject"],
            "resource": ["qcs::cos:ap-guangzhou:uid/1250000000:assets-1250000000/*"],
        }
    ]
