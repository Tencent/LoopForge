"""探测本机的宿主 Agent CLI，并把终端交给它执行工作流。"""

import os
import shutil
import subprocess
import sys
from typing import Dict, List, NamedTuple, Optional, Tuple

from .core import DevFlowError, status_rows
from .editions import HOST_PRIORITY, entrypoint_for, launch_args


HOST_CLI_CANDIDATES: Dict[str, Tuple[str, ...]] = {
    "codebuddy": ("codebuddy",),
    "codex": ("codex",),
    "cursor": ("cursor-agent", "cursor"),
    "claude": ("claude",),
}

CLI_OVERRIDE_PREFIX = "LOOPFORGE_CLI_"


class Candidate(NamedTuple):
    host: str
    edition: str
    cli: str
    entrypoint: str


def override_variable(host: str) -> str:
    return f"{CLI_OVERRIDE_PREFIX}{host.upper()}"


def tried_names(host: str) -> Tuple[str, ...]:
    override = os.environ.get(override_variable(host), "").strip()
    if override:
        return (override,)
    return HOST_CLI_CANDIDATES[host]


def resolve_cli(host: str) -> Optional[str]:
    for name in tried_names(host):
        found = shutil.which(name)
        if found:
            return found
    return None


def detected_hosts() -> Dict[str, str]:
    """本机 PATH 上可用的宿主 CLI，按 HOST_PRIORITY 排序。"""
    found = {}
    for host in HOST_PRIORITY:
        cli = resolve_cli(host)
        if cli:
            found[host] = cli
    return found


def candidate_for(host: str, edition: str) -> Optional[Candidate]:
    cli = resolve_cli(host)
    if not cli:
        return None
    return Candidate(
        host=host, edition=edition, cli=cli, entrypoint=entrypoint_for(edition, host),
    )


def candidates(project) -> List[Candidate]:
    """项目已安装、且本机能找到终端 CLI 的宿主。"""
    result = []
    for row in status_rows(project):
        candidate = candidate_for(row["host"], row["edition"])
        if candidate is not None:
            result.append(candidate)
    result.sort(key=lambda item: HOST_PRIORITY.index(item.host))
    return result


def build_prompt(candidate: Candidate, requirement: str = "") -> str:
    parts = [candidate.entrypoint, *launch_args(candidate.edition, candidate.host)]
    requirement = requirement.strip()
    if requirement:
        parts.append(requirement)
    return " ".join(parts)


def build_argv(candidate: Candidate, requirement: str = "") -> List[str]:
    return [candidate.cli, build_prompt(candidate, requirement)]


def launch(argv: List[str]) -> int:
    """把终端交给宿主 Agent；POSIX 下直接替换当前进程以保留 TTY 与信号语义。"""
    sys.stdout.flush()
    sys.stderr.flush()
    if os.name == "posix":
        try:
            os.execvp(argv[0], argv)
        except OSError as exc:
            raise DevFlowError(f"无法启动 {argv[0]}: {exc}") from exc
    return subprocess.run(argv, check=False).returncode
