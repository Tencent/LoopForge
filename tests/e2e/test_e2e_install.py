"""E2E: install the packaged CLI into a user project for every supported host."""

import json
import unittest
from pathlib import Path

import harness as h
from fake_agent import HOST_SKILL_DIRS


def entry_files(edition, host):
    """Files the host Agent must find to expose the documented entrypoint."""
    if edition == "classic":
        if host == "codex":
            return [".codex/skills/devflow-codex/SKILL.md", ".agents/skills/devflow-codex/SKILL.md"]
        return [".{}/commands/start-devflow.md".format(host), ".{}/commands/resume-devflow.md".format(host)]
    skills = HOST_SKILL_DIRS[host]
    files = [skills + "/devflow/SKILL.md", skills + "/devflow-clarify-requirements/SKILL.md",
             skills + "/devflow/scripts/workflow_state.py"]
    if host in ("codebuddy", "cursor", "claude"):
        files.append(".{}/agents/devflow-stage-executor.md".format(host))
    if host == "codebuddy":
        files.append(".codebuddy/commands/devflow.md")
    return files


def install_args(edition, host, command="install"):
    if edition == "portable":
        return ["skills", command, host]
    return [command, host]


class InstallMatrixE2E(unittest.TestCase):
    def test_every_host_and_edition_installs_updates_and_uninstalls_cleanly(self):
        for edition in h.EDITIONS:
            for host in h.hosts_for(edition):
                with self.subTest(edition=edition, host=host):
                    project = h.new_project(self, with_sample=False)
                    before = h.files_under(project)
                    out = h.loopforge(*install_args(edition, host), "--project-root", project).stdout
                    self.assertIn(h.entrypoint_for(edition, host), out)
                    for relative in entry_files(edition, host):
                        self.assertTrue((project / relative).is_file(), relative)

                    status_cmd = ["skills", "status", host] if edition == "portable" else ["status", host]
                    rows = json.loads(h.loopforge(*status_cmd, "--project-root", project, "--json").stdout)
                    self.assertEqual(1, len(rows), rows)
                    self.assertEqual((edition, 0, 0), (rows[0]["edition"], rows[0]["missing"], rows[0]["changed"]))
                    self.assertIn("healthy", h.loopforge("doctor", "--project-root", project).stdout)

                    update = h.loopforge(*install_args(edition, host, "update"), "--project-root", project).stdout
                    self.assertIn("written=0", update)

                    h.loopforge(*install_args(edition, host, "uninstall"), "--project-root", project)
                    self.assertEqual(before, h.files_under(project))

    def test_classic_is_rejected_for_portable_only_host(self):
        project = h.new_project(self, with_sample=False)
        before = h.files_under(project)
        result = h.loopforge("install", "pi", "--project-root", project, check=False)
        self.assertNotEqual(0, result.returncode)
        self.assertEqual(before, h.files_under(project))

    def test_second_edition_for_same_host_requires_force(self):
        project = h.new_project(self, with_sample=False)
        h.loopforge("install", "claude", "--project-root", project)
        before = h.files_under(project)
        result = h.loopforge("skills", "install", "claude", "--project-root", project, check=False)
        self.assertNotEqual(0, result.returncode)
        self.assertEqual(before, h.files_under(project))
        h.loopforge("skills", "install", "claude", "--project-root", project, "--force")
        self.assertTrue((project / ".claude/skills/devflow/SKILL.md").is_file())
        self.assertFalse((project / ".claude/commands/start-devflow.md").exists())


if __name__ == "__main__":
    unittest.main()
