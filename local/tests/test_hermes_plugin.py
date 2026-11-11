import importlib.util
import json
import os
import sys
import tempfile
import unittest
import shutil
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
PLUGIN = ROOT / "hermes-plugin" / "__init__.py"


def load_plugin():
    spec = importlib.util.spec_from_file_location("loopforge_hermes_plugin", PLUGIN)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class HermesPluginTests(unittest.TestCase):
    def test_register_uses_public_slash_command_api(self):
        module = load_plugin()
        calls = []

        class Context:
            def register_command(self, name, handler, description="", args_hint=""):
                calls.append((name, handler, description, args_hint))

        module.register(Context())
        self.assertEqual("loopforge", calls[0][0])
        self.assertIn("LoopForge", calls[0][2])
        self.assertEqual("[start <request>|status [slug]|result [slug]|approve [stage]|help]", calls[0][3])

    def test_commands_use_project_state_and_leave_profile_unmodified(self):
        module = load_plugin()
        with tempfile.TemporaryDirectory() as temporary:
            project = Path(temporary)
            skill_root = project / ".hermes/skills/devflow"
            source = ROOT / "skills"
            shutil.copytree(source, skill_root.parent)
            profile = project / ".hermes/config.yaml"
            profile.write_text("user_setting: keep\n", encoding="utf-8")
            old_root = os.environ.get("LOOPFORGE_PROJECT_ROOT")
            os.environ["LOOPFORGE_PROJECT_ROOT"] = str(project)
            try:
                started = module._handle("start add tests")
                self.assertIn("LoopForge task initialized", started)
                status = module._handle("status")
                self.assertIn("Task: add-tests", status)
                result = module._handle("result")
                self.assertIn("- workflow-state.json", result)
            finally:
                if old_root is None:
                    os.environ.pop("LOOPFORGE_PROJECT_ROOT", None)
                else:
                    os.environ["LOOPFORGE_PROJECT_ROOT"] = old_root
            self.assertEqual("user_setting: keep\n", profile.read_text(encoding="utf-8"))
            state = json.loads(
                (project / "artifacts/add-tests/workflow-state.json").read_text(encoding="utf-8")
            )
            self.assertEqual("hermes", state["host_adapter"])
            self.assertEqual("isolated", state["execution_mode"])


if __name__ == "__main__":
    unittest.main()
