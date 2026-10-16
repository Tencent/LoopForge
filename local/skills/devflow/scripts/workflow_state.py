#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
import re
import shutil
import tempfile
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import List, Optional

from validate_artifacts import validate_file
from validate_design_artifacts import validate_design_context
from validate_plan import validate_execution_plan
from template_registry import materialize_stage, required_artifacts, system_template
from agent_registry import HELPER_ROLES, STAGE_EXECUTION, STAGE_ROLES
from adapter_registry import (
    ADAPTERS, DISPATCH_TOOLS, RESEARCH_EXECUTORS, STAGE_EXECUTORS,
    TEAM_NAME_PREFIXES, TOPOLOGIES,
)
from build_stage_prompt import build_prompt


SKILL_ROOT = Path(__file__).resolve().parent.parent
DEFAULTS = json.loads((SKILL_ROOT / "config/workflow.json").read_text(encoding="utf-8"))
TEMPLATE = system_template("workflow-state")
ALL_STAGES = {"PHASE-0", "SOLO", "REQUIREMENT", "DESIGN", "IMPLEMENT", "REVIEW", "TEST", "KNOWLEDGE", "SUMMARY"}
STATUSES = {"pending", "in_progress", "completed", "failed", "skipped"}
WORKFLOW_STATUSES = {"in_progress", "awaiting_approval", "blocked", "completed"}
LEGACY_STAGE_ROLES = {"REQUIREMENT": {"devflow-requirement-analyst"}}
LEGACY_V2_EXECUTORS = {
    "codebuddy": ("devflow-stage-executor", "devflow-research-helper"),
}


def now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def expected_executor_type(data: dict, research: bool = False) -> Optional[str]:
    adapter = data.get("host_adapter")
    if data.get("execution_policy_version") == 2 and adapter in LEGACY_V2_EXECUTORS:
        return LEGACY_V2_EXECUTORS[adapter][1 if research else 0]
    registry = RESEARCH_EXECUTORS if research else STAGE_EXECUTORS
    return registry.get(adapter)


def expected_team_name(data: dict) -> Optional[str]:
    adapter = data.get("host_adapter")
    if TOPOLOGIES.get(adapter) != "team":
        return None
    prefix = TEAM_NAME_PREFIXES.get(adapter) or ""
    base = prefix + data.get("task_slug", "")
    run_id = data.get("run_id")
    return f"{base}-{run_id}" if run_id else base


def allocate_run_id(adapter: Optional[str]) -> Optional[str]:
    if TOPOLOGIES.get(adapter) != "team":
        return None
    return uuid.uuid4().hex[:12]


