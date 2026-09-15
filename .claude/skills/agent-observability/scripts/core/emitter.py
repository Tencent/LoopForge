"""ndjson 发射层。

只输出当前真正需要的核心字段：
- common: data_source
- tool: event, sid, ts, agent, tool, ms, skill, rule, transcript_path
- usage: event, sid, ts, agent, tool, tokens, model, cost_usd, transcript_path
- start: event, sid, ts, agent
- stop: event, sid, ts, agent, tokens, model, cost_usd, cost_session_usd, transcript_path
- error: event, sid, ts, phase, error

历史 x_* 扩展字段、stop skill/rule 汇总、schema 版本号等都不再生成。

`cost_usd` 的价格表 = 内置 `config/pricing.json` 与用户覆盖文件（默认
`<project_root>/.codebuddy/agent-observability/pricing.overrides.json`，可用
`AOBS_PRICING_OVERRIDES_PATH` 改路径）的字段级合并结果，详见 `load_prices()`。
"""
from __future__ import annotations

import json
import os
import time
from functools import lru_cache
from pathlib import Path
from typing import Any

from . import cls_sink
from . import state as st

DEFAULT_PRICES_PATH = Path(__file__).resolve().parents[2] / "config" / "pricing.json"
DEFAULT_DATA_SOURCE = "codebuddy-cli"


def _default_overrides_path() -> Path | None:
    """用户定价覆盖文件的默认位置：`<project_root>/.codebuddy/agent-observability/pricing.overrides.json`。

    刻意放在 `skills/` 树**外**：`scripts/build-classic-hosts.py` 会整棵同步
    `.codebuddy/skills`，放树内会 (a) 每次改动都产生 `--check` drift，(b) 把用户的
    自定义价格复制进 `.claude`/`.cursor` 宿主包；放树外则既不参与同步，也不会被
    `config/pricing.json` 的后续更新冲掉。
    """
    try:
        project_root = Path(__file__).resolve().parents[5]
    except IndexError:
        return None
    return project_root / ".codebuddy" / "agent-observability" / "pricing.overrides.json"


# None 表示"拿不到项目根"，此时等同于"没有覆盖文件"（纯内置价格表）。
DEFAULT_OVERRIDES_PATH: Path | None = _default_overrides_path()


def get_log_path(base_dir: Path) -> Path:
    """返回 metrics 日志路径，并确保目录已存在。"""
    log_dir = base_dir / "logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    return log_dir / "metrics.ndjson"


def _first_record_ts(log_path: Path, sid: str) -> float | None:
    """从 metrics.ndjson 中找到某个 session 的最早 ts。

    state 只保留最近几个 session，长流程或历史 session 可能被 prune 后重建，
    因此 `started_at` 不能作为唯一权威。日志里的首条 ts 更接近
    session/workflow 的真实起点。
    """
    if not sid or not log_path.exists():
        return None
    first_ts: float | None = None
    try:
        with log_path.open("r", encoding="utf-8") as fp:
            for line in fp:
                if not line.strip().startswith("{"):
                    continue
                try:
                    rec = json.loads(line)
                except Exception:
                    continue
                if rec.get("sid") != sid:
                    continue
                try:
                    ts = float(rec.get("ts"))
                except Exception:
                    continue
                if first_ts is None or ts < first_ts:
                    first_ts = ts
    except Exception:
        return None
    return first_ts


def _inject_session_duration(record: dict[str, Any], log_path: Path) -> None:
    """从 state 文件读取 session started_at，计算 session_duration_sec 注入 record。
    同时扫描 metrics.ndjson 累计 session 级别 token 总量。"""
    sid = record.get("sid")
    if not sid or "session_duration_sec" in record:
        return
    try:
        now = float(record.get("ts") or time.time())
        started_candidates: list[float] = []
        state_path = log_path.parent / ".state.json"
        state = st.load_state(state_path)
        sess = state.get(sid)
        if isinstance(sess, dict):
            try:
                state_started_at = float(sess.get("started_at") or 0)
            except Exception:
                state_started_at = 0.0
            if state_started_at > 0:
                started_candidates.append(state_started_at)
        log_started_at = _first_record_ts(log_path, str(sid))
        if log_started_at is not None and log_started_at > 0:
            started_candidates.append(log_started_at)
        if not started_candidates:
            started_candidates.append(now)
        started_at = min(started_candidates)
        record["session_duration_sec"] = round(max(0.0, now - started_at), 3)
    except Exception:
        return

    # 累计 session 级别 token（扫描 metrics.ndjson 尾部）
    try:
        total_input = 0
        total_output = 0
        total_cache = 0
        if log_path.exists():
            with log_path.open("r", encoding="utf-8") as fp:
                for line in fp:
                    if not line.strip().startswith("{"):
                        continue
                    try:
                        rec = json.loads(line)
                    except Exception:
                        continue
                    if rec.get("sid") != sid or rec.get("event") != "usage":
                        continue
                    tokens = rec.get("tokens")
                    if not isinstance(tokens, dict):
                        continue
                    total_input += int(tokens.get("input") or 0)
                    total_output += int(tokens.get("output") or 0)
                    total_cache += int(tokens.get("cache_read") or 0)
        record["session_total_tokens"] = total_input + total_output
        record["session_input_tokens"] = total_input
        record["session_output_tokens"] = total_output
        record["session_cache_tokens"] = total_cache
    except Exception:
        return


