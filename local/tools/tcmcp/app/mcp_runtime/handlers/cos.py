from __future__ import annotations

from tcmcp_server.config import resolve_alias, secret_pair
from tcmcp_server.handlers.dispatch import register as _register
from tcmcp_server.sdk import cos_client, fail, ok


def _bucket(selection, alias: str):
    return resolve_alias(selection.resources.cos, alias)


def _client(data: dict, bucket):
    sid, skey, default_region = secret_pair(data)
    return cos_client(sid, skey, bucket.region or default_region)


def _list(value) -> list:
    if not value:
        return []
    return value if isinstance(value, list) else [value]


def handle_bucket_detail(args, data, selection):
    bucket = _bucket(selection, args.get("bucket", ""))
    response = _client(data, bucket).head_bucket(Bucket=bucket.id)
    return ok(
        {
            "alias": bucket.alias,
            "name": bucket.id,
            "region": bucket.region,
            "headers": dict(response or {}),
        }
    )


def handle_list_objects(args, data, selection):
    bucket = _bucket(selection, args.get("bucket", ""))
    limit = int(args.get("limit") or 100)
    if limit < 1 or limit > 1000:
        return fail("limit 必须在 1 到 1000 之间", "调整 limit 后重试")

    request = {"Bucket": bucket.id, "MaxKeys": limit}
    for arg, sdk_name in (("prefix", "Prefix"), ("delimiter", "Delimiter"), ("marker", "Marker")):
        if args.get(arg):
            request[sdk_name] = args[arg]
    response = _client(data, bucket).list_objects(**request)

    items = []
    for item in _list(response.get("Contents")):
        items.append(
            {
                "key": item.get("Key"),
                "size": item.get("Size"),
                "etag": item.get("ETag"),
                "lastModified": item.get("LastModified"),
                "storageClass": item.get("StorageClass"),
            }
        )
    prefixes = [item.get("Prefix") for item in _list(response.get("CommonPrefixes")) if item.get("Prefix")]
    return ok(
        {
            "bucket": bucket.alias,
            "prefix": response.get("Prefix", args.get("prefix") or ""),
            "items": items,
            "commonPrefixes": prefixes,
            "count": len(items),
            "isTruncated": str(response.get("IsTruncated", "false")).lower() == "true",
            "nextMarker": response.get("NextMarker") or "",
        }
    )


def handle_object_metadata(args, data, selection):
    bucket = _bucket(selection, args.get("bucket", ""))
    key = args.get("key") or ""
    request = {"Bucket": bucket.id, "Key": key}
    if args.get("versionId"):
        request["versionId"] = args["versionId"]
    response = _client(data, bucket).head_object(**request)
    return ok(
        {
            "bucket": bucket.alias,
            "key": key,
            "metadata": dict(response or {}),
        }
    )


def register() -> None:
    _register("cos_bucket_detail", handle_bucket_detail)
    _register("cos_list_objects", handle_list_objects)
    _register("cos_object_metadata", handle_object_metadata)
