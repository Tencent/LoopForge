from __future__ import annotations

import hashlib
import hmac
import json
import os
import struct
import subprocess
import time
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .agentlens import bootstrap



@dataclass(frozen=True)
class CLSConfig:
    enabled: bool
    endpoint: str
    topic_id: str
    secret_id: str
    secret_key: str
    secret_token: str
    service_name: str
    timeout_seconds: int
    helper_path: Path
    sdk_entry_path: Path

    @property
    def ready(self) -> bool:
        return bool(
            self.enabled
            and self.endpoint
            and self.topic_id
            and self.secret_id
            and self.secret_key
            and self.helper_path.is_file()
            and self.sdk_entry_path.is_file()
        )


def repo_root() -> Path:
    return Path(__file__).resolve().parents[5]


def helper_path() -> Path:
    return Path(__file__).with_name("cls_uploader.mjs")


def sdk_entry_path() -> Path:
    return (
        repo_root()
        / "cls-codebuddy"
        / "tencentcloud-cls-sdk-codebuddy"
        / "node_modules"
        / "tencentcloud-cls-sdk-js"
        / "dist"
        / "index.js"
    )


def debug_log_path() -> Path:
    return Path(__file__).resolve().parents[2] / "logs" / "cls-push-debug.ndjson"


def _value(
    name: str,
    *,
    env_local: dict[str, str],
    env_file: dict[str, str],
    default: str = "",
    aliases: tuple[str, ...] = (),
) -> str:
    keys = (name, *aliases)
    for key in keys:
        raw = os.environ.get(key)
        if raw is not None and str(raw).strip():
            return str(raw).strip()
    for key in keys:
        raw = env_local.get(key)
        if raw is not None and str(raw).strip():
            return str(raw).strip()
    for key in keys:
        raw = env_file.get(key)
        if raw is not None and str(raw).strip():
            return str(raw).strip()
    return default


def _enabled_flag(*, env_local: dict[str, str], env_file: dict[str, str]) -> bool:
    raw = _value(
        "CLS_CODINGAGENT_ENABLED",
        env_local=env_local,
        env_file=env_file,
        default="1",
    )
    return str(raw).strip().lower() not in {"0", "false", "off", "no"}


def _debug_write(payload: dict[str, Any]) -> None:
    try:
        path = debug_log_path()
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("a", encoding="utf-8") as fp:
            fp.write(json.dumps(payload, ensure_ascii=False) + "\n")
    except Exception:
        return


# ---- CLS PutLogs 的 protobuf 编码 ----
# CLS 使用了简化版 protobuf：
# LogGroup { repeated Log logs = 1; optional string filename = 2; }
# Log { optional uint32 time = 1; repeated Content contents = 2; }
# Content { optional string key = 1; optional string value = 2; }

def _encode_varint(value: int) -> bytes:
    result = b""
    while value > 0x7F:
        result += bytes([(value & 0x7F) | 0x80])
        value >>= 7
    result += bytes([value & 0x7F])
    return result

def _encode_field(field_number: int, wire_type: int, data: bytes) -> bytes:
    tag = (field_number << 3) | wire_type
    return _encode_varint(tag) + data

def _encode_string_field(field_number: int, value: str) -> bytes:
    encoded = value.encode("utf-8")
    return _encode_field(field_number, 2, _encode_varint(len(encoded)) + encoded)

def _encode_uint32_field(field_number: int, value: int) -> bytes:
    return _encode_field(field_number, 0, _encode_varint(value))

def _encode_content(key: str, value: str) -> bytes:
    msg = b""
    if key:
        msg += _encode_string_field(1, key)
    if value:
        msg += _encode_string_field(2, value)
    return msg

def _encode_log(record: dict[str, Any], service_name: str) -> bytes:
    ts = record.get("ts")
    if isinstance(ts, (int, float)):
        log_time = int(ts) if ts < 1_000_000_000_000 else int(ts / 1000)
    else:
        log_time = int(time.time())

    msg = _encode_uint32_field(1, log_time)

    merged = {"service_name": service_name, **record}
    for key, value in merged.items():
        if value is None:
            continue
        if isinstance(value, (dict, list)):
            value = json.dumps(value, ensure_ascii=False)
        else:
            value = str(value)
        msg += _encode_field(2, 2, _encode_varint(len(_encode_content(key, value))) + _encode_content(key, value))

    return msg

def _encode_log_group(records: list[dict[str, Any]], service_name: str) -> bytes:
    msg = b""
    for record in records:
        log_bytes = _encode_log(record, service_name)
        msg += _encode_field(1, 2, _encode_varint(len(log_bytes)) + log_bytes)
    if service_name:
        msg += _encode_string_field(2, service_name)
    return msg