def emit(log_path: Path, record: dict[str, Any]) -> None:
    """向 ndjson 日志追加一条记录。"""
    record.setdefault("data_source", DEFAULT_DATA_SOURCE)
    record.setdefault("ts", time.time())
    _inject_session_duration(record, log_path)
    with log_path.open("a", encoding="utf-8") as fp:
        fp.write(json.dumps(record, ensure_ascii=False) + "\n")
    try:
        cls_sink.mirror_record(record)
    except Exception:
        return


def build_tool_event(
    sid: str,
    tool: str | None,
    duration_ms: int | None,
    active_agent: str | None = None,
    transcript_path: str | None = None,
    turn_id: str | None = None,
    task_slug: str | None = None,
    stage: str | None = None,
    pt_id: str | None = None,
) -> dict[str, Any]:
    """构造 tool 事件记录，只包含调用信息与耗时。

    `task_slug` / `stage` / `pt_id` 只有在检测到 devflow 运行时才会出现（见
    `core/devflow.py`），非 devflow 项目不受影响。
    """
    record = {
        "event": "tool",
        "sid": sid,
        "agent": active_agent or "main",
        "tool": tool,
        "ms": duration_ms,
    }
    if transcript_path:
        record["transcript_path"] = transcript_path
    if turn_id:
        record["turn_id"] = turn_id
    if task_slug:
        record["task_slug"] = task_slug
    if stage:
        record["stage"] = stage
    if pt_id:
        record["pt_id"] = pt_id
    return record


def build_usage_event(
    sid: str,
    tool: str | None,
    tokens: dict[str, Any],
    active_agent: str | None = None,
    model: str | None = None,
    cost_usd: float | None = None,
    transcript_path: str | None = None,
    source_offset: int | None = None,
    turn_id: str | None = None,
    message_id: str | None = None,
    task_slug: str | None = None,
    stage: str | None = None,
) -> dict[str, Any]:
    """构造 LLM usage 事件记录。"""
    record: dict[str, Any] = {
        "event": "usage",
        "sid": sid,
        "agent": active_agent or "main",
        "tool": tool,
        "tokens": tokens,
    }
    if model:
        record["model"] = model
    if cost_usd is not None:
        record["cost_usd"] = cost_usd
    if transcript_path:
        record["transcript_path"] = transcript_path
    if source_offset is not None:
        record["source_offset"] = source_offset
    if turn_id:
        record["turn_id"] = turn_id
    if message_id:
        record["message_id"] = message_id
    if task_slug:
        record["task_slug"] = task_slug
    if stage:
        record["stage"] = stage
    return record


def build_start_event(
    sid: str,
    agent: str | None = None,
) -> dict[str, Any]:
    """构造最小化的 start 事件记录。

    初始化阶段没有明确角色时，统一记为 ``main``。
    """
    return {
        "event": "start",
        "sid": sid,
        "agent": agent or "main",
    }


def build_prompt_submit_event(
    sid: str,
    turn_id: str,
    agent: str | None = None,
    prompt_len: int | None = None,
) -> dict[str, Any]:
    """构造 `user_prompt_submit` 事件，作为 turn 边界标记。

    这条记录会落到 metrics.ndjson，方便下游把 turn 和 AgentLens trace 对上。
    """
    record: dict[str, Any] = {
        "event": "user_prompt_submit",
        "sid": sid,
        "agent": agent or "main",
        "turn_id": turn_id,
    }
    if prompt_len is not None:
        record["prompt_len"] = int(prompt_len)
    return record


