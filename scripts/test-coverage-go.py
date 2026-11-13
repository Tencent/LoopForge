from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().with_name('coverage-go.sh')


class CoverageGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def profile(self, name, blocks):
        profile = self.root / name
        profile.write_text('mode: set\n' + blocks)
        return profile

    def gate(self, *profiles, minimum='86.0'):
        args = ['bash', str(SCRIPT), '--min', minimum]
        for profile in profiles:
            args.extend(['--profile', str(profile)])
        return subprocess.run(args, capture_output=True, text=True, timeout=10)

    def test_combines_hits_across_profiles_without_counting_blocks_twice(self):
        core = self.profile('core.out', (
            'agentflow/internal/runtime/browser.go:1.1,2.2 6 1\n'
            'agentflow/internal/runtime/browser.go:3.1,4.2 3 0\n'
            'agentflow/internal/runtime/browser.go:5.1,6.2 1 0\n'
            'agentflow/cmd/main.go:1.1,2.2 100 0\n'
        ))
        browser = self.profile('browser.out', (
            'agentflow/internal/runtime/browser.go:1.1,2.2 6 0\n'
            'agentflow/internal/runtime/browser.go:3.1,4.2 3 1\n'
            'agentflow/internal/runtime/browser.go:5.1,6.2 1 0\n'
            'agentflow/deploy/main.go:1.1,2.2 100 0\n'
        ))
        self.assertEqual(self.gate(core).returncode, 1)
        for profiles in ((core, browser), (browser, core), (core, browser, core)):
            with self.subTest(profiles=profiles):
                result = self.gate(*profiles)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertRegex(result.stdout, r'total\s+10\s+9\s+1\s+90\.0000%')

    def test_rejects_ci_measurement_below_threshold_despite_rounding(self):
        profile = self.profile('ci.out', (
            'agentflow/internal/engine/run.go:1.1,2.2 48051 1\n'
            'agentflow/internal/engine/run.go:3.1,4.2 7834 0\n'
        ))
        result = self.gate(profile)
        self.assertEqual(result.returncode, 1)
        self.assertIn('85.9819% (48051/55885 statements)', result.stderr)

    def test_accepts_exact_threshold(self):
        profile = self.profile('exact.out', (
            'agentflow/internal/engine/run.go:1.1,2.2 86 1\n'
            'agentflow/internal/engine/run.go:3.1,4.2 14 0\n'
        ))
        result = self.gate(profile)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_requires_every_supplied_profile(self):
        profile = self.profile('complete.out', 'agentflow/internal/auth/auth.go:1.1,2.2 1 1\n')
        empty = self.root / 'empty.out'
        empty.write_text('')
        for extra in (self.root / 'missing.out', empty):
            with self.subTest(profile=extra):
                result = self.gate(profile, extra)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('empty coverage profile:', result.stderr)

    def test_preserves_input_profile_named_like_default_filtered_output(self):
        core = self.profile('core.out', (
            'agentflow/internal/runtime/browser.go:1.1,2.2 6 1\n'
            'agentflow/internal/runtime/browser.go:3.1,4.2 4 0\n'
        ))
        second = self.profile('core.filtered.out', (
            'agentflow/internal/runtime/browser.go:1.1,2.2 6 0\n'
            'agentflow/internal/runtime/browser.go:3.1,4.2 4 1\n'
        ))
        core_before = core.read_text()
        second_before = second.read_text()

        result = subprocess.run(
            ['bash', str(SCRIPT), '--profile', 'core.out', '--profile', 'core.filtered.out', '--min', '86'],
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=10,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertRegex(result.stdout, r'total\s+10\s+10\s+0\s+100\.0000%')
        self.assertEqual(core.read_text(), core_before)
        self.assertEqual(second.read_text(), second_before)
        self.assertTrue((self.root / 'core.filtered.filtered.out').is_file())


if __name__ == '__main__':
    unittest.main()
