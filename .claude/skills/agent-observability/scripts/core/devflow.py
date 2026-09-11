"""Devflow 感知层（可选增强，不属于通用采集核心）。

这一层只在项目实际跑着 `.codebuddy/runtime` 描述的 multi-agents-devflow 工作流时才生效：
- 判断当前 session 是否属于某个 devflow team（`multi-agents-devflow-{task_slug}`）
- 读取该 team 对应的 `workflow-state.json`，投影成精简 stage 快照
- 和上一次观测到的快照 diff，只把真正变化的 stage 产出为事件

任何解析失败（team 目录不存在 / workflow-state.json 缺失或损坏 / 字段缺失）都必须
优雅降级为 None / 空列表，绝不能让不跑 devflow 的普通项目因为这一层报错。
"""
from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from . import agent_identity, state as st


def task_slug_from_team_dir(team_dir: Path) -> str | None:
    """从 devflow team 目录名里剥离出 task_slug（固定前缀 `multi-agents-devflow-`）。"""
    name = team_dir.name
    prefix = agent_identity.TEAM_PREFIX
    if not name.startswith(prefix):
        return None
    slug = name[len(prefix):].strip()
    return slug or None


def resolve_devflow_context(cwd: str, sid: str, cached_team_dir: str | None = None) -> dict[str, Any] | None:
    """判断当前 session 是否处于某次 devflow 运行中；不是则返回 None。

    优先复用 `agent_identity.resolve_team_dir` 做 team 发现（Classic 全部场景，以及
    Portable 在 `topology: team` 宿主——目前是 CodeBuddy——下也走同一套
    `multi-agents-devflow-{task_slug}` 命名，可以直接复用，不用区分 edition）。

    `topology: spawn` 的宿主（Codex/Claude/Cursor 的 Portable 适配器）不创建 team
    目录，找不到时退化成 `_scan_artifacts_for_active_run` 直接扫 `artifacts/` 目录。
    这个兜底本身就是尽力而为的启发式，不保证唯一/精确，见该函数的说明。

    `artifacts_dir` 按 `devflow.defaults.yaml` 的默认值 `{project_root}/artifacts/{task_slug}`
    推算——项目若覆写了 `artifacts.root_dir`，这里暂不感知，读取 workflow-state.json
    找不到文件会安全返回 None，不会误报。
    """
    team_dir = agent_identity.resolve_team_dir(cwd, sid, cached_team_dir)
    if team_dir is not None:
        task_slug = task_slug_from_team_dir(team_dir)
        if task_slug:
            artifacts_dir = Path(cwd).expanduser() / "artifacts" / task_slug
            workflow_state_path = artifacts_dir / "workflow-state.json"
            return {
                "task_slug": task_slug,
                "team_dir": str(team_dir),
                "artifacts_dir": str(artifacts_dir),
                "workflow_state_path": str(workflow_state_path),
            }
    return _scan_artifacts_for_active_run(cwd)


def _scan_artifacts_for_active_run(cwd: str) -> dict[str, Any] | None:
    """没有 team 目录时的兜底发现：直接扫 `{cwd}/artifacts/*/workflow-state.json`。

    用于 `topology: spawn` 的宿主（没有 `.codebuddy/teams/` 这类目录可以反查）。
    这是启发式，不是精确匹配：多个 task_slug 同时在跑时，优先选 `status="in_progress"`
    的那个；都不是或都是时选文件 mtime 最新的一个。项目里如果同时有多个真正并发的
    devflow 运行，这个兜底可能选错——已知限制，不在这次范围内解决。
    """
    artifacts_root = Path(cwd).expanduser() / "artifacts"
    if not artifacts_root.is_dir():
        return None
    candidates: list[tuple[tuple[int, float], str, Path]] = []
    try:
        children = list(artifacts_root.iterdir())
    except Exception:
        return None
    for child in children:
        if not child.is_dir():
            continue
        state_path = child / "workflow-state.json"
        state = read_workflow_state(str(state_path))
        if not isinstance(state, dict) or not isinstance(state.get("stages"), dict):
            continue
        try:
            mtime = state_path.stat().st_mtime
        except Exception:
            mtime = 0.0
        in_progress = 1 if str(state.get("status") or "") == "in_progress" else 0
        candidates.append(((in_progress, mtime), child.name, state_path))
    if not candidates:
        return None
    candidates.sort(key=lambda item: item[0], reverse=True)
    _, task_slug, state_path = candidates[0]
    return {
        "task_slug": task_slug,
        "team_dir": None,
        "artifacts_dir": str(state_path.parent),
        "workflow_state_path": str(state_path),
    }


def read_workflow_state(path: str) -> dict[str, Any] | None:
    """安全读取 workflow-state.json；文件不存在或解析失败都返回 None。"""
    try:
        p = Path(path)
        if not p.is_file():
            return None
        data = json.loads(p.read_text("utf-8"))
        return data if isinstance(data, dict) else None
    except Exception:
        return None