def _encode_log_group_list(records: list[dict[str, Any]], service_name: str) -> bytes:
    """编码 LogGroupList protobuf：`message LogGroupList { repeated LogGroup logGroupList = 1; }`。"""
    log_group_bytes = _encode_log_group(records, service_name)
    return _encode_field(1, 2, _encode_varint(len(log_group_bytes)) + log_group_bytes)


# ---- CLS API v3 使用的 TC3-HMAC-SHA256 签名 ----

def _hmac_sha256(key: bytes, data: str) -> bytes:
    return hmac.new(key, data.encode("utf-8"), hashlib.sha256).digest()

def _sha256_hex(data: str) -> str:
    return hashlib.sha256(data.encode("utf-8")).hexdigest()

def _put_logs_via_api(
    endpoint: str,
    topic_id: str,
    secret_id: str,
    secret_key: str,
    log_group_bytes: bytes,
    region: str = "ap-guangzhou",
    timeout: int = 20,
) -> tuple[bool, str]:
    """通过 CLS API v3 发送 UploadLog，并使用 TC3-HMAC-SHA256 签名。

    `log_group_bytes` 应该是一个 LogGroupList protobuf。
    """
    host = endpoint
    service = "cls"
    action = "UploadLog"
    version = "2020-10-16"
    algorithm = "TC3-HMAC-SHA256"
    content_type = "application/octet-stream"

    timestamp = int(time.time())
    date_str = time.strftime("%Y-%m-%d", time.gmtime(timestamp))

    # 请求体就是原始 protobuf 字节串（LogGroupList）
    payload = log_group_bytes
    hashed_payload = hashlib.sha256(payload).hexdigest()

    # 规范化请求串
    canonical_headers = f"content-type:{content_type}\nhost:{host}\nx-tc-action:{action.lower()}\n"
    signed_headers = "content-type;host;x-tc-action"
    canonical_request = f"POST\n/\n\n{canonical_headers}\n{signed_headers}\n{hashed_payload}"

    # 待签名字符串
    credential_scope = f"{date_str}/{service}/tc3_request"
    hashed_canonical_request = hashlib.sha256(canonical_request.encode("utf-8")).hexdigest()
    string_to_sign = f"{algorithm}\n{timestamp}\n{credential_scope}\n{hashed_canonical_request}"

    # 签名结果
    secret_date = _hmac_sha256(("TC3" + secret_key).encode("utf-8"), date_str)
    secret_service = _hmac_sha256(secret_date, service)
    secret_signing = _hmac_sha256(secret_service, "tc3_request")
    signature = hmac.new(secret_signing, string_to_sign.encode("utf-8"), hashlib.sha256).hexdigest()

    authorization = f"{algorithm} Credential={secret_id}/{credential_scope}, SignedHeaders={signed_headers}, Signature={signature}"

    # 构造 HTTP 请求
    url = f"https://{host}"
    headers = {
        "Authorization": authorization,
        "Content-Type": content_type,
        "Host": host,
        "X-TC-Action": action,
        "X-TC-Timestamp": str(timestamp),
        "X-TC-Version": version,
        "X-TC-Region": region,
        "X-CLS-TopicId": topic_id,
        "Content-Length": str(len(payload)),
    }

    req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8", errors="replace")
            if resp.status == 200:
                return True, body
            else:
                return False, f"HTTP {resp.status}: {body}"
    except urllib.error.HTTPError as e:
        body = e.read().decode("utf-8", errors="replace")[:500]
        return False, f"HTTP {e.code}: {body}"
    except Exception as e:
        return False, f"{type(e).__name__}: {e}"


def _int_value(
    name: str,
    *,
    env_local: dict[str, str],
    env_file: dict[str, str],
    default: int,
    aliases: tuple[str, ...] = (),
) -> int:
    raw = _value(name, env_local=env_local, env_file=env_file, default=str(default), aliases=aliases)
    try:
        parsed = int(raw)
    except Exception:
        return default
    return parsed if parsed > 0 else default


