import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SKILL_ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = SKILL_ROOT / "scripts/workflow_state.py"


def cli(*args, cwd=None):
    return subprocess.run(
        [sys.executable, str(WORKFLOW), *map(str, args)],
        cwd=cwd,
        env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"),
        text=True,
        capture_output=True,
        check=True,
    )


def complete_template(path: Path, evidence: str) -> None:
    text = path.read_text(encoding="utf-8")
    text = re.sub(
        r"\{\{无接口变动时写“无接口变动”；[^{}]+\}\}",
        "无接口变动",
        text,
    )
    text = re.sub(r"\{\{[^{}]+\}\}", evidence, text)
    path.write_text(text, encoding="utf-8")


class CliEndToEndTests(unittest.TestCase):
    def test_small_project_reaches_verified_completion(self):
        """Exercise the public CLI, real code/tests, artifacts, and completion gate."""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "app.py").write_text(
                "def normalize(value):\n    return value.strip().lower()\n",
                encoding="utf-8",
            )
            (root / "test_app.py").write_text(
                "import unittest\nfrom app import normalize\n\n"
                "class NormalizeTest(unittest.TestCase):\n"
                "    def test_user_input_journey(self):\n"
                "        self.assertEqual('hello', normalize(' Hello '))\n",
                encoding="utf-8",
            )
            test_result = subprocess.run(
                [sys.executable, "-m", "unittest", "-v"], cwd=root,
                text=True, capture_output=True, check=True,
            )
            self.assertIn("OK", test_result.stderr)

            state_path = Path(cli(
                "init", "--project-root", root, "--slug", "cli-e2e",
                "--size", "small", "--mode", "auto",
                "--execution-mode", "single-context", "--coordinator-id", "main",
            ).stdout.strip())
            prompt = cli(
                "prepare", "--state", state_path, "--stage", "SOLO",
                "--emit-prompt", "--request", "规范化用户输入并验证",
            ).stdout
            self.assertIn("完成前自检", prompt)
            cli("start", "--state", state_path, "--stage", "SOLO")
            artifact_root = state_path.parent
            complete_template(
                artifact_root / "01-solo/solo-report.md",
                "修改 `app.py` 并运行 `python3 -m unittest -v`，结果 passed。",
            )
            cli(
                "finish", "--state", state_path, "--stage", "SOLO",
                "--result", "completed",
            )

            cli("prepare", "--state", state_path, "--stage", "SUMMARY")
            cli("start", "--state", state_path, "--stage", "SUMMARY")
            complete_template(
                artifact_root / "workflow-summary.md",
                "交付完成；`python3 -m unittest -v` 已通过，无剩余风险。",
            )
            cli(
                "finish", "--state", state_path, "--stage", "SUMMARY",
                "--result", "completed",
            )
            validation = cli("validate", "--state", state_path)
            self.assertIn("OK", validation.stdout)
            state = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual("completed", state["status"])


if __name__ == "__main__":
    unittest.main()
