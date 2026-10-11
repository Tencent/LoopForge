"""Shared helpers for the LoopForge E2E suite.

By default the suite tests what users actually install: it builds the npm
tarball once (``npm pack``), installs it into a throw-away prefix and drives the
resulting ``loopforge`` binary. Set ``LOOPFORGE_E2E_SOURCE=1`` to run the CLI
straight from ``src/`` for a faster local loop.
"""

import atexit
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
E2E = Path(__file__).resolve().parent
FIXTURES = E2E / "fixtures"
FAKE_AGENT = E2E / "fake_agent.py"

sys.path.insert(0, str(REPO / "src"))
from devflow_cli.editions import EDITIONS, entrypoint_for, hosts_for  # noqa: E402

_PACKAGE = {}


def _env(**extra):
    env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1", NO_UPDATE_NOTIFIER="1")
    env.update(extra)
    return env


def run(argv, cwd=None, check=True, env=None):
    result = subprocess.run(
        [str(item) for item in argv], cwd=str(cwd) if cwd else None,
        text=True, capture_output=True, env=env or _env(),
    )
    if check and result.returncode != 0:
        raise AssertionError(
            "command failed (rc={}): {}\n--- stdout\n{}\n--- stderr\n{}".format(
                result.returncode, " ".join(map(str, argv)), result.stdout, result.stderr
            )
        )
    return result


def loopforge_command():
    """Return argv prefix for the CLI under test (installed from the npm tarball)."""
    if os.environ.get("LOOPFORGE_E2E_SOURCE") == "1":
        return [sys.executable, "-m", "devflow_cli.cli"]
    if "argv" not in _PACKAGE:
        if not shutil.which("npm"):
            raise unittest.SkipTest("npm is required; set LOOPFORGE_E2E_SOURCE=1 to skip packaging")
        workdir = Path(tempfile.mkdtemp(prefix="loopforge-e2e-pkg-"))
        _PACKAGE["dir"] = workdir
        atexit.register(shutil.rmtree, str(workdir), True)  # shared by all test modules
        packed = json.loads(run(
            ["npm", "pack", REPO, "--pack-destination", workdir, "--json"]
        ).stdout)
        tarball = workdir / packed[0]["filename"]
        prefix = workdir / "prefix"
        run(["npm", "install", "--global", "--prefix", prefix, tarball])
        binary = prefix / "bin" / "loopforge"
        if not binary.exists():
            raise AssertionError("npm package did not install a loopforge binary")
        _PACKAGE["argv"] = [binary]
    return list(_PACKAGE["argv"])


def loopforge(*args, cwd=None, check=True):
    env = _env()
    if os.environ.get("LOOPFORGE_E2E_SOURCE") == "1":
        env["PYTHONPATH"] = str(REPO / "src")
    return run(loopforge_command() + list(args), cwd=cwd, check=check, env=env)


def new_project(test_case, with_sample=True):
    """Create a throw-away git repo that looks like a user's project."""
    root = Path(tempfile.mkdtemp(prefix="loopforge-e2e-project-"))
    test_case.addCleanup(shutil.rmtree, str(root), True)
    if with_sample:
        for item in (FIXTURES / "sample_project").glob("*.py"):
            shutil.copy(str(item), str(root / item.name))
    (root / "USER_NOTES.md").write_text("user file that must survive\n", encoding="utf-8")
    run(["git", "init", "-q", root])
    return root


def fake_agent(host, project, *extra, check_rc=None):
    result = run(
        [sys.executable, FAKE_AGENT, "--host", host, "--project-root", project] + list(extra),
        check=False,
    )
    if check_rc is not None and result.returncode != check_rc:
        raise AssertionError(
            "fake agent rc={} (expected {})\n{}\n{}".format(
                result.returncode, check_rc, result.stderr, transcript_text(project)
            )
        )
    return result


def transcript(project):
    path = Path(project) / ".fake-agent" / "transcript.jsonl"
    if not path.is_file():
        return []
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]


def transcript_text(project):
    return "\n".join(
        json.dumps(item, ensure_ascii=False) for item in transcript(project)
        if item["kind"] != "command"
    )


def workflow_state(project, slug="e2e-task"):
    return json.loads(
        (Path(project) / "artifacts" / slug / "workflow-state.json").read_text(encoding="utf-8")
    )


def files_under(root):
    root = Path(root)
    return sorted(
        str(path.relative_to(root)) for path in root.rglob("*")
        if (path.is_file() or path.is_symlink()) and ".git" not in path.relative_to(root).parts
    )


def loopforge_validate(project, slug="e2e-task", host_skills=None):
    """Run the installed ``workflow_state.py validate`` for a finished task."""
    from fake_agent import HOST_SKILL_DIRS
    project = Path(project)
    for skills in HOST_SKILL_DIRS.values():
        script = project / skills / "devflow/scripts/workflow_state.py"
        if script.is_file():
            state = project / "artifacts" / slug / "workflow-state.json"
            return run([sys.executable, script, "validate", "--state", state]).stdout
    raise AssertionError("no installed workflow_state.py found")
