#!/usr/bin/env python3
"""Deterministic stand-in for a host coding Agent (Claude Code, Codex, ...).

A real host loads the installed ``devflow`` Skill and follows its contract:
``init`` once, then repeatedly ask ``workflow_state.py resume`` for the next
action and carry it out (prepare a stage prompt, spawn and register a stage
executor, start, let the executor write artifacts, finish, ask the user to
approve the requirement, ...).

This script follows exactly that protocol against the *installed* copy of the
Skill in a project, but replaces the LLM with recorded stage outputs from a
scenario fixture. No network access, API key or real Agent CLI is needed, so
the run is deterministic in CI.

Exit codes: 0 completed, 3 waiting for the user, 4 blocked, 75 interrupted,
2 contract violation (a command the real Agent would run was rejected).
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import uuid
from pathlib import Path

HOST_SKILL_DIRS = {
    "codebuddy": ".codebuddy/skills",
    "codex": ".agents/skills",
    "cursor": ".cursor/skills",
    "claude": ".claude/skills",
    "pi": ".pi/skills",
}
FIXTURES = Path(__file__).resolve().parent / "fixtures"
EXIT_COMPLETED, EXIT_CONTRACT, EXIT_WAITING, EXIT_BLOCKED, EXIT_INTERRUPTED = 0, 2, 3, 4, 75


class ContractError(Exception):
    pass


class FakeAgent:
    def __init__(self, args):
        self.args = args
        self.host = args.host
        self.root = args.project_root.resolve()
        self.scenario = json.loads(args.scenario.read_text(encoding="utf-8"))
        self.session = uuid.uuid4().hex[:8]
        self.skill_root = self.root / HOST_SKILL_DIRS[self.host] / "devflow"
        self.workflow = self.skill_root / "scripts" / "workflow_state.py"
        self.transcript = self.root / ".fake-agent" / "transcript.jsonl"
        self.transcript.parent.mkdir(parents=True, exist_ok=True)
        self.actions = 0
        self.review_rejections_left = args.review_reject
        self.failures_left = dict(args.fail_stage)
        self.fresh_team = bool(args.resume and args.host == "codebuddy")
        self.rounds = {}

    # -- helpers ---------------------------------------------------------
    def log(self, kind, **fields):
        record = {"session": self.session, "kind": kind}
        record.update(fields)
        with self.transcript.open("a", encoding="utf-8") as handle:
            handle.write(json.dumps(record, ensure_ascii=False) + "\n")

    def run(self, *argv, check=True):
        command = [sys.executable, str(self.workflow)] + [str(item) for item in argv]
        result = subprocess.run(
            command, cwd=str(self.root), text=True, capture_output=True,
            env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"),
        )
        self.log("command", argv=[str(item) for item in argv[:3]], rc=result.returncode)
        if check and result.returncode != 0:
            raise ContractError(
                "workflow_state.py {} rejected (rc={}): {}".format(
                    argv[0], result.returncode, (result.stderr or result.stdout).strip()
                )
            )
        return result

    def state_path(self):
        return self.root / "artifacts" / self.args.slug / "workflow-state.json"

    def state(self):
        return json.loads(self.state_path().read_text(encoding="utf-8"))

    # -- the host side of the Skill contract ----------------------------
    def load_skill(self):
        skill = self.skill_root / "SKILL.md"
        if not skill.is_file():
            raise ContractError("host cannot find installed Skill: {}".format(skill))
        text = skill.read_text(encoding="utf-8")
        for needle in ("workflow_state.py", "prepare", "approve"):
            if needle not in text:
                raise ContractError("SKILL.md no longer documents `{}`".format(needle))
        # The Agent follows the links in SKILL.md; every one must ship with the install.
        for target in re.findall(r"\]\(([^)#]+)\)", text):
            if "://" not in target and not (self.skill_root / target).exists():
                raise ContractError("SKILL.md links to missing file: " + target)
        contract = (self.skill_root / "references" / "workflow-contract.md").read_text(encoding="utf-8")
        if "resume --project-root . --slug" not in contract:
            raise ContractError("workflow-contract.md no longer documents `resume --slug`")
        self.log("skill-loaded", path=str(skill.relative_to(self.root)))

    def init(self):
        output = self.run(
            "init", "--project-root", self.root, "--slug", self.args.slug,
            "--size", "medium", "--mode", "auto", "--execution-mode", "isolated",
            "--host-adapter", self.host, "--coordinator-id", "fake-main",
        ).stdout.strip()
        self.log("init", state=output)

    def next_action(self):
        argv = ["resume", "--project-root", self.root, "--slug", self.args.slug]
        if self.fresh_team:
            # Per workflow-contract.md a new CodeBuddy main session resumes with a fresh Team.
            argv.append("--fresh-team")
            self.fresh_team = False
        return json.loads(self.run(*argv).stdout)

    def loop(self):
        while True:
            action = self.next_action()
            kind = action.get("action")
            self.log("action", action=kind, stage=action.get("stage"))
            if kind == "completed":
                return EXIT_COMPLETED
            if kind == "blocked":
                return EXIT_BLOCKED
            if kind == "approve":
                if action.get("requires_user_confirmation") and not self.args.user_confirms:
                    self.log("waiting-for-user", stage=action["stage"])
                    return EXIT_WAITING
                extra = ["--user-confirmed"] if action.get("requires_user_confirmation") else []
                self.run("approve", "--state", self.state_path(), "--stage", action["stage"], *extra)
            elif kind == "rotate-team":
                rotated = json.loads(self.run("rotate-team", "--state", self.state_path()).stdout)
                self.log("team-rotated", old=rotated["old_team_name"], new=rotated["team_name"])
            elif kind == "assess-knowledge":
                decision = self.scenario["knowledge_decision"]
                self.run(
                    "decide-knowledge", "--state", self.state_path(),
                    "--decision", decision["decision"], "--reason", decision["reason"],
                )
            elif kind == "prepare":
                prompt = self.run(
                    "prepare", "--state", self.state_path(), "--stage", action["stage"],
                    "--emit-prompt", "--request", self.scenario["request"],
                ).stdout
                if not prompt.strip():
                    raise ContractError("empty stage prompt for " + action["stage"])
                self.log("prompt", stage=action["stage"], role=action.get("executor_role"),
                         chars=len(prompt), mentions_role=action.get("executor_role", "") in prompt)
            elif kind == "spawn-and-assign":
                executor_id = "fake-{}-{}".format(action["stage"].lower(), uuid.uuid4().hex[:6])
                argv = [
                    "assign", "--state", self.state_path(), "--stage", action["stage"],
                    "--role", action["executor_role"], "--executor-type", action["executor_type"],
                    "--executor-id", executor_id, "--dispatch-tool", action["dispatch_tool"],
                ]
                if action.get("team_name"):
                    argv += ["--team-name", action["team_name"]]
                self.run(*argv)
                self.log("spawned", stage=action["stage"], executor_id=executor_id,
                         executor_type=action["executor_type"],
                         dispatch_tool=action["dispatch_tool"])
            elif kind in ("start-assigned", "start-coordinator"):
                self.run("start", "--state", self.state_path(), "--stage", action["stage"])
                if self.args.crash_in == action["stage"]:
                    self.log("crash", stage=action["stage"])
                    return EXIT_INTERRUPTED
                self.execute_stage(action["stage"])
            elif kind == "continue-executor":
                self.execute_stage(action["stage"])
            else:
                raise ContractError("unexpected resume action: {}".format(action))
            self.actions += 1
            if self.args.stop_after and self.actions >= self.args.stop_after:
                self.log("interrupted", after=self.actions)
                return EXIT_INTERRUPTED

    # -- the fake stage executor ----------------------------------------
    def execute_stage(self, stage):
        result = "completed"
        evidence = self.scenario["stages"][stage]
        overrides = dict(self.scenario["overrides"])
        if stage == "REVIEW" and self.review_rejections_left > 0:
            self.review_rejections_left -= 1
            result = "failed"
            evidence = self.scenario["stages"]["REVIEW_REJECTED"]
            for key in list(overrides):
                if "通过" in key and "不通过" in key:
                    overrides[key] = evidence
        if self.failures_left.get(stage, 0) > 0:
            self.failures_left[stage] -= 1
            result = "failed"
        for relative, source in self.scenario.get("code_changes", {}).get(stage, {}).items():
            shutil.copyfile(str(FIXTURES / source), str(self.root / relative))
        if stage == "TEST":
            verify = subprocess.run(
                [sys.executable] + self.scenario["verify_command"], cwd=str(self.root),
                text=True, capture_output=True,
            )
            self.log("verify", rc=verify.returncode, tail=verify.stderr.strip().splitlines()[-1:])
            if verify.returncode != 0:
                result = "failed"
                evidence = "测试失败：" + verify.stderr.strip()[-200:]
        self.rounds[stage] = self.rounds.get(stage, 0) + 1
        if stage not in self.args.leave_placeholders:
            note = "（session {}，第 {} 轮）".format(self.session, self.rounds[stage])
            self.fill_artifacts(stage, overrides, evidence + note)
        finished = self.run(
            "finish", "--state", self.state_path(), "--stage", stage, "--result", result,
            check=False,
        )
        self.log("finish", stage=stage, result=result, rc=finished.returncode,
                 error=finished.stderr.strip()[-300:] if finished.returncode else None)
        if finished.returncode != 0:
            raise ContractError("finish {} rejected: {}".format(stage, finished.stderr.strip()))

    def fill_artifacts(self, stage, overrides, evidence):
        """Write every required artifact afresh from its template, like a real executor."""
        artifact_root = self.state_path().parent
        templates = self.skill_root / "templates"
        manifest = json.loads((templates / "manifest.json").read_text(encoding="utf-8"))
        sources = {item["path"]: item["template"] for item in manifest["artifacts"]}
        for relative in self.required_artifacts(stage):
            text = (templates / sources[relative]).read_text(encoding="utf-8")
            for source, target in overrides.items():
                text = text.replace(source, target)
            text = re.sub(r"\{\{[^{}]+\}\}", evidence, text)
            path = artifact_root / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text, encoding="utf-8")

    def required_artifacts(self, stage):
        sys.path.insert(0, str(self.skill_root / "scripts"))
        try:
            from template_registry import required_artifacts  # type: ignore
        finally:
            sys.path.pop(0)
        return required_artifacts(stage, bool(self.state().get("requires_design_artifacts")))

    def main(self):
        try:
            self.load_skill()
            if not self.args.resume:
                self.init()
            code = self.loop()
        except ContractError as error:
            self.log("contract-error", message=str(error))
            print("fake-agent: " + str(error), file=sys.stderr)
            return EXIT_CONTRACT
        self.log("exit", code=code)
        return code


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--host", choices=sorted(HOST_SKILL_DIRS), required=True)
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--slug", default="e2e-task")
    parser.add_argument("--scenario", type=Path, default=FIXTURES / "scenario-medium.json")
    parser.add_argument("--resume", action="store_true", help="new session; do not init")
    parser.add_argument("--no-user-confirmation", dest="user_confirms", action="store_false")
    parser.add_argument("--stop-after", type=int, default=0, help="interrupt after N actions")
    parser.add_argument("--crash-in", help="die right after starting this stage")
    parser.add_argument("--review-reject", type=int, default=0, help="reject REVIEW N times")
    parser.add_argument("--leave-placeholders", action="append", default=[],
                        help="executor of this stage leaves the template unfilled")
    parser.add_argument("--fail-stage", action="append", default=[],
                        type=lambda item: (item.split(":")[0], int(item.split(":")[1])),
                        help="STAGE:N report failure N times")
    return parser.parse_args(argv)


if __name__ == "__main__":
    sys.exit(FakeAgent(parse_args()).main())