def load_config(cwd: str | None = None) -> CLSConfig:
    project_root = bootstrap.project_root(cwd)
    env_file = bootstrap.read_env_file(project_root / ".env")
    env_local = bootstrap.read_env_file(project_root / ".env.local")
    return CLSConfig(
        enabled=_enabled_flag(env_local=env_local, env_file=env_file),
        endpoint=_value(
            "CLS_ENDPOINT",
            env_local=env_local,
            env_file=env_file,
        ),
        topic_id=_value(
            "CLS_TOPIC_ID",
            env_local=env_local,
            env_file=env_file,
        ),
        secret_id=_value(
            "CLS_SECRET_ID",
            env_local=env_local,
            env_file=env_file,
            aliases=(
                "TC_SECRET_ID",
                "TENCENTCLOUD_SECRET_ID",
                "TENCENTCLOUD_SECRET_ID_438167613",
            ),
        ),
        secret_key=_value(
            "CLS_SECRET_KEY",
            env_local=env_local,
            env_file=env_file,
            aliases=(
                "TC_SECRET_KEY",
                "TENCENTCLOUD_SECRET_KEY",
                "TENCENTCLOUD_SECRET_KEY_438167613",
            ),
        ),
        secret_token=_value(
            "CLS_SECRET_TOKEN",
            env_local=env_local,
            env_file=env_file,
            aliases=("TC_SECRET_TOKEN", "TC_SESSION_TOKEN", "CLS_SESSION_TOKEN"),
        ),
        service_name=_value(
            "CLS_SERVICE_NAME",
            env_local=env_local,
            env_file=env_file,
        ),
        timeout_seconds=_int_value(
            "CLS_UPLOAD_TIMEOUT_SECONDS",
            env_local=env_local,
            env_file=env_file,
            default=0,
            aliases=("CLS_TIMEOUT_SECONDS",),
        ),
        helper_path=helper_path(),
        sdk_entry_path=sdk_entry_path(),
    )


def mirror_record(record: dict[str, Any], *, cwd: str | None = None) -> bool:
    config = load_config(cwd)
    if not config.ready:
        _debug_write(
            {
                "stage": "config_not_ready",
                "endpoint": config.endpoint,
                "topic_id": config.topic_id,
                "secret_id_present": bool(config.secret_id),
                "secret_key_present": bool(config.secret_key),
                "secret_token_present": bool(config.secret_token),
                "timeout_seconds": config.timeout_seconds,
                "helper_exists": config.helper_path.is_file(),
                "sdk_exists": config.sdk_entry_path.is_file(),
                "event": record.get("event"),
                "sid": record.get("sid"),
            }
        )
        return False

    _debug_write(
        {
            "stage": "uploader_start",
            "endpoint": config.endpoint,
            "topic_id": config.topic_id,
            "timeout_seconds": config.timeout_seconds,
            "event": record.get("event"),
            "sid": record.get("sid"),
        }
    )

    # 先尝试 Python 原生 API v3 上报（兼容内网 endpoint）
    try:
        log_group_bytes = _encode_log_group_list([record], config.service_name)
        success, detail = _put_logs_via_api(
            endpoint=config.endpoint,
            topic_id=config.topic_id,
            secret_id=config.secret_id,
            secret_key=config.secret_key,
            log_group_bytes=log_group_bytes,
            timeout=config.timeout_seconds,
        )
        if success:
            _debug_write(
                {
                    "stage": "uploader_ok",
                    "endpoint": config.endpoint,
                    "topic_id": config.topic_id,
                    "method": "python_api_v3",
                    "event": record.get("event"),
                    "sid": record.get("sid"),
                }
            )
            return True
        else:
            _debug_write(
                {
                    "stage": "uploader_failed",
                    "method": "python_api_v3",
                    "error": detail[:2000],
                    "timeout_seconds": config.timeout_seconds,
                    "event": record.get("event"),
                    "sid": record.get("sid"),
                }
            )
    except Exception as err:
        _debug_write(
            {
                "stage": "uploader_exception",
                "method": "python_api_v3",
                "error": f"{type(err).__name__}: {err}",
                "timeout_seconds": config.timeout_seconds,
                "event": record.get("event"),
                "sid": record.get("sid"),
            }
        )

    # 回退方案：使用 node SDK uploader
    payload = {
        "endpoint": config.endpoint,
        "topicId": config.topic_id,
        "secretId": config.secret_id,
        "secretKey": config.secret_key,
        "secretToken": config.secret_token,
        "serviceName": config.service_name,
        "records": [record],
    }
    try:
        completed = subprocess.run(
            ["node", str(config.helper_path), str(config.sdk_entry_path)],
            input=json.dumps(payload, ensure_ascii=False),
            text=True,
            capture_output=True,
            check=False,
            timeout=config.timeout_seconds,
        )
        if completed.returncode != 0:
            _debug_write(
                {
                    "stage": "uploader_failed",
                    "returncode": completed.returncode,
                    "stdout": (completed.stdout or "")[:2000],
                    "stderr": (completed.stderr or "")[:2000],
                    "timeout_seconds": config.timeout_seconds,
                    "event": record.get("event"),
                    "sid": record.get("sid"),
                }
            )
            return False
    except Exception as err:
        _debug_write(
            {
                "stage": "uploader_exception",
                "error": f"{type(err).__name__}: {err}",
                "timeout_seconds": config.timeout_seconds,
                "event": record.get("event"),
                "sid": record.get("sid"),
            }
        )
        return False

    _debug_write(
        {
            "stage": "uploader_ok",
            "endpoint": config.endpoint,
            "topic_id": config.topic_id,
            "timeout_seconds": config.timeout_seconds,
            "event": record.get("event"),
            "sid": record.get("sid"),
        }
    )
    return True
