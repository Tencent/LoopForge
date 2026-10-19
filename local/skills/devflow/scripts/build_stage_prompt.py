#!/usr/bin/env python3
import argparse
import json
import shlex
from pathlib import Path
from typing import List

from agent_registry import ROLES, STAGE_ROLES, role_body
from rule_registry import applicable_rules, stage_rule
from template_registry import ARTIFACTS, required_artifacts
from validate_artifacts import find_section, sections


SKILL_ROOT = Path(__file__).resolve().parent.parent
RULES_PATH = SKILL_ROOT / "rules/core.md"
HANDOFF_HEADINGS = {
    "DESIGN": {
        "01-requirement/requirement-report.md": [
            "范围", "排除项", "决策与理由", "验收标准", "假设与证据", "未决问题",
        ],
    },
    "IMPLEMENT": {
        "02-design/tech-design.md": ["方案", "影响范围", "风险与对策", "回滚方案"],
        "02-design/execution-plan.md": [
            "目标与完成定义", "上下文与范围", "执行任务", "验证与验收", "风险、恢复与回滚",
        ],
    },
    "REVIEW": {
        "01-requirement/requirement-report.md": ["范围", "排除项", "验收标准"],
        "02-design/execution-plan.md": ["目标与完成定义", "执行任务", "验证与验收"],
        "03-code/change-report.md": ["变更摘要", "修改文件", "验证结果", "计划偏差", "API 接口文档", "剩余风险"],
    },
    "TEST": {
        "01-requirement/requirement-report.md": ["验收标准"],
        "02-design/execution-plan.md": ["验证与验收"],
        "03-code/review-report.md": ["审查结论", "问题清单", "证据"],
    },
    "KNOWLEDGE": {
        "02-design/tech-design.md": ["方案", "风险与对策"],
        "03-code/review-report.md": ["问题清单"],
        "04-test/test-report.md": ["项目测试能力", "风险与覆盖矩阵", "测试结果", "未运行项与剩余风险"],
    },
    "SUMMARY": {
        "01-solo/solo-report.md": ["变更", "测试", "剩余风险"],
        "03-code/change-report.md": ["变更摘要", "修改文件", "API 接口文档", "剩余风险"],
        "03-code/review-report.md": ["审查结论", "问题清单"],
        "04-test/test-report.md": ["风险与覆盖矩阵", "测试结果", "未运行项与剩余风险"],
        "05-knowledge/knowledge-report.md": ["事实", "决策"],
    },
}


def upstream_artifacts(state: dict, role: dict) -> List[str]:
    requested_artifacts = role.get("input_artifacts")
    if requested_artifacts is not None:
        available = set()
        for stage_state in state.get("stages", {}).values():
            if isinstance(stage_state, dict):
                available.update(stage_state.get("artifacts", []))
        return [relative for relative in requested_artifacts if relative in available]
    requested = role.get("input_stages", [])
    if requested == ["*"]:
        requested = [name for name in state.get("route", []) if name != role.get("stage")]
    result = []
    for stage_name in requested:
        stage = state.get("stages", {}).get(stage_name, {})
        for relative in stage.get("artifacts", []):
            if relative not in result:
                result.append(relative)
    return result


def compact_handoff(stage: str, artifact_root: Path, upstream: List[str]) -> List[str]:
    blocks = []
    selection = HANDOFF_HEADINGS.get(stage, {})
    for relative in upstream:
        path = artifact_root / relative
        headings = selection.get(relative, [])
        if not headings or not path.is_file():
            continue
        items = sections(path.read_text(encoding="utf-8"))
        selected = []
        for heading in headings:
            found = find_section(items, [heading])
            if found is not None and found[1].strip():
                selected.append(f"### {found[0]}\n\n{found[1].strip()}")
        if selected:
            blocks.append(f"## 来源：`{relative}`\n\n" + "\n\n".join(selected))
    return blocks


