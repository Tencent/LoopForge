import os
import stat
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path

from devflow_cli.cli import main
from devflow_cli.core import DevFlowError, apply_install, doctor, status_rows, uninstall
from devflow_cli.editions import DEFAULT_EDITION, EDITION_SPECS, HOSTS, source_for


ROOT = Path(__file__).resolve().parents[1]


class InstallerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.old_asset_root = os.environ.get("DEVFLOW_ASSET_ROOT")
        os.environ["DEVFLOW_ASSET_ROOT"] = str(ROOT)

    @classmethod
    def tearDownClass(cls):
        if cls.old_asset_root is None:
            os.environ.pop("DEVFLOW_ASSET_ROOT", None)
        else:
            os.environ["DEVFLOW_ASSET_ROOT"] = cls.old_asset_root

    def project(self, temporary: str) -> Path:
        path = Path(temporary) / "project"
        path.mkdir()
        return path

    def test_all_host_and_edition_combinations_install_and_update(self):
        for edition in ("portable", "classic"):
            for host in ("codebuddy", "codex", "cursor", "claude"):
                with self.subTest(edition=edition, host=host), tempfile.TemporaryDirectory() as temporary:
                    project = self.project(temporary)
                    written, unchanged = apply_install(project, host, edition)
                    self.assertGreater(written, 0)
                    self.assertEqual(0, unchanged)
                    self.assertEqual([], doctor(project))
                    written, unchanged = apply_install(project, host, edition, update_only=True)
                    self.assertEqual(0, written)
                    self.assertGreater(unchanged, 0)
                    row = status_rows(project, host)[0]
                    self.assertEqual(0, row["changed"])
                    self.assertEqual(0, row["missing"])

    def test_untracked_user_file_conflict_is_atomic(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            user_file = project / ".claude/agents/devflow-stage-executor.md"
            user_file.parent.mkdir(parents=True)
            user_file.write_text("user content\n", encoding="utf-8")
            with self.assertRaises(DevFlowError):
                apply_install(project, "claude", "portable")
            self.assertEqual("user content\n", user_file.read_text(encoding="utf-8"))
            self.assertFalse((project / ".devflow/install-state.json").exists())

    def test_force_replaces_conflicting_skill_symlink_without_touching_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            external = Path(temporary) / "external-devflow"
            external.mkdir()
            external_skill = external / "SKILL.md"
            external_skill.write_text("external content\n", encoding="utf-8")
            link = project / ".agents/skills/devflow"
            link.parent.mkdir(parents=True)
            link.symlink_to(external, target_is_directory=True)

            with self.assertRaises(DevFlowError):
                apply_install(project, "codex", "portable")
            self.assertEqual(0, main([
                "skills", "install", "codex", "--project-root", str(project), "--force",
            ]))

            self.assertTrue(link.is_dir())
            self.assertFalse(link.is_symlink())
            self.assertEqual("external content\n", external_skill.read_text(encoding="utf-8"))
            self.assertNotEqual("external content\n", (link / "SKILL.md").read_text(encoding="utf-8"))

    def test_update_rejects_modified_managed_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "cursor", "portable")
            managed = project / ".cursor/agents/devflow-stage-executor.md"
            managed.write_text("user changed\n", encoding="utf-8")
            with self.assertRaises(DevFlowError):
                apply_install(project, "cursor", "portable", update_only=True)
            self.assertEqual("user changed\n", managed.read_text(encoding="utf-8"))

    def test_force_update_overwrites_modified_managed_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "cursor", "portable")
            managed = project / ".cursor/agents/devflow-stage-executor.md"
            managed.write_text("user changed\n", encoding="utf-8")
            apply_install(project, "cursor", "portable", update_only=True, force=True)
            self.assertNotEqual("user changed\n", managed.read_text(encoding="utf-8"))

    def test_uninstall_preserves_modified_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "claude", "classic")
            managed = project / ".claude/README.md"
            managed.write_text("user changed\n", encoding="utf-8")
            removed, preserved = uninstall(project, "claude")
            self.assertGreater(removed, 0)
            self.assertIn(".claude/README.md", preserved)
            self.assertEqual("user changed\n", managed.read_text(encoding="utf-8"))

    def test_same_host_cannot_mix_editions(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "codex", "portable")
            with self.assertRaises(DevFlowError):
                apply_install(project, "codex", "classic")

    def test_force_install_switches_editions_in_both_directions(self):
        cases = (
            ("portable", "classic", ".codebuddy/commands/devflow.md", ".codebuddy/commands/start-devflow.md"),
            ("classic", "portable", ".codebuddy/commands/start-devflow.md", ".codebuddy/commands/devflow.md"),
        )
        for source, target, removed_path, installed_path in cases:
            with self.subTest(source=source, target=target), tempfile.TemporaryDirectory() as temporary:
                project = self.project(temporary)
                apply_install(project, "codebuddy", source)

                apply_install(project, "codebuddy", target, force=True)

                rows = status_rows(project, "codebuddy")
                self.assertEqual(1, len(rows))
                self.assertEqual(target, rows[0]["edition"])
                self.assertFalse((project / removed_path).exists())
                self.assertTrue((project / installed_path).is_file())

    def test_force_edition_switch_preserves_modified_old_only_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "codebuddy", "portable")
            old_only = project / ".codebuddy/commands/devflow.md"
            old_only.write_text("user changed\n", encoding="utf-8")

            apply_install(project, "codebuddy", "classic", force=True)

            self.assertEqual("user changed\n", old_only.read_text(encoding="utf-8"))
            self.assertEqual("classic", status_rows(project, "codebuddy")[0]["edition"])

    def test_edition_registry_is_complete_and_sources_exist(self):
        self.assertEqual("classic", DEFAULT_EDITION)
        self.assertEqual(("portable", "classic"), tuple(EDITION_SPECS))
        for edition, metadata in EDITION_SPECS.items():
            hosts = metadata.get("hosts", HOSTS)
            self.assertEqual(set(hosts), set(metadata["entrypoints"]))
            for host in hosts:
                self.assertTrue((ROOT / source_for(edition, host)).is_dir())

    def test_pi_is_portable_only(self):
        self.assertIn("pi", EDITION_SPECS["portable"]["hosts"])
        self.assertNotIn("pi", EDITION_SPECS["classic"]["hosts"])
        self.assertEqual("/skill:devflow", EDITION_SPECS["portable"]["entrypoints"]["pi"])

    def test_pi_installs_portable_skills_and_rejects_classic(self):
        with tempfile.TemporaryDirectory() as td:
            project = Path(td)
            installed, _unchanged = apply_install(project, "pi", "portable")
            self.assertGreater(installed, 0)
            self.assertTrue((project / ".pi/skills/devflow/SKILL.md").is_file())
            self.assertTrue((project / ".pi/skills/manifest.json").is_file())
            with self.assertRaises(DevFlowError):
                apply_install(project, "pi", "classic")

    def test_opencode_installs_portable_skills_and_agents_and_rejects_classic(self):
        with tempfile.TemporaryDirectory() as td:
            project = Path(td)
            installed, _unchanged = apply_install(project, "opencode", "portable")
            self.assertGreater(installed, 0)
            self.assertTrue((project / ".opencode/skills/devflow/SKILL.md").is_file())
            self.assertTrue((project / ".opencode/skills/manifest.json").is_file())
            stage = (project / ".opencode/agents/devflow-stage-executor.md").read_text(
                encoding="utf-8"
            )
            helper = (project / ".opencode/agents/devflow-research-helper.md").read_text(
                encoding="utf-8"
            )
            self.assertIn('mode: "subagent"', stage)
            self.assertIn('permission: {"edit": "allow"}', stage)
            self.assertIn('permission: {"edit": "deny", "bash": "deny"}', helper)
            with self.assertRaises(DevFlowError):
                apply_install(project, "opencode", "classic")

    def test_editions_and_plan_make_the_split_visible_without_writing(self):
        output = StringIO()
        with redirect_stdout(output):
            self.assertEqual(0, main(["editions"]))
        text = output.getvalue()
        self.assertIn("portable:", text)
        self.assertIn("sources: skills/", text)
        self.assertIn("classic (default)", text)
        self.assertIn(".codebuddy/", text)

        output = StringIO()
        with redirect_stdout(output):
            self.assertEqual(0, main(["plan", "claude"]))
        text = output.getvalue()
        self.assertIn("edition=classic host=claude source=.claude/", text)
        self.assertIn(".claude/commands/start-devflow.md", text)

    def test_install_output_names_project_and_next_entrypoint(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            output = StringIO()
            with redirect_stdout(output):
                self.assertEqual(0, main([
                    "install", "codebuddy", "--project-root", str(project),
                ]))
            text = output.getvalue()
            self.assertIn(f"project={project.resolve()}", text)
            self.assertIn("edition=classic", text)
            self.assertIn("run /start-devflow <你的需求>", text)

    def test_skills_command_installs_and_manages_portable_edition(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            output = StringIO()
            with redirect_stdout(output):
                self.assertEqual(0, main([
                    "skills", "install", "codex", "--project-root", str(project),
                ]))
            self.assertIn("OK: skills install host=codex", output.getvalue())
            self.assertIn("run $devflow <你的需求>", output.getvalue())
            self.assertTrue((project / ".agents/skills/devflow/SKILL.md").is_file())
            self.assertFalse((project / ".codex").exists())

            output = StringIO()
            with redirect_stdout(output):
                self.assertEqual(0, main([
                    "skills", "status", "codex", "--project-root", str(project),
                ]))
            self.assertIn("skills/codex:", output.getvalue())

            output = StringIO()
            with redirect_stdout(output):
                self.assertEqual(0, main([
                    "skills", "uninstall", "codex", "--project-root", str(project),
                ]))
            self.assertIn("OK: skills uninstall host=codex", output.getvalue())

    def test_portable_install_keeps_skill_manifest_with_copied_skills(self):
        skill_roots = {
            "codebuddy": ".codebuddy/skills",
            "codex": ".agents/skills",
            "cursor": ".cursor/skills",
            "claude": ".claude/skills",
            "opencode": ".opencode/skills",
        }
        for host, relative in skill_roots.items():
            with self.subTest(host=host), tempfile.TemporaryDirectory() as temporary:
                project = self.project(temporary)
                apply_install(project, host, "portable")
                self.assertTrue((project / relative / "manifest.json").is_file())

    def test_codebuddy_copy_contains_runnable_generic_adapter_installer(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "codebuddy", "portable")
            downstream = Path(temporary) / "downstream"
            downstream.mkdir()
            installer = project / ".codebuddy/skills/devflow/scripts/install_adapter.py"
            result = subprocess.run(
                [
                    sys.executable,
                    str(installer),
                    "--adapter",
                    "claude",
                    "--project-root",
                    str(downstream),
                    "--copy-skills",
                    "--refresh-managed",
                ],
                check=False,
                capture_output=True,
                text=True,
            )
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertTrue((downstream / ".claude/skills/manifest.json").is_file())
            self.assertTrue((downstream / ".claude/skills/devflow/SKILL.md").is_file())

    def test_codebuddy_portable_agents_use_autorun_bypass_permissions(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "codebuddy", "portable")
            for name in ("devflow-stage-executor.md", "devflow-research-helper.md"):
                text = (project / ".codebuddy/agents" / name).read_text(encoding="utf-8")
                self.assertIn("permissionMode: bypassPermissions", text)
                self.assertIn("enabledAutoRun: true", text)

    def test_classic_install_makes_observability_hook_executable(self):
        hooks = (
            ("codebuddy", ".codebuddy/skills/agent-observability/scripts/run_hook.sh"),
            ("cursor", ".cursor/skills/agent-observability/scripts/run_hook.sh"),
            ("claude", ".claude/skills/agent-observability/scripts/run_hook.sh"),
        )
        for host, relative in hooks:
            with self.subTest(host=host), tempfile.TemporaryDirectory() as temporary:
                project = self.project(temporary)
                apply_install(project, host, "classic")
                hook = project / relative
                self.assertTrue(hook.is_file(), relative)
                self.assertTrue(hook.stat().st_mode & stat.S_IXUSR, relative)

    def test_update_restores_missing_executable_bit_on_hook(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "codebuddy", "classic")
            hook = project / ".codebuddy/skills/agent-observability/scripts/run_hook.sh"
            hook.chmod(0o644)
            written, unchanged = apply_install(project, "codebuddy", "classic", update_only=True)
            self.assertEqual(0, written)
            self.assertGreater(unchanged, 0)
            self.assertTrue(hook.stat().st_mode & stat.S_IXUSR)


if __name__ == "__main__":
    unittest.main()
