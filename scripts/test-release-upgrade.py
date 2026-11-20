import importlib.util
from pathlib import Path
import unittest


spec = importlib.util.spec_from_file_location('release_upgrade_base', Path(__file__).with_name('release-upgrade-base.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class UpgradeBaseTests(unittest.TestCase):
    def test_uses_numeric_version_order_instead_of_publication_order(self):
        releases = [{'tagName': tag} for tag in ('v0.3.10', 'v0.3.9', 'v0.3.2')]
        self.assertEqual(release.upgrade_base('v0.3.11', releases), 'v0.3.10')

    def test_republishing_excludes_the_target_and_newer_releases(self):
        releases = [{'tagName': tag} for tag in ('v0.4.0', 'v0.3.4', 'v0.3.3')]
        self.assertEqual(release.upgrade_base('v0.3.4', releases), 'v0.3.3')

    def test_excludes_local_history_drafts_and_prereleases(self):
        releases = [
            {'tagName': 'local-v99.0.0'},
            {'tagName': 'v0.2.3'},
            {'tagName': 'v0.3.4-rc.1'},
            {'tagName': 'v0.3.4', 'isDraft': True},
            {'tagName': 'v0.3.3', 'isPrerelease': True},
            {'tagName': 'v0.3.2'},
        ]
        self.assertEqual(release.upgrade_base('v0.3.5', releases), 'v0.3.2')

    def test_prerelease_upgrades_from_an_earlier_stable_version(self):
        releases = [{'tagName': tag} for tag in ('v0.3.5', 'v0.3.4')]
        self.assertEqual(release.upgrade_base('v0.3.5-rc.1', releases), 'v0.3.4')

    def test_only_the_first_platform_release_can_have_no_baseline(self):
        releases = [{'tagName': 'v0.2.3'}]
        self.assertEqual(release.upgrade_base('v0.3.0', releases), '')
        with self.assertRaisesRegex(ValueError, 'refusing to skip'):
            release.upgrade_base('v0.3.1', releases)

    def test_rejects_non_platform_target(self):
        with self.assertRaises(ValueError):
            release.upgrade_base('local-v0.3.5', [])


if __name__ == '__main__':
    unittest.main()