def build_prompt(
    state: dict,
    stage: str,
    request: str,
    extra_inputs: List[Path],
    explicit_profiles: List[str] = None,
) -> str:
    role_id = STAGE_ROLES.get(stage)
    if role_id is None:
        raise ValueError(f"阶段没有已注册角色: {stage}")
    role = ROLES[role_id]
    stage_state = state.get("stages", {}).get(stage, {})
    if not stage_state.get("prepared_at"):
        raise ValueError(f"先运行 workflow_state.py prepare --stage {stage}")
    artifact_root = Path(state["artifacts_dir"])
    include_design = bool(state.get("requires_design_artifacts", False))
    outputs = required_artifacts(stage, include_design)
    upstream = upstream_artifacts(state, role)
    upstream_paths = [artifact_root / relative for relative in upstream]
    handoff = compact_handoff(stage, artifact_root, upstream)
    project_root = Path(state["project_root"])
    project_instructions = [
        project_root / name for name in ("AGENTS.md", "CLAUDE.md", ".cursorrules")
        if (project_root / name).is_file()
    ]
    lines = [
        f"# DevFlow 阶段任务：{stage}",
        "",
        f"- 角色：`{role_id}`",
        f"- 执行主体：`{role.get('execution', 'agent')}`",
        f"- 写入级别：`{role['access']}`",
        f"- 项目根目录：`{state['project_root']}`",
        f"- 状态文件：`{artifact_root / 'workflow-state.json'}`",
        f"- 产物根目录：`{artifact_root}`",
    ]
    # Downstream stages use the approved upstream artifacts as their compact handoff.
    # The raw request still participates in profile selection, but repeating it in
    # every prompt wastes context and can conflict with later confirmed decisions.
    if request.strip() and not upstream:
        lines.extend(["", "## 用户目标", "", request.strip()])
    lines.extend(["", "## 强制规则", "", RULES_PATH.read_text(encoding="utf-8").strip()])
    lines.extend(["", "## 当前阶段规则", "", stage_rule(stage)])
    profile_rules = applicable_rules(
        state, stage, request, upstream_paths, explicit_profiles or []
    )
    if profile_rules:
        lines.extend(["", "## 自动选择的专项规则", ""])
        for profile_id, title, body, reasons in profile_rules:
            lines.extend([
                f"### {title}", "",
                f"- 规则 ID：`{profile_id}`",
                f"- 选择依据：{'; '.join(reasons)}", "", body, "",
            ])
    lines.extend(["", "## 角色边界", "", role_body(role_id)])
    if handoff:
        lines.extend([
            "", "## 精简上游交接", "",
            "以下内容由脚本从已验证产物的必要章节确定性提取。默认直接使用，不要重新读取整份上游报告；只有发现缺失、矛盾或需要核对引用时才打开源文件。", "",
            *handoff,
        ])
    lines.extend(["", "## 允许输入", ""])
    if upstream:
        lines.extend(
            f"- `{artifact_root / relative}`（仅在精简交接不足时读取）"
            for relative in upstream
        )
    lines.extend(f"- `{path}`（项目指令）" for path in project_instructions)
    lines.extend(f"- {item}" for item in role.get("workspace_evidence", []))
    for path in extra_inputs:
        lines.append(f"- `{path}`")
    if not upstream and not role.get("workspace_evidence") and not extra_inputs:
        lines.append("- 用户目标、项目规则，以及为回答当前阶段问题所必需的最小项目证据")
    lines.extend(["", "## 必需输出", ""])
    for relative in outputs:
        spec = ARTIFACTS[relative]
        lines.append(
            f"- `{artifact_root / relative}`（模板：`{SKILL_ROOT / 'templates' / spec['template']}`）"
        )
    lines.extend([
        "",
        "## 执行要求",
        "",
        "1. 先使用精简交接；仅对仍缺证据的点做定向调研，再作判断或写产物。",
        "2. 直接填写 `prepare` 已生成的模板，删除所有 `{{...}}` 占位项，不重建排版。",
        "3. 在产物中保留关键证据、未知项和实际验证结果；不得修改 workflow state。",
        "4. `report` 角色只能写上述必需输出，`read` 角色不得写文件，`write` 角色可以修改计划内业务文件；`test-write` 只能修改测试、测试夹具和上述测试报告，不得修改生产代码。",
        "5. 不得改用 YAML、JSON 或自创结构替代模板中的 Markdown 任务块。",
        "6. 完成后先运行下面的确定性自检；失败时由当前执行者修改产物并重试，不把格式修复交给协调者。",
    ])
    validation_command = [
        "python3",
        str(SKILL_ROOT / "scripts" / "validate_artifacts.py"),
        str(artifact_root),
    ]
    for relative in outputs:
        validation_command.extend(["--path", relative])
    lines.extend([
        "", "## 完成前自检", "", "```bash",
        " ".join(shlex.quote(item) for item in validation_command),
    ])
    if stage == "DESIGN":
        lines.append(
            " ".join(shlex.quote(item) for item in [
                "python3", str(SKILL_ROOT / "scripts" / "validate_plan.py"),
                str(artifact_root / "02-design/execution-plan.md"), "--require-pending",
            ])
        )
        if include_design:
            lines.append(
                " ".join(shlex.quote(item) for item in [
                    "python3", str(SKILL_ROOT / "scripts" / "validate_design_artifacts.py"),
                    str(artifact_root / "02-design/design-context"),
                ])
            )
    lines.extend([
        "```", "", "## 上下文与工具预算", "",
        "- 同一目的的文件发现、搜索和状态检查合并执行；已有精简交接可回答时不重复打开上游全文。",
        "- 工具输出只保留命中文件、关键结果和失败证据，不回传完整日志、完整 diff 或探索过程。",
        "- 证据足以完成当前职责后停止扩展搜索；REVIEW/TEST 的独立验证不受此限制。",
        "", "自检通过后，最多用 8 个要点向协调者返回产物路径、结论、证据和阻塞项。",
    ])
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="生成确定性的 DevFlow 阶段 Agent 提示")
    parser.add_argument("--state", type=Path, required=True)
    parser.add_argument("--stage", required=True)
    parser.add_argument("--request", default="")
    parser.add_argument("--input", type=Path, action="append", default=[])
    parser.add_argument(
        "--profile", action="append", default=[],
        help="显式追加专项规则；可重复，例如 backend、sql",
    )
    args = parser.parse_args()
    state = json.loads(args.state.read_text(encoding="utf-8"))
    if args.stage not in state.get("route", []):
        raise SystemExit("阶段不在当前 route 中")
    print(build_prompt(state, args.stage, args.request, args.input, args.profile), end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
