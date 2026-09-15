from __future__ import annotations
"""AgentLens 启动辅助层。

这一层负责“读入与初始化”：
- 解析项目配置与环境变量
- 延迟加载可选的 zhiyan 运行时
- 缓存初始化结果，避免重复 init
- 处理调试落盘与失败状态写回
"""

import getpass
import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .. import state as st

DEFAULT_APP_NAME = "skillhub.codebuddy-hooks"
DEFAULT_BUSINESS_SCENARIO = "codebuddy-hook"

_RUNTIME: dict[str, Any] | None = None
_INIT_SIGNATURE: tuple[str, str, str] | None = None


@dataclass(frozen=True)
class AgentLensConfig:
    """从环境变量与 env 文件归一化得到的 AgentLens 配置。"""
    endpoint: str | None
    api_key: str | None
    app_name: str
    business_scenario: str
    user: str

    @property
    def enabled(self) -> bool:
        return bool(self.endpoint and self.api_key)


def project_root(cwd: str | None = None) -> Path:
    """解析用于查找 `.env` 的项目根目录。"""
    if cwd:
        return Path(cwd).expanduser().resolve()
    env_cwd = str(os.environ.get("CODEBUDDY_PROJECT_DIR") or "").strip()
    if env_cwd:
        return Path(env_cwd).expanduser().resolve()
    return Path.cwd().resolve()


def read_env_file(path: Path) -> dict[str, str]:
    """以轻量方式解析 shell 风格的 env 文件，不做 source。"""
    values: dict[str, str] = {}
    if not path.is_file():
        return values
    try:
        lines = path.read_text("utf-8").splitlines()
    except Exception:
        return values

    for raw_line in lines:
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].strip()
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if not key:
            continue
        if value and value[0] == value[-1] and value[0] in {"'", '"'}:
            value = value[1:-1]
        values[key] = value
    return values


def load_config(project_root_path: Path) -> AgentLensConfig:
    """按优先级加载配置：进程环境变量 > `.env.local` > `.env`。"""
    env_file = read_env_file(project_root_path / ".env")
    env_local_file = read_env_file(project_root_path / ".env.local")

    def _value(name: str, default: str | None = None) -> str | None:
        env_value = os.environ.get(name)
        if env_value is not None and str(env_value).strip():
            return str(env_value).strip()
        if name in env_local_file and str(env_local_file[name]).strip():
            return str(env_local_file[name]).strip()
        if name in env_file and str(env_file[name]).strip():
            return str(env_file[name]).strip()
        return default

    user = _value("ZHIYANLLM_USER")
    if not user:
        user = (
            str(os.environ.get("USER") or "").strip()
            or str(os.environ.get("USERNAME") or "").strip()
            or getpass.getuser()
        )

    return AgentLensConfig(
        endpoint=_value("ZHIYANLLM_API_ENDPOINT"),
        api_key=_value("ZHIYANLLM_API_KEY"),
        app_name=_value("ZHIYANLLM_APP_NAME", DEFAULT_APP_NAME) or DEFAULT_APP_NAME,
        business_scenario=_value("ZHIYANLLM_BUSINESS_SCENARIO", DEFAULT_BUSINESS_SCENARIO)
        or DEFAULT_BUSINESS_SCENARIO,
        user=user or "unknown",
    )


def load_runtime() -> dict[str, Any] | None:
    """延迟导入可选的 zhiyan 运行时组件。

    即使本地没有安装 AgentLens 依赖，hook 主链路也必须继续工作，
    所以这里返回 ``None``，而不是把导入异常抛出去。
    """
    global _RUNTIME
    if _RUNTIME is not None:
        return _RUNTIME
    try:
        from zhiyanllm import Zhiyanllm
        from zhiyanllm.opentelemetry.instrumentation.semconv_ai import (
            SpanAttributes as ZhiyanSpanAttributes,
            ZhiyanllmSpanKindValues,
        )
        from zhiyanllm.tracing.context_manager import get_tracer
        from zhiyanllm.tracing.manual import track_llm_call, track_task_server_call
        from zhiyanllm.tracing.tracing import TracerWrapper
        from opentelemetry.context import attach as otel_attach, detach as otel_detach
    except Exception:
        _RUNTIME = None
        return None

    _RUNTIME = {
        "Zhiyanllm": Zhiyanllm,
        "track_llm_call": track_llm_call,
        "track_task_server_call": track_task_server_call,
        "TracerWrapper": TracerWrapper,
        "get_tracer": get_tracer,
        "ZhiyanSpanAttributes": ZhiyanSpanAttributes,
        "ZhiyanllmSpanKindValues": ZhiyanllmSpanKindValues,
        "otel_attach": otel_attach,
        "otel_detach": otel_detach,
    }
    return _RUNTIME


def ensure_initialized(config: AgentLensConfig) -> dict[str, Any] | None:
    """仅当有效配置签名发生变化时才重新初始化 zhiyan。"""
    global _INIT_SIGNATURE
    if not config.enabled:
        return None
    runtime = load_runtime()
    if runtime is None:
        return None
    signature = (str(config.endpoint), str(config.api_key), str(config.app_name))
    if _INIT_SIGNATURE == signature:
        return runtime
    runtime["Zhiyanllm"].init(
        app_name=str(config.app_name),
        api_endpoint=str(config.endpoint),
        api_key=str(config.api_key),
        disable_batch=True,
    )
    _INIT_SIGNATURE = signature
    return runtime


def project_root_from_state_path(state_path: Path) -> Path:
    """尽力从 `logs/.state.json` 反推出项目根目录。"""
    try:
        current = state_path.resolve().parent
        for parent in [current, *current.parents]:
            if parent.name == ".codebuddy":
                return parent.parent
        return state_path.resolve().parents[4]
    except Exception:
        return Path.cwd().resolve()


def debug_enabled(state_path: Path) -> bool:
    """判断是否要把 span 调试信息镜像写入 `agentlens-push-debug.ndjson`。"""
    root = project_root_from_state_path(state_path)
    env_file = read_env_file(root / ".env")
    env_local_file = read_env_file(root / ".env.local")
    raw = (
        os.environ.get("AGENTLENS_PUSH_DEBUG")
        or env_local_file.get("AGENTLENS_PUSH_DEBUG")
        or env_file.get("AGENTLENS_PUSH_DEBUG")
        or ""
    )
    return str(raw).strip().lower() in {"1", "true", "yes", "on"}


def debug_write_span(
    state_path: Path | None,
    sid: str | None,
    payload: dict[str, Any],
    *,
    sanitizer,
) -> None:
    """在开启 AgentLens 调试模式时追加写入清洗后的调试记录。"""
    if state_path is None or not sid:
        return
    try:
        if not debug_enabled(state_path):
            return
        debug_path = state_path.parent / "agentlens-push-debug.ndjson"
        record = {"sid": sid, **sanitizer(payload)}
        with debug_path.open("a", encoding="utf-8") as fp:
            fp.write(json.dumps(record, ensure_ascii=False, sort_keys=True) + "\n")
    except Exception:
        return


def set_failure(state_path: Path, sid: str, message: str) -> None:
    """记录最近一次 AgentLens 失败，但不打断 hook 主流程。"""
    st.update_agentlens_session(
        state_path,
        sid,
        {"enabled": False, "last_error": str(message)},
    )


def get_session_context(state_path: Path, sid: str) -> dict[str, Any]:
    """从共享状态里读取 AgentLens sidecar 的 session payload。"""
    return st.load_agentlens_session(state_path, sid)