def stage_snapshot(workflow_state: dict[str, Any] | None) -> dict[str, Any]:
    """把 workflow-state.json 投影成精简快照，供比对和事件输出使用。

    兼容两套 schema：Classic（v1.3，字段名 `executor`，有 `review_result`）和
    Portable（v2.0，字段名 `executor_role`，没有 `review_result`，但顶层多了
    `execution_mode`/`host_adapter`/`run_id`/`team_name` 这些 Classic 没有的上下文）。
    按 `version` 字段区分，取不到就都尝试取一遍，不强制要求调用方先判断是哪个 edition。
    """
    if not isinstance(workflow_state, dict):
        return {}
    stages_raw = workflow_state.get("stages")
    stages: dict[str, Any] = {}
    if isinstance(stages_raw, dict):
        for name, info in stages_raw.items():
            if not isinstance(info, dict):
                continue
            stages[name] = {
                "status": info.get("status"),
                "executor": info.get("executor") or info.get("executor_role"),
                "retry_count": info.get("retry_count", 0),
                "review_result": info.get("review_result"),
            }
    return {
        "current_stage": workflow_state.get("current_stage"),
        "size_class": workflow_state.get("size_class"),
        "run_mode": workflow_state.get("run_mode"),
        "schema_version": workflow_state.get("version"),
        "execution_mode": workflow_state.get("execution_mode"),
        "host_adapter": workflow_state.get("host_adapter"),
        "run_id": workflow_state.get("run_id"),
        "stages": stages,
    }


def diff_stage_changes(prev: dict[str, Any] | None, curr: dict[str, Any]) -> list[dict[str, Any]]:
    """比较两次 stage 快照，只返回 status/retry_count/review_result 真正变化的阶段。

    首次观测（`prev` 为 None，即这个 session 第一次检测到 devflow）不产出任何变更——
    避免刚接入 observability 时，把一个已经跑了大半的 devflow 运行的全部历史阶段
    当成"新事件"一次性炸出来。之后每次变化都会被正常捕获。
    """
    if not isinstance(curr, dict) or prev is None:
        return []
    curr_stages = curr.get("stages") or {}
    prev_stages = prev.get("stages") if isinstance(prev, dict) else {}
    if not isinstance(prev_stages, dict):
        prev_stages = {}
    changes: list[dict[str, Any]] = []
    for name, info in curr_stages.items():
        if not isinstance(info, dict):
            continue
        before = prev_stages.get(name)
        before = before if isinstance(before, dict) else {}
        fields = ("status", "retry_count", "review_result")
        if any(before.get(f) != info.get(f) for f in fields):
            changes.append({
                "stage": name,
                "status": info.get("status"),
                "executor": info.get("executor"),
                "retry_count": info.get("retry_count", 0),
                "review_result": info.get("review_result"),
            })
    return changes


def resolve_and_diff(state_path: Path, sid: str, cwd: str) -> dict[str, Any] | None:
    """解析（并缓存）当前 session 的 devflow 上下文，返回上下文 + 本次观测到的 stage 变更。

    缓存策略：
    - 非 devflow session：每个 session 只做一次 team 目录探测，结果（包括"不是
      devflow"这个结论本身）都会缓存，后续调用直接返回，不会反复扫描
      `.codebuddy/teams/`。
    - devflow session：每次调用都会重新读一遍 workflow-state.json（文件很小，
      且只在阶段边界被角色写入），与上次快照 diff。

    返回 None 表示这不是一次 devflow session；否则返回
    `{task_slug, artifacts_dir, workflow_state_path, current_stage, size_class, changes}`。
    """

    def _update(state: dict[str, Any]) -> dict[str, Any] | None:
        dv = st.get_devflow_state(state, sid)

        if not dv.get("checked"):
            context = resolve_devflow_context(cwd, sid, None)
            st.set_devflow_state(state, sid, {"checked": True, "context": context, "last_snapshot": None})
            dv = st.get_devflow_state(state, sid)

        context = dv.get("context")
        if not isinstance(context, dict):
            return None

        workflow_state = read_workflow_state(context.get("workflow_state_path", ""))
        curr = stage_snapshot(workflow_state)
        prev = dv.get("last_snapshot")
        changes = diff_stage_changes(prev if isinstance(prev, dict) else None, curr)
        st.set_devflow_state(state, sid, {"last_snapshot": curr})

        result = dict(context)
        result["current_stage"] = curr.get("current_stage")
        result["size_class"] = curr.get("size_class")
        result["schema_version"] = curr.get("schema_version")
        result["execution_mode"] = curr.get("execution_mode")
        result["changes"] = changes
        return result

    return st.update_state_locked(state_path, _update)