def load(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def file_digest(path: Path) -> Optional[str]:
    if not path.is_file():
        return None
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()


def tree_digest(path: Path) -> Optional[str]:
    if not path.is_dir():
        return None
    digest = hashlib.sha256()
    files = sorted(item for item in path.rglob("*") if item.is_file())
    if not files:
        return None
    for item in files:
        digest.update(str(item.relative_to(path)).encode("utf-8"))
        digest.update(bytes.fromhex(file_digest(item) or ""))
    return digest.hexdigest()


def atomic_write(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.exists():
        shutil.copy2(path, path.with_suffix(path.suffix + ".bak"))
    descriptor, temporary = tempfile.mkstemp(prefix=path.name, dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(data, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def state_errors(data: dict, check_files: bool = False) -> List[str]:
    errors = []
    if data.get("version") != DEFAULTS["version"]:
        errors.append(f"version must be {DEFAULTS['version']}")
    size = data.get("size_class")
    if size not in DEFAULTS["routes"]:
        errors.append("invalid size_class")
    configured_route = DEFAULTS["routes"].get(size, [])
    compatible_routes = DEFAULTS.get("compatible_routes", {}).get(size, [])
    route = data.get("route")
    if route != configured_route and route not in compatible_routes:
        errors.append("route does not match size_class")
    expected_route = route if isinstance(route, list) else configured_route
    stages = data.get("stages", {})
    if not isinstance(stages, dict):
        errors.append("stages must be an object")
        stages = {}
    if set(stages) != ALL_STAGES:
        errors.append("stage set does not match schema")
    for name, stage in stages.items():
        if not isinstance(stage, dict):
            errors.append(f"{name}: stage must be an object")
            continue
        if stage.get("status") not in STATUSES:
            errors.append(f"{name}: invalid status")
        if name in DEFAULTS.get("optional_stages", []) and name in expected_route:
            decision = stage.get("decision")
            if decision not in {None, "run", "skip"}:
                errors.append(f"{name}: invalid optional-stage decision")
            if stage.get("status") == "skipped" and decision != "skip":
                errors.append(f"{name}: skipped optional stage requires decision=skip")
        elif stage.get("status") == "skipped" and name in expected_route:
            errors.append(f"{name}: only optional stages may be skipped")
    if data.get("next_stage") not in {None, *expected_route}:
        errors.append("next_stage is outside route")
    if data.get("status") not in WORKFLOW_STATUSES:
        errors.append("invalid workflow status")
    if data.get("run_mode") not in {"auto", "manual"}:
        errors.append("invalid run_mode")
    if not isinstance(data.get("requires_design_artifacts", False), bool):
        errors.append("requires_design_artifacts must be boolean")
    policy_version = data.get("execution_policy_version")
    if policy_version not in {None, 1, 2, 3}:
        errors.append("unsupported execution_policy_version")
    if policy_version in {1, 2, 3}:
        execution_mode = data.get("execution_mode")
        if execution_mode not in {"isolated", "single-context"}:
            errors.append("invalid execution_mode")
        host_adapter = data.get("host_adapter")
        if execution_mode == "isolated" and host_adapter not in ADAPTERS:
            errors.append("isolated mode requires a supported host_adapter")
        if execution_mode == "single-context" and host_adapter is not None:
            errors.append("single-context mode must not declare host_adapter")
        coordinator_id = data.get("coordinator_id")
        if not isinstance(coordinator_id, str) or not coordinator_id.strip():
            errors.append("coordinator_id must be non-empty")
        if size in {"medium", "large"} and execution_mode == "single-context":
            if not isinstance(data.get("fallback_reason"), str) or not data["fallback_reason"].strip():
                errors.append("medium/large single-context mode requires fallback_reason")
        if policy_version >= 3 and execution_mode == "isolated":
            topology = TOPOLOGIES.get(host_adapter)
            if data.get("executor_topology") != topology:
                errors.append(f"executor_topology must be {topology}")
            required_team = expected_team_name(data)
            if required_team is not None and data.get("team_name") != required_team:
                errors.append(f"team_name must be {required_team}")
            if required_team is None and data.get("team_name") is not None:
                errors.append("spawn topology must not declare team_name")
            run_id = data.get("run_id")
            if run_id is not None and not re.fullmatch(r"[0-9a-f]{12}", str(run_id)):
                errors.append("run_id must be 12 lowercase hex characters")
    approval_gates = {
        *DEFAULTS.get("required_approval_gates", []), *DEFAULTS["manual_gates"]
    }
    if data.get("awaiting_approval") not in {None, *approval_gates}:
        errors.append("invalid awaiting_approval")
    if data.get("current_stage") not in {"PHASE-0", *expected_route}:
        errors.append("current_stage is outside route")
    for name, stage in stages.items():
        if not isinstance(stage, dict):
            continue
        retry_count = stage.get("retry_count")
        if not isinstance(retry_count, int) or retry_count < 0:
            errors.append(f"{name}: invalid retry_count")
    if policy_version in {1, 2, 3}:
        assigned = {}
        for name in expected_route:
            stage = stages.get(name)
            if not isinstance(stage, dict):
                continue
            role = stage.get("executor_role")
            executor_type = stage.get("executor_type")
            executor_id = stage.get("executor_id")
            expected_role = STAGE_ROLES[name]
            coordinator_stage = STAGE_EXECUTION[name] == "coordinator" and (
                policy_version >= 2 or stage.get("dispatch_tool") == "coordinator"
            )
            expected_dispatch = DISPATCH_TOOLS.get(data.get("host_adapter"))
            expected_type = (
                "coordinator" if coordinator_stage
                else expected_executor_type(data)
            )
            role_is_legacy = policy_version == 1 and role in LEGACY_STAGE_ROLES.get(name, set())
            if role is not None and role != expected_role and not role_is_legacy:
                errors.append(f"{name}: executor_role must be {expected_role}")
            if executor_id is not None:
                if not isinstance(executor_id, str) or not executor_id.strip():
                    errors.append(f"{name}: executor_id must be non-empty")
                elif executor_id == data.get("coordinator_id") and not coordinator_stage:
                    errors.append(f"{name}: coordinator cannot be the isolated stage executor")
                elif coordinator_stage and executor_id != data.get("coordinator_id"):
                    errors.append(f"{name}: coordinator stage must use coordinator_id")
                elif not coordinator_stage and executor_id in assigned and assigned[executor_id] != name:
                    errors.append(f"{name}: executor_id is already assigned to {assigned[executor_id]}")
                elif not coordinator_stage:
                    assigned[executor_id] = name
            if data.get("execution_mode") == "isolated" and stage.get("status") in {"in_progress", "completed"}:
                required_dispatch = "coordinator" if coordinator_stage else expected_dispatch
                if (role != expected_role and not role_is_legacy) or not executor_id or stage.get("dispatch_tool") != required_dispatch:
                    errors.append(f"{name}: stage has no valid executor")
                if policy_version >= 2 and executor_type != expected_type:
                    errors.append(f"{name}: executor_type must be {expected_type}")
                if policy_version >= 3 and not coordinator_stage:
                    topology = TOPOLOGIES.get(data.get("host_adapter"))
                    if stage.get("executor_topology") != topology:
                        errors.append(f"{name}: executor_topology must be {topology}")
                    if topology == "team" and stage.get("team_name") != data.get("team_name"):
                        errors.append(f"{name}: team_name does not match workflow team")
            helpers = stage.get("helpers", [])
            if not isinstance(helpers, list):
                errors.append(f"{name}: helpers must be a list")
                continue
            for helper in helpers:
                if not isinstance(helper, dict):
                    errors.append(f"{name}: invalid helper record")
                    continue
                if helper.get("role") not in HELPER_ROLES:
                    errors.append(f"{name}: invalid research helper role")
                if helper.get("dispatch_tool") != expected_dispatch:
                    errors.append(f"{name}: helper dispatch_tool does not match host adapter")
                if not helper.get("executor_id") or not helper.get("purpose"):
                    errors.append(f"{name}: incomplete research helper record")
                if policy_version >= 2 and helper.get("executor_type") != expected_executor_type(
                    data, research=True
                ):
                    errors.append(
                        f"{name}: research executor_type must be "
                        f"{expected_executor_type(data, research=True)}"
                    )
                if policy_version >= 3:
                    topology = TOPOLOGIES.get(data.get("host_adapter"))
                    if helper.get("executor_topology") != topology:
                        errors.append(f"{name}: research executor_topology must be {topology}")
                    if topology == "team" and helper.get("team_name") != data.get("team_name"):
                        errors.append(f"{name}: research helper team_name does not match workflow team")
        helper_ids = set()
        reserved_ids = {data.get("coordinator_id"), *assigned.keys()}
        for name in expected_route:
            stage = stages.get(name)
            if not isinstance(stage, dict) or not isinstance(stage.get("helpers", []), list):
                continue
            for helper in stage.get("helpers", []):
                if not isinstance(helper, dict):
                    continue
                helper_id = helper.get("executor_id")
                if helper_id in reserved_ids:
                    errors.append(f"{name}: helper executor_id conflicts with coordinator or stage executor")
                elif helper_id in helper_ids:
                    errors.append(f"{name}: helper executor_id is reused")
                elif helper_id:
                    helper_ids.add(helper_id)
    workflow_status = data.get("status")
    awaiting = data.get("awaiting_approval")
    if (workflow_status == "awaiting_approval") != (awaiting is not None):
        errors.append("workflow status and awaiting_approval disagree")
    if workflow_status == "blocked" and data.get("next_stage") is not None:
        errors.append("blocked workflow must not have next_stage")
    if workflow_status == "completed":
        if data.get("next_stage") is not None:
            errors.append("completed workflow must not have next_stage")
        optional_stages = set(DEFAULTS.get("optional_stages", []))
        if any(
            not isinstance(stages.get(name), dict)
            or stages[name].get("status") not in (
                {"completed", "skipped"} if name in optional_stages else {"completed"}
            )
            for name in expected_route
        ):
            errors.append("completed workflow has incomplete route stages")
    project_root = Path(data.get("project_root", ""))
    task_slug = data.get("task_slug", "")
    if task_slug:
        expected_artifacts = project_root / "artifacts" / task_slug
        if Path(data.get("artifacts_dir", "")) != expected_artifacts:
            errors.append("artifacts_dir does not match project_root and task_slug")
    if check_files:
        artifact_root = Path(data.get("artifacts_dir", ""))
        for stage_name, stage in stages.items():
            if not isinstance(stage, dict):
                continue
            if stage.get("status") != "completed" or stage_name == "PHASE-0":
                continue
            for relative in required_artifacts(
                stage_name, bool(data.get("requires_design_artifacts", False))
            ):
                if not (artifact_root / relative).is_file():
                    errors.append(f"{stage_name}: missing {relative}")
                else:
                    errors.extend(validate_file(artifact_root / relative, relative))
            if stage_name == "DESIGN" and data.get("requires_design_artifacts", False):
                errors.extend(validate_design_context(artifact_root / "02-design/design-context"))
    return errors


def next_after(route: List[str], stage: str) -> Optional[str]:
    index = route.index(stage)
    return route[index + 1] if index + 1 < len(route) else None


def command_init(args: argparse.Namespace) -> int:
    root = args.project_root.resolve()
    if not root.is_dir():
        raise SystemExit("project root does not exist")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,79}", args.slug) or ".." in args.slug:
        raise SystemExit("unsafe task slug")
    data = load(TEMPLATE)
    stamp = now()
    route = DEFAULTS["routes"][args.size]
    execution_mode = args.execution_mode or ("single-context" if args.size == "small" else "isolated")
    if args.size in {"medium", "large"} and execution_mode == "single-context" and not args.fallback_reason:
        raise SystemExit("medium/large single-context mode requires --fallback-reason")
    if execution_mode == "isolated" and not args.host_adapter:
        raise SystemExit(
            "isolated mode requires --host-adapter " + "|".join(ADAPTERS)
        )
    if execution_mode == "single-context" and args.host_adapter:
        raise SystemExit("single-context mode cannot use --host-adapter")
    artifact_root = root / "artifacts" / args.slug
    if artifact_root.exists():
        raise SystemExit(f"artifact directory already exists; resume it or choose a new slug: {artifact_root}")
    run_id = allocate_run_id(args.host_adapter)
    for directory in (
        "01-requirement", "02-design", "02-design/design-context/sections",
        "02-design/design-context/evidence", "03-code", "04-test", "05-knowledge", "01-solo",
    ):
        (artifact_root / directory).mkdir(parents=True, exist_ok=True)
    data.update(
        task_slug=args.slug,
        size_class=args.size,
        run_mode=args.mode,
        requires_design_artifacts=args.require_design_artifacts,
        execution_policy_version=3,
        execution_mode=execution_mode,
        host_adapter=args.host_adapter,
        executor_topology=(
            TOPOLOGIES.get(args.host_adapter) if execution_mode == "isolated" else "single-context"
        ),
        run_id=run_id,
        team_name=None,
        coordinator_id=args.coordinator_id,
        fallback_reason=args.fallback_reason,
        project_root=str(root),
        artifacts_dir=str(artifact_root),
        route=route,
        current_stage="PHASE-0",
        next_stage=route[0],
        created_at=stamp,
        updated_at=stamp,
    )
    data["team_name"] = expected_team_name(data)
    data["stages"]["PHASE-0"].update(started_at=stamp, completed_at=stamp)
    for stage_name, stage in data["stages"].items():
        if stage_name != "PHASE-0" and stage_name not in route:
            stage["status"] = "skipped"
        elif stage_name in STAGE_EXECUTION and STAGE_EXECUTION[stage_name] == "coordinator":
            stage.update(
                executor_role=STAGE_ROLES[stage_name],
                executor_type="coordinator",
                executor_id=args.coordinator_id,
                executor_topology="coordinator",
                dispatch_tool="coordinator",
            )
    data["events"].append({
        "at": stamp,
        "event": "workflow_initialized",
        "size": args.size,
        "requires_design_artifacts": args.require_design_artifacts,
        "execution_mode": execution_mode,
        "host_adapter": args.host_adapter,
        "executor_topology": data.get("executor_topology"),
        "team_name": data.get("team_name"),
        "fallback_reason": args.fallback_reason,
    })
    state = artifact_root / "workflow-state.json"
    atomic_write(state, data)
    print(state)
    return 0


def command_start(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if args.stage != data.get("next_stage"):
        raise SystemExit(f"next stage must be {data.get('next_stage')}")
    if data.get("awaiting_approval"):
        raise SystemExit(f"approval required for {data['awaiting_approval']}")
    stage = data["stages"][args.stage]
    if stage["status"] not in {"pending", "failed"}:
        raise SystemExit("stage cannot start from current status")
    if stage["retry_count"] >= DEFAULTS["max_stage_failures"]:
        raise SystemExit("stage failure limit reached")
    if not stage.get("prepared_at") or not isinstance(stage.get("artifact_baseline"), dict):
        raise SystemExit(f"prepare {args.stage} before assigning or starting its executor")
    if data.get("execution_policy_version") in {1, 2, 3} and data.get("execution_mode") == "isolated":
        expected_role = STAGE_ROLES[args.stage]
        coordinator_stage = STAGE_EXECUTION[args.stage] == "coordinator" and (
            data.get("execution_policy_version") >= 2 or stage.get("dispatch_tool") == "coordinator"
        )
        expected_id = data.get("coordinator_id") if coordinator_stage else None
        role_is_legacy = (
            data.get("execution_policy_version") == 1
            and stage.get("executor_role") in LEGACY_STAGE_ROLES.get(args.stage, set())
        )
        if (stage.get("executor_role") != expected_role and not role_is_legacy) or not stage.get("executor_id") or (
            expected_id is not None and stage.get("executor_id") != expected_id
        ):
            raise SystemExit(
                f"configure the required {expected_role} executor before starting {args.stage}"
            )
    stamp = now()
    stage.update(status="in_progress", started_at=stamp, completed_at=None)
    data.update(current_stage=args.stage, updated_at=stamp, status="in_progress")
    data["events"].append({
        "at": stamp,
        "event": "stage_started",
        "stage": args.stage,
        "executor_role": stage.get("executor_role"),
        "executor_id": stage.get("executor_id"),
    })
    atomic_write(args.state, data)
    print(f"{args.stage} started")
    return 0


def command_prepare(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if args.stage != data.get("next_stage"):
        raise SystemExit(f"next stage must be {data.get('next_stage')}")
    if data.get("awaiting_approval"):
        raise SystemExit(f"approval required for {data['awaiting_approval']}")
    stage = data["stages"][args.stage]
    if args.stage in DEFAULTS.get("optional_stages", []) and stage.get("decision") != "run":
        raise SystemExit(
            f"decide whether to run optional stage {args.stage} before preparing it"
        )
    if stage["status"] not in {"pending", "failed"}:
        raise SystemExit("stage cannot be prepared from current status")
    if stage["retry_count"] >= DEFAULTS["max_stage_failures"]:
        raise SystemExit("stage failure limit reached")
    stamp = now()
    artifact_root = Path(data["artifacts_dir"])
    include_design_context = bool(data.get("requires_design_artifacts", False))
    required = required_artifacts(args.stage, include_design_context)
    baseline = {relative: file_digest(artifact_root / relative) for relative in required}
    if args.stage == "DESIGN" and include_design_context:
        baseline["__design_context__"] = tree_digest(artifact_root / "02-design/design-context")
    created = materialize_stage(args.stage, artifact_root, include_design_context)
    stage.update(artifact_baseline=baseline, prepared_at=stamp)
    data["updated_at"] = stamp
    data["events"].append({
        "at": stamp,
        "event": "stage_prepared",
        "stage": args.stage,
        "templates_created": created,
    })
    atomic_write(args.state, data)
    if args.emit_prompt:
        print(build_prompt(
            data, args.stage, args.request, args.input, args.profile
        ), end="")
    else:
        print(f"{args.stage} prepared; templates_created={len(created)}")
    return 0


def command_decide_knowledge(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if data.get("next_stage") != "KNOWLEDGE":
        raise SystemExit("KNOWLEDGE is not the next stage")
    stage = data["stages"]["KNOWLEDGE"]
    if stage.get("status") != "pending" or stage.get("prepared_at"):
        raise SystemExit("KNOWLEDGE decision must be made before prepare")
    reason = args.reason.strip()
    if not reason:
        raise SystemExit("knowledge decision requires a concrete --reason")
    stamp = now()
    stage.update(decision=args.decision, decision_reason=reason)
    event = {
        "at": stamp,
        "event": "knowledge_decided",
        "decision": args.decision,
        "reason": reason,
    }
    if args.decision == "skip":
        stage.update(status="skipped", completed_at=stamp, artifacts=[])
        data.update(current_stage="KNOWLEDGE", next_stage=next_after(data["route"], "KNOWLEDGE"))
    data["updated_at"] = stamp
    data["events"].append(event)
    atomic_write(args.state, data)
    print(
        f"KNOWLEDGE decision={args.decision}; "
        f"next={data.get('next_stage')} reason={reason}"
    )
    return 0


def command_assign(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if data.get("execution_policy_version") not in {1, 2, 3}:
        raise SystemExit("executor assignment is unavailable for legacy workflow state")
    if args.stage != data.get("next_stage"):
        raise SystemExit(f"next stage must be {data.get('next_stage')}")
    stage = data["stages"][args.stage]
    if stage["status"] not in {"pending", "failed"}:
        raise SystemExit("executor can only be assigned before stage start")
    if not stage.get("prepared_at") or not isinstance(stage.get("artifact_baseline"), dict):
        raise SystemExit(f"prepare {args.stage} before assigning its executor")
    expected_role = STAGE_ROLES[args.stage]
    if STAGE_EXECUTION[args.stage] == "coordinator":
        raise SystemExit(f"{args.stage} is executed by the coordinator and must not be assigned")
    if args.role != expected_role:
        raise SystemExit(f"{args.stage} executor role must be {expected_role}")
    if data.get("execution_policy_version") >= 2:
        expected_type = expected_executor_type(data)
        if args.executor_type != expected_type:
            raise SystemExit(f"{args.stage} executor type must be {expected_type}")
    if args.executor_id == data.get("coordinator_id"):
        raise SystemExit("coordinator cannot be assigned as an isolated stage executor")
    expected_dispatch = DISPATCH_TOOLS.get(data.get("host_adapter"))
    if args.dispatch_tool != expected_dispatch:
        raise SystemExit(f"{data.get('host_adapter')} assignments require --dispatch-tool {expected_dispatch}")
    topology = TOPOLOGIES.get(data.get("host_adapter"))
    if data.get("execution_policy_version") >= 3:
        if topology == "team" and args.team_name != data.get("team_name"):
            raise SystemExit(f"{data.get('host_adapter')} assignments require --team-name {data.get('team_name')}")
        if topology != "team" and args.team_name is not None:
            raise SystemExit(f"{data.get('host_adapter')} does not accept --team-name")
    for name in data["route"]:
        if name != args.stage and data["stages"][name].get("executor_id") == args.executor_id:
            raise SystemExit(f"executor is already assigned to {name}")
    stamp = now()
    stage.update(
        executor_role=args.role,
        executor_type=args.executor_type,
        executor_id=args.executor_id,
        executor_topology=topology,
        team_name=args.team_name,
        dispatch_tool=args.dispatch_tool,
    )
    data["updated_at"] = stamp
    data["events"].append({
        "at": stamp,
        "event": "stage_executor_assigned",
        "stage": args.stage,
        "role": args.role,
        "executor_type": args.executor_type,
        "executor_id": args.executor_id,
        "executor_topology": topology,
        "team_name": args.team_name,
        "dispatch_tool": args.dispatch_tool,
    })
    atomic_write(args.state, data)
    print(f"{args.stage} assigned to {args.role} ({args.executor_id})")
    return 0


def command_helper(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if data.get("execution_policy_version") not in {1, 2, 3}:
        raise SystemExit("helper audit is unavailable for legacy workflow state")
    if args.stage != data.get("next_stage") and args.stage != data.get("current_stage"):
        raise SystemExit("helper must belong to the current or next stage")
    if args.role not in HELPER_ROLES:
        raise SystemExit("research helper role must be devflow-code-explorer or devflow-knowledge-retriever")
    if data.get("execution_policy_version") >= 2:
        expected_type = expected_executor_type(data, research=True)
        if args.executor_type != expected_type:
            raise SystemExit(f"research executor type must be {expected_type}")
    expected_dispatch = DISPATCH_TOOLS.get(data.get("host_adapter"))
    if args.dispatch_tool != expected_dispatch:
        raise SystemExit(f"{data.get('host_adapter')} helpers require --dispatch-tool {expected_dispatch}")
    topology = TOPOLOGIES.get(data.get("host_adapter"))
    if data.get("execution_policy_version") >= 3:
        if topology == "team" and args.team_name != data.get("team_name"):
            raise SystemExit(f"{data.get('host_adapter')} helpers require --team-name {data.get('team_name')}")
        if topology != "team" and args.team_name is not None:
            raise SystemExit(f"{data.get('host_adapter')} does not accept --team-name")
    reserved = {data.get("coordinator_id")}
    reserved.update(
        data["stages"][name].get("executor_id") for name in data["route"]
    )
    reserved.update(
        item.get("executor_id")
        for name in data["route"]
        for item in data["stages"][name].get("helpers", [])
        if isinstance(item, dict)
    )
    if args.executor_id in reserved:
        raise SystemExit("helper executor_id conflicts with coordinator or stage executor")
    stage = data["stages"][args.stage]
    if any(item.get("executor_id") == args.executor_id for item in stage.get("helpers", [])):
        raise SystemExit("helper executor_id is already recorded for this stage")
    stamp = now()
    record = {
        "role": args.role,
        "executor_type": args.executor_type,
        "executor_id": args.executor_id,
        "executor_topology": topology,
        "team_name": args.team_name,
        "purpose": args.purpose,
        "dispatch_tool": args.dispatch_tool,
        "assigned_at": stamp,
    }
    stage.setdefault("helpers", []).append(record)
    data["updated_at"] = stamp
    data["events"].append({"at": stamp, "event": "research_helper_recorded", "stage": args.stage, **record})
    atomic_write(args.state, data)
    print(f"{args.stage} helper recorded: {args.role} ({args.executor_id})")
    return 0


def command_finish(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if args.stage not in data["route"]:
        raise SystemExit("stage is outside route")
    stage = data["stages"][args.stage]
    if stage["status"] != "in_progress":
        raise SystemExit("stage must be in_progress")
    stamp = now()
    if args.result == "overflow":
        if args.stage != "SOLO":
            raise SystemExit("overflow is only valid for SOLO")
        stage.update(status="skipped", completed_at=stamp)
        data["size_class"] = "medium"
        data["route"] = DEFAULTS["routes"]["medium"]
        if data.get("execution_policy_version") in {1, 2, 3}:
            adapter = data.get("host_adapter") or args.host_adapter
            if not adapter:
                raise SystemExit(
                    "SOLO overflow requires --host-adapter " + "|".join(ADAPTERS)
                )
            data["execution_mode"] = "isolated"
            data["host_adapter"] = adapter
            if data.get("execution_policy_version") >= 3:
                data["executor_topology"] = TOPOLOGIES.get(adapter)
                data["run_id"] = allocate_run_id(adapter)
                data["team_name"] = expected_team_name(data)
        for stage_name in data["route"]:
            reset = {
                "status": "pending", "prepared_at": None, "artifact_baseline": None,
                "started_at": None, "completed_at": None, "artifacts": [],
            }
            if stage_name in DEFAULTS.get("optional_stages", []):
                reset.update(decision=None, decision_reason=None)
            if STAGE_EXECUTION[stage_name] == "coordinator":
                reset.update(
                    executor_role=STAGE_ROLES[stage_name], executor_type="coordinator",
                    executor_id=data["coordinator_id"], executor_topology="coordinator",
                    team_name=None, dispatch_tool="coordinator",
                )
            else:
                reset.update(
                    executor_role=None, executor_type=None, executor_id=None,
                    executor_topology=None, team_name=None, dispatch_tool=None,
                )
            data["stages"][stage_name].update(reset)
        # SOLO is no longer part of the medium route; park current_stage on the
        # PHASE-0 sentinel so the escalated state stays valid for resume/prepare.
        data.update(status="in_progress", next_stage="REQUIREMENT", current_stage="PHASE-0")
    elif args.result == "completed":
        artifact_root = Path(data["artifacts_dir"])
        required = required_artifacts(
            args.stage, bool(data.get("requires_design_artifacts", False))
        )
        missing = [relative for relative in required if not (artifact_root / relative).is_file()]
        if missing:
            raise SystemExit("required artifacts missing: " + ", ".join(missing))
        content_errors = []
        for relative in required:
            content_errors.extend(validate_file(artifact_root / relative, relative))
        if args.stage == "DESIGN":
            plan_errors, _ = validate_execution_plan(
                artifact_root / "02-design/execution-plan.md", require_pending=True
            )
            content_errors.extend(plan_errors)
        baseline = stage.get("artifact_baseline")
        if isinstance(baseline, dict):
            unchanged = [
                relative for relative in required
                if relative in baseline and baseline[relative] == file_digest(artifact_root / relative)
            ]
        else:
            started = datetime.fromisoformat(stage["started_at"])
            unchanged = [
                relative for relative in required
                if datetime.fromtimestamp((artifact_root / relative).stat().st_mtime, timezone.utc) < started
            ]
        if unchanged:
            content_errors.append("artifacts unchanged since stage prepare: " + ", ".join(unchanged))
        if args.stage == "DESIGN" and data.get("requires_design_artifacts", False):
            design_root = artifact_root / "02-design/design-context"
            content_errors.extend(validate_design_context(design_root))
            if isinstance(baseline, dict) and baseline.get("__design_context__") == tree_digest(design_root):
                content_errors.append("design context unchanged since stage prepare")
        if content_errors:
            raise SystemExit("artifact content invalid: " + "; ".join(content_errors))
        stage.update(status="completed", completed_at=stamp, artifacts=required)
        following = next_after(data["route"], args.stage)
        data.update(next_stage=following, current_stage=args.stage)
        required_approval = args.stage in DEFAULTS.get("required_approval_gates", [])
        manual_approval = (
            data.get("run_mode") == "manual" and args.stage in DEFAULTS["manual_gates"]
        )
        if required_approval or manual_approval:
            data.update(status="awaiting_approval", awaiting_approval=args.stage)
        elif following is None:
            data["status"] = "completed"
            completion_errors = state_errors(data, check_files=True)
            if completion_errors:
                raise SystemExit("workflow completion gate failed: " + "; ".join(completion_errors))
    else:
        stage["retry_count"] += 1
        stage.update(
            status="failed", completed_at=stamp, prepared_at=None, artifact_baseline=None
        )
        if stage["retry_count"] >= DEFAULTS["max_stage_failures"]:
            data.update(status="blocked", next_stage=None)
        elif args.stage == "REVIEW":
            implementation = data["stages"]["IMPLEMENT"]
            implementation.update(
                status="pending",
                prepared_at=None,
                artifact_baseline=None,
                started_at=None,
                completed_at=None,
            )
            data.update(status="in_progress", next_stage="IMPLEMENT")
        else:
            data["next_stage"] = args.stage
    data["updated_at"] = stamp
    data["events"].append({"at": stamp, "event": f"stage_{args.result}", "stage": args.stage})
    atomic_write(args.state, data)
    if data.get("awaiting_approval"):
        print(
            f"{args.stage} {args.result}; awaiting_approval={data['awaiting_approval']}; "
            f"next={data.get('next_stage')}"
        )
    else:
        print(f"{args.stage} {args.result}; next={data.get('next_stage')}")
    return 0


def command_approve(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if data.get("awaiting_approval") != args.stage:
        raise SystemExit(f"not awaiting approval for {args.stage}")
    if args.stage in DEFAULTS.get("required_approval_gates", []) and not args.user_confirmed:
        raise SystemExit(
            f"{args.stage} requires explicit user confirmation; rerun with --user-confirmed "
            "only after the user approves the requirement report"
        )
    stamp = now()
    data["awaiting_approval"] = None
    data["status"] = "completed" if data.get("next_stage") is None else "in_progress"
    data["updated_at"] = stamp
    data["events"].append({
        "at": stamp,
        "event": "stage_approved",
        "stage": args.stage,
        "user_confirmed": bool(args.user_confirmed),
    })
    atomic_write(args.state, data)
    print(f"{args.stage} approved; next={data.get('next_stage')}")
    return 0


def command_validate(args: argparse.Namespace) -> int:
    errors = state_errors(load(args.state), check_files=True)
    if errors:
        print("ERROR:\n- " + "\n- ".join(errors))
        return 1
    print("OK: workflow state and completed-stage artifacts are valid")
    return 0


def command_status(args: argparse.Namespace) -> int:
    data = load(args.state)
    print(
        f"task={data['task_slug']} size={data['size_class']} status={data['status']} "
        f"next={data['next_stage']} execution={data.get('execution_mode', 'legacy')} "
        f"adapter={data.get('host_adapter', 'none')} "
        f"topology={data.get('executor_topology', 'legacy')} team={data.get('team_name') or 'none'}"
    )
    for name in ("PHASE-0", *data["route"]):
        stage = data["stages"][name]
        executor = stage.get("executor_id") or "unassigned"
        role = stage.get("executor_role") or STAGE_ROLES.get(name, "coordinator")
        dispatch = stage.get("dispatch_tool") or "none"
        executor_type = stage.get("executor_type") or "unknown"
        topology = stage.get("executor_topology") or "unknown"
        print(
            f"{name}: {stage['status']} failures={stage['retry_count']} "
            f"executor={role}:{executor_type}:{executor} topology={topology} dispatch={dispatch}"
        )
    return 0


def can_upgrade_to_team(data: dict) -> bool:
    if (
        data.get("execution_policy_version") != 2
        or data.get("execution_mode") != "isolated"
        or TOPOLOGIES.get(data.get("host_adapter")) != "team"
    ):
        return False
    return not any(
        STAGE_EXECUTION[name] != "coordinator" and data["stages"][name].get("executor_id")
        for name in data.get("route", [])
    ) and not any(
        STAGE_EXECUTION[name] != "coordinator"
        and data["stages"][name].get("status") in {"in_progress", "completed"}
        for name in data.get("route", [])
    )


def can_rotate_team(data: dict) -> bool:
    if (
        data.get("execution_policy_version") not in {3}
        or data.get("execution_mode") != "isolated"
        or TOPOLOGIES.get(data.get("host_adapter")) != "team"
        or data.get("status") == "completed"
    ):
        return False
    return True


def command_rotate_team(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if not can_rotate_team(data):
        raise SystemExit("workflow does not use a rotatable Team")
    old_team = data.get("team_name")
    stamp = now()
    data["run_id"] = allocate_run_id(data.get("host_adapter"))
    data["team_name"] = expected_team_name(data)
    for name in data["route"]:
        stage = data["stages"][name]
        if STAGE_EXECUTION[name] == "coordinator":
            continue
        if stage.get("status") in {"pending", "failed", "in_progress"}:
            if stage.get("status") == "in_progress":
                stage.update(status="pending", started_at=None, completed_at=None)
            stage.update(
                executor_role=None,
                executor_type=None,
                executor_id=None,
                executor_topology=None,
                team_name=None,
                dispatch_tool=None,
                helpers=[],
            )
        elif stage.get("status") == "completed":
            stage["team_name"] = data["team_name"]
        for helper in stage.get("helpers", []):
            if isinstance(helper, dict):
                helper["team_name"] = data["team_name"]
    data["updated_at"] = stamp
    data["events"].append({
        "at": stamp,
        "event": "workflow_team_rotated",
        "old_team_name": old_team,
        "team_name": data["team_name"],
        "run_id": data["run_id"],
    })
    atomic_write(args.state, data)
    print(json.dumps({
        "ok": True,
        "old_team_name": old_team,
        "run_id": data["run_id"],
        "team_name": data["team_name"],
        "message": "Create this new Team immediately; ignore the old Team and do not inspect it",
    }, ensure_ascii=False, indent=2))
    return 0


def command_upgrade_team(args: argparse.Namespace) -> int:
    data = load(args.state)
    errors = state_errors(data)
    if errors:
        raise SystemExit("invalid state: " + "; ".join(errors))
    if not can_upgrade_to_team(data):
        raise SystemExit(
            "only an unassigned policy-v2 Team workflow can be upgraded; "
            "do not replace an active or completed executor"
        )
    stamp = now()
    data["execution_policy_version"] = 3
    data["executor_topology"] = "team"
    data["run_id"] = allocate_run_id(data.get("host_adapter"))
    data["team_name"] = expected_team_name(data)
    for name in data["route"]:
        stage = data["stages"][name]
        if STAGE_EXECUTION[name] == "coordinator":
            stage.update(executor_topology="coordinator", team_name=None)
        else:
            stage.update(executor_topology=None, team_name=None)
    data["updated_at"] = stamp
    data["events"].append({
        "at": stamp,
        "event": "execution_policy_upgraded",
        "from": 2,
        "to": 3,
        "executor_topology": "team",
        "team_name": data["team_name"],
    })
    atomic_write(args.state, data)
    print(json.dumps({
        "ok": True,
        "execution_policy_version": 3,
        "executor_topology": "team",
        "team_name": data["team_name"],
    }, ensure_ascii=False, indent=2))
    return 0


def resume_state_path(args: argparse.Namespace) -> Path:
    if args.state is not None:
        return args.state.resolve()
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,79}", args.slug or "") or ".." in args.slug:
        raise SystemExit("unsafe task slug")
    return args.project_root.resolve() / "artifacts" / args.slug / "workflow-state.json"


def command_resume(args: argparse.Namespace) -> int:
    state_path = resume_state_path(args)
    if not state_path.is_file():
        action = {
            "ok": False,
            "action": "restart-required",
            "state": str(state_path),
            "message": "状态文件不存在，不能 resume；删除同名旧 Team 后重新提交原始需求",
        }
        if args.fresh_team and args.slug:
            action["delete_team_name"] = (
                (TEAM_NAME_PREFIXES.get("codebuddy") or "") + args.slug
            )
        print(json.dumps(action, ensure_ascii=False, indent=2))
        return 0
    data = load(state_path)
    errors = state_errors(data)
    if errors:
        print(json.dumps({"ok": False, "errors": errors}, ensure_ascii=False, indent=2))
        return 1
    action = {"ok": True, "task": data["task_slug"], "status": data["status"]}
    if data["status"] == "completed":
        action.update(action="completed", message="流程已经完成，无需恢复")
    elif can_upgrade_to_team(data):
        action.update(
            action="upgrade-team",
            executor_topology="team",
            command=f"workflow_state.py upgrade-team --state {state_path}",
            message=(
                "旧 v2 流程尚未登记阶段执行者；先原地升级 Team 策略，"
                "由升级命令分配唯一 Team 名，再创建 Team member"
            ),
        )
    elif args.fresh_team and can_rotate_team(data):
        action.update(
            action="rotate-team",
            delete_team_name=data.get("team_name"),
            command=f"workflow_state.py rotate-team --state {state_path}",
            message="直接删除旧 Team，再分配并创建新 Team；不要检查或唤醒旧成员",
        )
    elif data.get("awaiting_approval"):
        required_user_confirmation = data["awaiting_approval"] in DEFAULTS.get(
            "required_approval_gates", []
        )
        action.update(
            action="approve",
            stage=data["awaiting_approval"],
            requires_user_confirmation=required_user_confirmation,
            artifact=(
                str(Path(data["artifacts_dir"]) / "01-requirement/requirement-report.md")
                if data["awaiting_approval"] == "REQUIREMENT" else None
            ),
            command=(
                f"workflow_state.py approve --state {state_path} "
                f"--stage {data['awaiting_approval']}"
                + (" --user-confirmed" if required_user_confirmation else "")
            ),
            message=(
                "先向用户展示需求报告的目标、范围、排除项、关键决策、验收标准和未决问题；"
                "只有用户明确确认后才能执行 approve"
                if required_user_confirmation else "等待用户批准当前手动门禁"
            ),
        )
    else:
        in_progress = next(
            (name for name in data["route"] if data["stages"][name]["status"] == "in_progress"),
            None,
        )
        if in_progress:
            stage = data["stages"][in_progress]
            action.update(
                action="continue-executor",
                stage=in_progress,
                executor_role=stage.get("executor_role"),
                executor_type=stage.get("executor_type"),
                executor_id=stage.get("executor_id"),
                executor_topology=stage.get("executor_topology"),
                team_name=stage.get("team_name"),
                message="继续现有执行者；校验产物后调用 finish，不要重新 init 或创建新 slug",
            )
        else:
            next_stage = data.get("next_stage")
            if next_stage is None:
                action.update(action="blocked", message="没有 next_stage，请检查失败事件和状态")
            else:
                stage = data["stages"][next_stage]
                if (
                    next_stage == "KNOWLEDGE"
                    and stage.get("decision") not in {"run", "skip"}
                ):
                    action.update(
                        action="assess-knowledge",
                        stage="KNOWLEDGE",
                        run_when=[
                            "new reusable module or interface constraint",
                            "surprising failure mode or diagnostic method",
                            "compatibility, data, security, or operational pitfall",
                            "reusable verification, recovery, or rollback technique",
                        ],
                        skip_when=[
                            "only a change list or routine test result",
                            "one-off process detail with no reuse value",
                            "the same knowledge is already documented",
                        ],
                        run_command=(
                            f"workflow_state.py decide-knowledge --state {state_path} "
                            "--decision run --reason <candidate-and-evidence>"
                        ),
                        skip_command=(
                            f"workflow_state.py decide-knowledge --state {state_path} "
                            "--decision skip --reason <why-no-reusable-knowledge>"
                        ),
                        message="先基于最终 diff、审查和测试的精简证据判断是否有新增可复用知识；不要按任务规模决定",
                    )
                    print(json.dumps(action, ensure_ascii=False, indent=2))
                    return 0
                common = {
                    "stage": next_stage,
                    "executor_role": STAGE_ROLES[next_stage],
                    "executor_type": (
                        "coordinator" if STAGE_EXECUTION[next_stage] == "coordinator"
                        else expected_executor_type(data)
                    ),
                    "executor_topology": (
                        "coordinator" if STAGE_EXECUTION[next_stage] == "coordinator"
                        else TOPOLOGIES.get(data.get("host_adapter"))
                    ),
                    "team_name": (
                        data.get("team_name") if STAGE_EXECUTION[next_stage] != "coordinator"
                        else None
                    ),
                }
                if not stage.get("prepared_at"):
                    action.update(action="prepare", **common)
                elif STAGE_EXECUTION[next_stage] == "coordinator":
                    action.update(action="start-coordinator", **common)
                elif not stage.get("executor_id"):
                    action.update(
                        action="spawn-and-assign",
                        dispatch_tool=DISPATCH_TOOLS.get(data.get("host_adapter")),
                        **common,
                    )
                else:
                    action.update(
                        action="start-assigned",
                        executor_id=stage.get("executor_id"),
                        dispatch_tool=stage.get("dispatch_tool"),
                        **common,
                    )
    print(json.dumps(action, ensure_ascii=False, indent=2))
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Manage the portable DevFlow state machine")
    sub = parser.add_subparsers(dest="command", required=True)
    item = sub.add_parser("init")
    item.add_argument("--project-root", type=Path, required=True)
    item.add_argument("--slug", required=True)
    item.add_argument("--size", choices=("small", "medium", "large"), required=True)
    item.add_argument("--mode", choices=("auto", "manual"), default="auto")
    item.add_argument("--require-design-artifacts", action="store_true")
    item.add_argument("--execution-mode", choices=("isolated", "single-context"))
    item.add_argument("--host-adapter", choices=tuple(ADAPTERS))
    item.add_argument("--coordinator-id", default="main")
    item.add_argument("--fallback-reason")
    item.set_defaults(func=command_init)
    item = sub.add_parser("assign")
    item.add_argument("--state", type=Path, required=True)
    item.add_argument("--stage", required=True)
    item.add_argument("--role", required=True)
    item.add_argument("--executor-type")
    item.add_argument("--executor-id", required=True)
    item.add_argument("--team-name")
    item.add_argument("--dispatch-tool", choices=tuple(sorted(set(DISPATCH_TOOLS.values()))), required=True)
    item.set_defaults(func=command_assign)
    item = sub.add_parser("helper")
    item.add_argument("--state", type=Path, required=True)
    item.add_argument("--stage", required=True)
    item.add_argument("--role", required=True)
    item.add_argument("--executor-type")
    item.add_argument("--executor-id", required=True)
    item.add_argument("--purpose", required=True)
    item.add_argument("--team-name")
    item.add_argument("--dispatch-tool", choices=tuple(sorted(set(DISPATCH_TOOLS.values()))), required=True)
    item.set_defaults(func=command_helper)
    item = sub.add_parser("start")
    item.add_argument("--state", type=Path, required=True)
    item.add_argument("--stage", required=True)
    item.set_defaults(func=command_start)
    item = sub.add_parser("prepare")
    item.add_argument("--state", type=Path, required=True)
    item.add_argument("--stage", required=True)
    item.add_argument(
        "--emit-prompt", action="store_true",
        help="准备阶段并直接输出完整阶段提示，避免单独调用 build_stage_prompt.py",
    )
    item.add_argument("--request", default="")
    item.add_argument("--input", type=Path, action="append", default=[])
    item.add_argument("--profile", action="append", default=[])
    item.set_defaults(func=command_prepare)
    item = sub.add_parser("finish")
    item.add_argument("--state", type=Path, required=True)
    item.add_argument("--stage", required=True)
    item.add_argument("--result", choices=("completed", "failed", "overflow"), required=True)
    item.add_argument("--host-adapter", choices=tuple(ADAPTERS))
    item.set_defaults(func=command_finish)
    item = sub.add_parser("decide-knowledge")
    item.add_argument("--state", type=Path, required=True)
    item.add_argument("--decision", choices=("run", "skip"), required=True)
    item.add_argument("--reason", required=True)
    item.set_defaults(func=command_decide_knowledge)
    item = sub.add_parser("approve")
    item.add_argument("--state", type=Path, required=True)
    item.add_argument("--stage", required=True)
    item.add_argument("--user-confirmed", action="store_true")
    item.set_defaults(func=command_approve)
    for name, func in (
        ("validate", command_validate), ("status", command_status),
        ("upgrade-team", command_upgrade_team), ("rotate-team", command_rotate_team),
    ):
        item = sub.add_parser(name)
        item.add_argument("--state", type=Path, required=True)
        item.set_defaults(func=func)
    item = sub.add_parser("resume")
    source = item.add_mutually_exclusive_group(required=True)
    source.add_argument("--state", type=Path)
    source.add_argument("--slug")
    item.add_argument("--project-root", type=Path, default=Path("."))
    item.add_argument("--fresh-team", action="store_true")
    item.set_defaults(func=command_resume)
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
