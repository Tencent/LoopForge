"""Thin Tencent Cloud client factory for generated tools."""

from __future__ import annotations

import warnings
from functools import lru_cache

# tencentcloud-sdk-python typos "fileds" when the API returns extra JSON keys.
warnings.filterwarnings("ignore", message=r".*fileds are useless\.")


@lru_cache(maxsize=32)
def cloud_client(product: str, secret_id: str, secret_key: str, region: str):
    from tencentcloud.common import credential
    from tencentcloud.common.profile.client_profile import ClientProfile
    from tencentcloud.common.profile.http_profile import HttpProfile

    cred = credential.Credential(secret_id, secret_key)
    endpoint = "tke.tencentcloudapi.com" if product.startswith("tke") else f"{product}.tencentcloudapi.com"
    http = HttpProfile(endpoint=endpoint)
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
    if product == "tke2022":
        from tencentcloud.tke.v20220501.tke_client import TkeClient as TkeClient2022

        return TkeClient2022(cred, region, profile)
    if product == "cls":
        from tencentcloud.cls.v20201016.cls_client import ClsClient

        return ClsClient(cred, region, profile)
    if product == "dbbrain":
        from tencentcloud.dbbrain.v20210527.dbbrain_client import DbbrainClient

        return DbbrainClient(cred, region, profile)
    raise ValueError(f"unknown product {product}")


@lru_cache(maxsize=32)
def cos_client(secret_id: str, secret_key: str, region: str):
    from qcloud_cos import CosConfig, CosS3Client

    config = CosConfig(Region=region, SecretId=secret_id, SecretKey=secret_key, Scheme="https")
    return CosS3Client(config)


def ok(payload) -> dict:
    return payload if isinstance(payload, dict) else {"result": payload}


def fail(message: str, hint: str = "") -> dict:
    out = {"error": message}
    if hint:
        out["hint"] = hint
    return out
