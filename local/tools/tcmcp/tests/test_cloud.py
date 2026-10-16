from app.cloud.listing import list_resources
from app.models import CloudCreds, ListedLogset, ListedResource, ListedTopic, ResourceCatalog


def test_list_resources_uses_injected_listers(monkeypatch):
    import app.cloud.listing as mod

    def fake_redis(creds):
        return [ListedResource(id="crs-1", name="cache")]

    def fake_cls(creds):
        return [
            ListedLogset(
                id="ls-1",
                name="app",
                topics=[ListedTopic(id="t1", name="api", logsetId="ls-1")],
            )
        ]

    monkeypatch.setattr(mod, "LISTERS", {"redis": fake_redis, "cls": fake_cls})
    cat = list_resources(
        CloudCreds(secretId="id", secretKey="key", region="ap-guangzhou", products=["redis", "cls"])
    )
    assert isinstance(cat, ResourceCatalog)
    assert cat.redis[0].id == "crs-1"
    assert cat.cls[0].topics[0].id == "t1"
    assert not cat.errors


def test_lister_error_is_captured(monkeypatch):
    import app.cloud.listing as mod

    def boom(creds):
        raise RuntimeError("AuthFailure")

    monkeypatch.setattr(mod, "LISTERS", {"redis": boom})
    cat = list_resources(CloudCreds(secretId="id", secretKey="key", products=["redis"]))
    assert "AuthFailure" in cat.errors["redis"]


def test_list_cos_filters_requested_region(monkeypatch):
    import app.cloud.listing as mod

    class FakeClient:
        def list_buckets(self, **kwargs):
            assert kwargs == {"Region": "ap-guangzhou"}
            return {
                "Buckets": {
                    "Bucket": [
                        {
                            "Name": "assets-1250000000",
                            "Location": "ap-guangzhou",
                            "CreationDate": "2026-09-14T00:00:00Z",
                        },
                        {"Name": "backup-1250000000", "Location": "ap-shanghai"},
                    ]
                }
            }

    monkeypatch.setattr(mod, "_cos_client", lambda *args: FakeClient())
    rows = mod.list_cos(
        CloudCreds(secretId="id", secretKey="key", region="ap-guangzhou", products=["cos"])
    )
    assert [row.id for row in rows] == ["assets-1250000000"]
    assert rows[0].extra["creationDate"] == "2026-09-14T00:00:00Z"
