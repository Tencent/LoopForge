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


def classic_hook_env(project):
    """Environment the CodeBuddy host gives its hooks; CLS mirroring stays off."""
    env = dict(os.environ, CODEBUDDY_PROJECT_DIR=str(project), PYTHONDONTWRITEBYTECODE="1")
    for key in list(env):
        if key.startswith(("CLS_", "TENCENTCLOUD_", "TC_")):
            del env[key]
    return env


def run_hook(project, command, payload):
    result = subprocess.run(
        [str(item) for item in command], input=json.dumps(payload), cwd=str(project),
        env=classic_hook_env(project), capture_output=True, text=True, timeout=60,
    )
    if result.returncode != 0:
        raise AssertionError("hook {} failed: {}".format(command, result.stderr))
    return result.stdout


def send_dispatch(project, sender, event, target, state_path):
    payload = {"event": event, "dispatch_prompt": "continue as " + target,
               "next_target": {"subagent_name": target},
               "workflow_state_path": str(state_path)}
    output = run_hook(
        project, [sys.executable, Path(project) / ".codebuddy/hooks/devflow-auto-dispatch.py"],
        {"tool_name": "SendMessage", "tool_input": {
            "type": "message", "from": sender, "recipient": "main",
            "content": json.dumps(payload)}},
    )
    return json.loads(output)


def seed_classic_run(project, slug="e2e-classic"):
    """A Classic workflow-state.json as the leader writes it at Phase 0."""
    state_path = Path(project) / "artifacts" / slug / "workflow-state.json"
    state_path.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy(str(Path(project) / ".codebuddy/assets/workflow-state-template.json"), str(state_path))
    return state_path


def record_classic_session(project, state_path, session_id="e2e-session"):
    """Replay one recorded CodeBuddy session through the observability hooks."""
    project = Path(project)
    skill = project / ".codebuddy/skills/agent-observability"
    transcript = project / "session.jsonl"
    shutil.copy(str(REPO / ".codebuddy/skills/agent-observability/tests/fixtures/transcript.jsonl"),
                str(transcript))
    base = {"session_id": session_id, "cwd": str(project), "transcript_path": str(transcript)}
    tool = dict(base, tool_name="Bash", tool_input={"command": "ls"}, tool_use_id="call-002")
    hook = [skill / "scripts/run_hook.sh"]
    run_hook(project, hook + ["session-start"], base)
    run_hook(project, hook + ["user-prompt-submit"], dict(base, prompt="/start-devflow fix slugify"))
    run_hook(project, hook + ["pre"], tool)
    run_hook(project, hook + ["post"], dict(tool, tool_response={"stdout": "ok"}))
    send_dispatch(project, "requirement-owner", "TASK-01_completed", "architect", state_path)
    run_hook(project, hook + ["stop"], base)


def build_dashboard(project):
    skill = Path(project) / ".codebuddy/skills/agent-observability"
    output = run([sys.executable, skill / "scripts/build_dashboard_data.py", "--project-root", project],
                 cwd=project, env=classic_hook_env(project)).stdout
    return output, skill / "scripts/dashboard"


class StaticServer(object):
    """Serve a directory on a random 127.0.0.1 port for the duration of a test."""

    def __init__(self, test_case, directory):
        import functools
        import threading
        from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

        class Quiet(SimpleHTTPRequestHandler):
            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(Quiet, directory=str(directory)))
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        test_case.addCleanup(self.server.server_close)
        test_case.addCleanup(self.server.shutdown)
        self.url = "http://127.0.0.1:{}/".format(self.server.server_address[1])