def build_stop_event(
    sid: str,
    tokens: dict | None = None,
    model: str | None = None,
    cost_usd: float | None = None,
    cost_session_usd: float | None = None,
    active_agent: str | None = None,
    transcript_path: str | None = None,
    source_offset: int | None = None,
    turn_id: str | None = None,
    message_id: str | None = None,
    task_slug: str | None = None,
    stage: str | None = None,
) -> dict[str, Any]:
    """构造最小化的 stop 事件记录。"""
    record: dict[str, Any] = {
        "event": "stop",
        "sid": sid,
        "agent": active_agent or "main",
    }
    if tokens:
        record["tokens"] = tokens
    if model:
        record["model"] = model
    if cost_usd is not None:
        record["cost_usd"] = cost_usd
    if cost_session_usd is not None:
        record["cost_session_usd"] = cost_session_usd
    if transcript_path:
        record["transcript_path"] = transcript_path
    if source_offset is not None:
        record["source_offset"] = source_offset
    if turn_id:
        record["turn_id"] = turn_id
    if message_id:
        record["message_id"] = message_id
    if task_slug:
        record["task_slug"] = task_slug
    if stage:
        record["stage"] = stage
    return record


def build_error_event(
    phase: str,
    error: str,
    sid: str | None = None,
) -> dict[str, Any]:
    """构造最小化的 error 事件记录。"""
    return {
        "event": "error",
        "sid": sid or "",
        "phase": phase,
        "error": error,
    }


def build_stage_transition_event(
    sid: str,
    task_slug: str,
    stage: str,
    status: str | None = None,
    executor: str | None = None,
    retry_count: int = 0,
    review_result: str | None = None,
) -> dict[str, Any]:
    """构造 devflow stage 变更事件（只在检测到 devflow 运行、且 stage 真的变化时发出）。

    对应 `workflow-state.json` 里某个 stage 的 `status`/`retry_count`/`review_result`
    发生变化——retry_count 上升或 review_result="failed" 是 devflow 里最值得关注的
    信号（返工/打回循环），比 hook 自身的 `error` 事件更贴近业务语义。
    """
    record: dict[str, Any] = {
        "event": "stage_transition",
        "sid": sid,
        "task_slug": task_slug,
        "stage": stage,
    }
    if status:
        record["status"] = status
    if executor:
        record["executor"] = executor
    if retry_count:
        record["retry_count"] = int(retry_count)
    if review_result:
        record["review_result"] = review_result
    return record


def resolve_overrides_path() -> Path | None:
    """返回当前生效的覆盖文件路径：`AOBS_PRICING_OVERRIDES_PATH` 优先，否则默认路径。

    返回 `None` 表示"不加载任何覆盖"（纯内置价格表）。支持 `~` 展开。
    """
    raw = os.environ.get("AOBS_PRICING_OVERRIDES_PATH", "").strip()
    if raw:
        return Path(raw).expanduser()
    return DEFAULT_OVERRIDES_PATH


def _coerce_price_table(data: Any) -> dict[str, dict[str, float]]:
    """把任意 JSON 结果规整成 `{model: {field: float}}`，坏数据按字段/按模型跳过。

    与内置表加载的差别只有一处：内置表遇到非数字会整体抛错降级，这里逐字段跳过——
    用户的补丁文件里写错一个字段不应该把其余正确的覆盖一起丢掉，更不应该让 hook 挂掉。
    """
    if not isinstance(data, dict):
        return {}
    table: dict[str, dict[str, float]] = {}
    for raw_key, raw_value in data.items():
        if not isinstance(raw_value, dict):
            continue
        key = str(raw_key).strip().lower()
        if not key:
            continue
        row: dict[str, float] = {}
        for price_key, price_value in raw_value.items():
            # bool 是 int 的子类，True 会被 float() 变成 1.0，这里按"不是价格"处理。
            if price_value is None or isinstance(price_value, bool):
                continue
            try:
                row[str(price_key).strip().lower()] = float(price_value)
            except (TypeError, ValueError):
                continue
        # 全是坏字段的模型不落表：否则它会被 `lookup_price` 命中成 {}，
        # 既算不出成本又不算"未定价"，看板上会变成一个查不到原因的空洞。
        if row:
            table[key] = row
    return table


def load_price_overrides(path: Path | None = None) -> dict[str, dict[str, float]]:
    """加载用户定价覆盖文件；任何异常都静默降级为空覆盖（纯内置价格表）。

    静默是刻意的：这个文件是给人手写的常驻配置，hook 每次都是新进程，
    一旦因格式问题抛异常/打印，会让整条 hook 链路变得不可用。
    """
    try:
        target = path if path is not None else resolve_overrides_path()
        if not target:
            return {}
        with Path(target).expanduser().open("r", encoding="utf-8") as fp:
            data = json.load(fp)
    except Exception:
        return {}
    return _coerce_price_table(data)


def merge_prices(
    base: dict[str, dict[str, float]] | None,
    overrides: dict[str, dict[str, float]] | None,
) -> dict[str, dict[str, float]]:
    """字段级合并价格表：覆盖里写出的字段替换内置值，没写的字段沿用内置值。

    可以新增内置表里不存在的模型；**不支持删除**内置模型或字段（合并只能加不能减）。
    """
    merged: dict[str, dict[str, float]] = {}
    for model, row in (base or {}).items():
        if isinstance(row, dict):
            merged[model] = dict(row)
    for model, patch in (overrides or {}).items():
        if not isinstance(patch, dict):
            continue
        merged.setdefault(model, {}).update(patch)
    return merged


