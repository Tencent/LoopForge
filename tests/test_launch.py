import json
import os
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
from unittest import mock

from devflow_cli.cli import main, program_name
from devflow_cli.core import apply_install


ROOT = Path(__file__).resolve().parents[1]


class LaunchTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.project = self.root / "project"
        self.project.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.enter_context("DEVFLOW_ASSET_ROOT", str(ROOT))
        # 隔离 PATH，让宿主 CLI 探测只看到测试自建的假 CLI
        self.enter_context("PATH", str(self.bin))

    def enter_context(self, name: str, value: str) -> None:
        original = os.environ.get(name)
        os.environ[name] = value
        if original is None:
            self.addCleanup(os.environ.pop, name, None)
        else:
            self.addCleanup(os.environ.__setitem__, name, original)

    def fake_cli(self, name: str) -> Path:
        path = self.bin / name
        path.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        path.chmod(0o755)
        return path

    def run_json(self, argv):
        output = StringIO()
        with redirect_stdout(output):
            code = main(argv)
        self.assertEqual(0, code, output.getvalue())
        return json.loads(output.getvalue())

    def plan(self, *extra, requirement="测试需求"):
        argv = ["run"]
        if requirement is not None:
            argv.append(requirement)
        argv.extend([*extra, "--dry-run", "--json", "--project-root", str(self.project)])
        return self.run_json(argv)

    def test_single_host_dry_run_uses_classic_entrypoint(self):
        self.fake_cli("claude")
        apply_install(self.project, "claude", "classic")

        selected = self.plan()["selected"]

        self.assertEqual("claude", selected["host"])
        self.assertEqual("classic", selected["edition"])
        self.assertEqual("/start-devflow 测试需求", selected["prompt"])
        self.assertEqual(
            [str(self.bin / "claude"), "/start-devflow 测试需求"], selected["argv"],
        )

    def test_classic_codex_needs_start_keyword(self):
        self.fake_cli("codex")
        apply_install(self.project, "codex", "classic")

        self.assertEqual("$devflow-codex start 测试需求", self.plan()["selected"]["prompt"])

    def test_portable_codex_has_no_extra_keyword(self):
        self.fake_cli("codex")
        apply_install(self.project, "codex", "portable")

        self.assertEqual("$devflow 测试需求", self.plan()["selected"]["prompt"])

    def test_empty_requirement_sends_entrypoint_only(self):
        self.fake_cli("claude")
        apply_install(self.project, "claude", "classic")

        self.assertEqual("/start-devflow", self.plan(requirement=None)["selected"]["prompt"])

    def test_multiple_hosts_without_tty_requires_host_flag(self):
        self.fake_cli("claude")
        self.fake_cli("codex")
        apply_install(self.project, "claude", "classic")
        apply_install(self.project, "codex", "classic")

        stderr = StringIO()
        with mock.patch("sys.stdin", mock.Mock(isatty=lambda: False)), redirect_stderr(stderr):
            code = main(["run", "--project-root", str(self.project), "测试需求"])

        self.assertEqual(1, code)
        self.assertIn("--host", stderr.getvalue())
        self.assertEqual("codex", self.plan("--host", "codex")["selected"]["host"])

    def test_dry_run_lists_every_candidate_without_prompting(self):
        self.fake_cli("claude")
        self.fake_cli("codex")
        apply_install(self.project, "claude", "classic")
        apply_install(self.project, "codex", "classic")

        payload = self.plan()

        self.assertIsNone(payload["selected"])
        self.assertEqual(["claude", "codex"], [row["host"] for row in payload["candidates"]])

    def test_missing_installation_suggests_install_command(self):
        self.fake_cli("claude")

        stderr = StringIO()
        with redirect_stderr(stderr):
            code = main(["run", "--project-root", str(self.project), "测试需求"])

        self.assertEqual(1, code)
        self.assertIn(f"{program_name()} install claude", stderr.getvalue())

    def test_missing_host_cli_names_the_override_variable(self):
        apply_install(self.project, "cursor", "portable")

        stderr = StringIO()
        with redirect_stderr(stderr):
            code = main(["run", "--project-root", str(self.project), "测试需求"])

        self.assertEqual(1, code)
        self.assertIn("cursor-agent", stderr.getvalue())
        self.assertIn("LOOPFORGE_CLI_CURSOR", stderr.getvalue())

    def test_host_cli_override_variable_selects_the_binary(self):
        self.fake_cli("my-cursor")
        apply_install(self.project, "cursor", "portable")

        with mock.patch.dict(os.environ, {"LOOPFORGE_CLI_CURSOR": "my-cursor"}):
            selected = self.plan()["selected"]

        self.assertEqual(str(self.bin / "my-cursor"), selected["cli"])
        self.assertEqual("/devflow 测试需求", selected["prompt"])

    def test_bare_requirement_is_treated_as_run(self):
        self.fake_cli("claude")
        apply_install(self.project, "claude", "classic")

        stderr = StringIO()
        output = StringIO()
        with redirect_stderr(stderr), redirect_stdout(output):
            code = main([
                "测试需求", "--dry-run", "--json", "--project-root", str(self.project),
            ])

        self.assertEqual(0, code, stderr.getvalue())
        self.assertIn("按 `run <需求>` 处理", stderr.getvalue())
        self.assertEqual("/start-devflow 测试需求", json.loads(output.getvalue())["selected"]["prompt"])

    def test_unknown_subcommand_close_to_a_command_is_rejected(self):
        stderr = StringIO()
        with redirect_stderr(stderr):
            code = main(["instal", "claude"])

        self.assertEqual(1, code)
        self.assertIn("install", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
