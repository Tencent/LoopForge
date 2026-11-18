import json
import os
import re
import stat
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path

from devflow_cli.cli import main
from devflow_cli.core import DevFlowError, apply_install, digest, doctor, plan_install, status_rows, uninstall
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

    def run_cli(self, argv):
        stdout = StringIO()
        stderr = StringIO()
        with redirect_stdout(stdout), redirect_stderr(stderr):
            code = main(argv)
        return code, stdout.getvalue(), stderr.getvalue()

    def tree_snapshot(self, root: Path) -> dict:
        entries = {}
        for path in sorted(root.rglob("*")):
            rel = str(path.relative_to(root))
            mode = stat.S_IMODE(path.lstat().st_mode)
            if path.is_symlink():
                entries[rel] = ("symlink", os.readlink(str(path)), mode)
            elif path.is_dir():
                entries[rel] = ("dir", mode)
            elif path.is_file():
                entries[rel] = ("file", path.read_bytes(), mode)
        return {
            "devflow": (root / ".devflow").exists(),
            "state": (root / ".devflow/install-state.json").exists(),
            "entries": entries,
        }

    def summary_counts(self, output: str):
        match = re.search(
            r"add=(\d+) update=(\d+) unchanged=(\d+) remove=(\d+) conflict=(\d+)",
            output,
        )
        self.assertIsNotNone(match, output)
        return tuple(int(value) for value in match.groups())

    def track_extra_file(self, project: Path, relative: str, content: str) -> Path:
        path = project / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        state_path = project / ".devflow/install-state.json"
        state = json.loads(state_path.read_text(encoding="utf-8"))
        key = next(iter(state["installations"]))
        state["installations"][key]["files"].append({
            "path": relative,
            "sha256": digest(path.read_bytes()),
        })
        state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
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

    def test_dsh_installs_portable_skills_and_rejects_classic(self):
        with tempfile.TemporaryDirectory() as td:
            project = Path(td)
            installed, _unchanged = apply_install(project, "dsh", "portable")
            self.assertGreater(installed, 0)
            self.assertTrue((project / ".dsh/skills/devflow/SKILL.md").is_file())
            self.assertTrue((project / ".dsh/skills/manifest.json").is_file())
            self.assertTrue((project / ".dsh/skills/devflow/adapters/dsh/manifest.json").is_file())
            with self.assertRaises(DevFlowError):
                apply_install(project, "dsh", "classic")

    def test_hermes_installs_project_skills_and_rejects_classic(self):
        with tempfile.TemporaryDirectory() as td:
            project = Path(td)
            installed, _unchanged = apply_install(project, "hermes", "portable")
            self.assertGreater(installed, 0)
            self.assertTrue((project / ".hermes/skills/devflow/SKILL.md").is_file())
            self.assertTrue((project / ".hermes/skills/manifest.json").is_file())
            self.assertTrue((project / ".hermes/skills/devflow/adapters/hermes/manifest.json").is_file())
            with self.assertRaises(DevFlowError):
                apply_install(project, "hermes", "classic")

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

    def test_dry_run_fresh_install_is_all_add_and_writes_nothing(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "install", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(0, code, err)
            self.assertTrue(
                output.startswith("DRY RUN: install edition=classic host=claude "),
                output,
            )
            add, update, unchanged, remove, conflict = self.summary_counts(output)
            self.assertGreater(add, 0)
            self.assertEqual((0, 0, 0, 0), (update, unchanged, remove, conflict))
            self.assertIn("  add .claude/", output)
            self.assertNotIn("  update ", output)
            self.assertNotIn("  conflict ", output)
            self.assertEqual("", err)
            self.assertEqual(before, self.tree_snapshot(project))

            preview = plan_install(project, "claude", "classic")
            self.assertEqual(add, preview.counts()["add"])
            self.assertTrue(all(action == "add" for _path, action in preview.files))
            self.assertEqual(before, self.tree_snapshot(project))

            code, output, err = self.run_cli([
                "skills", "install", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(0, code, err)
            self.assertTrue(
                output.startswith("DRY RUN: skills install edition=portable host=claude "),
                output,
            )
            self.assertEqual(before, self.tree_snapshot(project))

    def test_dry_run_after_install_is_all_unchanged(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "claude", "classic")
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "update", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(0, code, err)
            add, update, unchanged, remove, conflict = self.summary_counts(output)
            self.assertEqual((0, 0, 0, 0), (add, update, remove, conflict))
            self.assertGreater(unchanged, 0)
            self.assertNotIn("\n  add ", "\n" + output)
            self.assertEqual(before, self.tree_snapshot(project))

            preview = plan_install(project, "claude", "classic", update_only=True)
            self.assertEqual(unchanged, preview.counts()["unchanged"])
            self.assertTrue(all(action == "unchanged" for _path, action in preview.files))
            self.assertEqual(before, self.tree_snapshot(project))

    def test_dry_run_user_modified_tracked_file_conflict_and_force_overwrite(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "claude", "classic")
            managed = project / ".claude/README.md"
            managed.write_text("user changed\n", encoding="utf-8")
            before = self.tree_snapshot(project)

            code, output, err = self.run_cli([
                "update", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            add, update, unchanged, remove, conflict = self.summary_counts(output)
            self.assertEqual(1, conflict)
            self.assertEqual(0, add)
            self.assertIn("  conflict .claude/README.md", output)
            self.assertEqual(before, self.tree_snapshot(project))
            self.assertEqual("user changed\n", managed.read_text(encoding="utf-8"))

            code, output, err = self.run_cli([
                "update", "claude", "--dry-run", "--force", "--project-root", str(project),
            ])
            self.assertEqual(0, code, err)
            self.assertIn("  overwrite .claude/README.md", output)
            self.assertNotIn("  conflict ", output)
            _add, _update, _unchanged, _remove, conflict = self.summary_counts(output)
            self.assertEqual(1, conflict)
            self.assertEqual(before, self.tree_snapshot(project))
            self.assertEqual("user changed\n", managed.read_text(encoding="utf-8"))

    def test_dry_run_untracked_user_file_at_bundle_path(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            user_file = project / ".claude/commands/start-devflow.md"
            user_file.parent.mkdir(parents=True)
            user_file.write_text("user content\n", encoding="utf-8")
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "install", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            self.assertIn("  conflict .claude/commands/start-devflow.md", output)
            self.assertEqual(before, self.tree_snapshot(project))
            self.assertEqual("user content\n", user_file.read_text(encoding="utf-8"))
            self.assertFalse((project / ".devflow/install-state.json").exists())

    def test_dry_run_blocking_parent_is_conflict(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            blocker = project / ".claude"
            blocker.write_text("not a directory\n", encoding="utf-8")
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "install", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            self.assertIn("  conflict .claude", output)
            _add, _update, _unchanged, _remove, conflict = self.summary_counts(output)
            self.assertGreaterEqual(conflict, 1)
            self.assertEqual(before, self.tree_snapshot(project))
            self.assertEqual("not a directory\n", blocker.read_text(encoding="utf-8"))

    def test_dry_run_dropped_tracked_file_is_remove(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "claude", "classic")
            extra = self.track_extra_file(project, ".claude/extra-tracked.md", "tracked extra\n")
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "update", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(0, code, err)
            add, update, unchanged, remove, conflict = self.summary_counts(output)
            self.assertEqual(1, remove)
            self.assertEqual(0, conflict)
            self.assertIn("  remove .claude/extra-tracked.md", output)
            self.assertEqual(before, self.tree_snapshot(project))
            self.assertEqual("tracked extra\n", extra.read_text(encoding="utf-8"))

            extra.write_text("user changed extra\n", encoding="utf-8")
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "update", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            self.assertIn("  conflict .claude/extra-tracked.md", output)
            self.assertEqual(before, self.tree_snapshot(project))

    def test_dry_run_edition_switch_requires_force(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            apply_install(project, "claude", "classic")
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "install", "claude", "--edition", "portable", "--dry-run",
                "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            self.assertIn("如需切换到 portable", err)
            self.assertEqual("", output)
            self.assertEqual(before, self.tree_snapshot(project))

            code, output, err = self.run_cli([
                "install", "claude", "--edition", "portable", "--dry-run", "--force",
                "--project-root", str(project),
            ])
            self.assertEqual(0, code, err)
            self.assertIn("DRY RUN: install edition=portable host=claude ", output)
            self.assertIn("  switch classic -> portable", output)
            self.assertEqual(before, self.tree_snapshot(project))

            code, output, err = self.run_cli([
                "update", "claude", "--edition", "portable", "--dry-run", "--force",
                "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            self.assertIn("update 不能切换 edition", err)
            self.assertEqual(before, self.tree_snapshot(project))

    def test_dry_run_json_and_update_without_install(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            before = self.tree_snapshot(project)
            code, output, err = self.run_cli([
                "install", "claude", "--dry-run", "--json", "--project-root", str(project),
            ])
            self.assertEqual(0, code, err)
            payload = json.loads(output)
            self.assertEqual("install", payload["command"])
            self.assertEqual("classic", payload["edition"])
            self.assertEqual("claude", payload["host"])
            self.assertGreater(payload["add"], 0)
            self.assertEqual(0, payload["conflict"])
            self.assertTrue(all(item["action"] == "add" for item in payload["files"]))
            self.assertEqual(before, self.tree_snapshot(project))

            code, output, err = self.run_cli([
                "install", "claude", "--json", "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            self.assertIn("--json 只能与 --dry-run 一起使用", err)
            self.assertEqual(before, self.tree_snapshot(project))

            code, output, err = self.run_cli([
                "update", "claude", "--dry-run", "--project-root", str(project),
            ])
            self.assertEqual(1, code)
            self.assertIn("尚未安装 classic/claude", err)
            self.assertEqual(before, self.tree_snapshot(project))

    def test_dry_run_plan_matches_apply_install(self):
        with tempfile.TemporaryDirectory() as temporary:
            project = self.project(temporary)
            preview = plan_install(project, "claude", "classic")
            counts = preview.counts()
            written, unchanged = apply_install(project, "claude", "classic")
            self.assertEqual(counts["add"] + counts["update"], written)
            self.assertEqual(counts["unchanged"], unchanged)
            for path, action in preview.files:
                if action == "add":
                    self.assertTrue((project / path).is_file(), path)

            extra = self.track_extra_file(project, ".claude/extra-tracked.md", "tracked extra\n")
            managed = project / ".claude/README.md"
            managed.write_text("user changed\n", encoding="utf-8")
            preview = plan_install(project, "claude", "classic", update_only=True, force=True)
            counts = preview.counts()
            labeled = dict(preview.files)
            self.assertEqual("remove", labeled[".claude/extra-tracked.md"])
            self.assertEqual("conflict", labeled[".claude/README.md"])
            written, unchanged = apply_install(
                project, "claude", "classic", update_only=True, force=True,
            )
            bundle_conflicts = sum(
                1 for path, action in preview.files
                if action == "conflict" and path in preview.bundle
            )
            self.assertEqual(counts["add"] + counts["update"] + bundle_conflicts, written)
            self.assertEqual(counts["unchanged"], unchanged)
            self.assertFalse(extra.exists())
            self.assertNotEqual("user changed\n", managed.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