def _load_builtin_prices() -> dict[str, dict[str, float]]:
    """从 `config/pricing.json` 中一次性加载内置模型价格配置（不含用户覆盖）。"""
    path = os.environ.get("AOBS_PRICES_PATH", "").strip()
    prices_path = Path(path).expanduser() if path else DEFAULT_PRICES_PATH
    try:
        with prices_path.open("r", encoding="utf-8") as fp:
            data = json.load(fp)
    except Exception:
        return {}
    if not isinstance(data, dict):
        return {}
    return {
        str(key).lower(): {price_key: float(price_value) for price_key, price_value in value.items()}
        for key, value in data.items()
        if isinstance(value, dict)
    }


@lru_cache(maxsize=1)
def load_prices() -> dict[str, dict[str, float]]:
    """内置价格表 + 用户覆盖文件的合并结果（字段级）。

    结果带 `lru_cache`：hook 是短命进程，一次会话里只该读一次盘；长驻进程或单测里
    改了覆盖文件/环境变量后，调 `clear_price_cache()` 让下一次调用重新加载。
    """
    return merge_prices(_load_builtin_prices(), load_price_overrides())


def clear_price_cache() -> None:
    """丢弃 `load_prices()` 的缓存，让下一次调用重读磁盘与环境变量。"""
    load_prices.cache_clear()


def lookup_price(model: str | None) -> dict[str, float] | None:
    """为一个具体模型名找到最匹配的价格配置项。"""
    if not model or not isinstance(model, str):
        return None
    normalized = model.lower().strip()
    if not normalized:
        return None
    prices = load_prices()
    if normalized in prices:
        return prices[normalized]
    best_key: str | None = None
    for key in prices:
        if key in normalized and (best_key is None or len(key) > len(best_key)):
            best_key = key
    if best_key is None:
        return None
    return prices[best_key]


def is_unpriced(model: str | None) -> bool:
    """模型是否完全没命中价格表（含模糊匹配）。

    "未定价"的唯一口径就是 `lookup_price(...) is None`（D4）：模糊（最长子串）命中的
    模型能算出成本，就不该出现在看板的"建议补价"提示里。
    """
    return lookup_price(model) is None


def estimate_cost(tokens: dict | None, model: str | None) -> dict[str, Any] | None:
    """根据归一化后的 token 总量估算单条 usage 事件的成本。"""
    if not isinstance(tokens, dict):
        return None
    rates = lookup_price(model)
    if not rates:
        return None

    def _int(key: str) -> int:
        try:
            return int(tokens.get(key) or 0)
        except Exception:
            return 0

    output = _int("output")
    cache_read = _int("cache_read")
    cache_write = _int("cache_creation")
    raw_input = _int("input")
    fresh_input = raw_input - cache_read - cache_write if raw_input and raw_input >= (cache_read + cache_write) else raw_input
    cost = (
        fresh_input * rates.get("input", 0.0)
        + cache_read * rates.get("cache_read", 0.0)
        + cache_write * rates.get("cache_write", 0.0)
        + output * rates.get("output", 0.0)
    ) / 1_000_000.0

    matched_key = None
    normalized = (model or "").lower()
    for key in load_prices():
        if key in normalized and (matched_key is None or len(key) > len(matched_key)):
            matched_key = key
    if matched_key is None and normalized in load_prices():
        matched_key = normalized

    return {
        "usd": round(cost, 6),
        "model_matched": matched_key,
        "rates": rates,
    }


def cost_of(tokens: dict | None, model: str | None) -> float | None:
    """只返回美元成本值的便捷封装。"""
    if not tokens or not model:
        return None
    info = estimate_cost(tokens, model)
    if not info:
        return None
    return info.get("usd")


def sum_session_cost(log_path: Path, sid: str) -> float | None:
    """尽力根据已发出的事件反算整个 session 的总成本。"""
    if not log_path.exists() or not sid:
        return None
    total = 0.0
    seen = False
    try:
        with log_path.open("r", encoding="utf-8") as fp:
            for line in fp:
                if not line.strip().startswith("{"):
                    continue
                try:
                    rec = json.loads(line)
                except Exception:
                    continue
                if rec.get("sid") != sid or rec.get("event") not in {"usage", "tool"}:
                    continue
                cost = rec.get("cost_usd")
                if isinstance(cost, (int, float)):
                    total += float(cost)
                    seen = True
    except Exception:
        return None
    return round(total, 6) if seen else None
