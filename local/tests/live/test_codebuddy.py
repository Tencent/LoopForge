"""Opt-in real CodeBuddy executions of installed local workflow editions."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'tests/e2e'))
import harness


@unittest.skipUnless(os.environ.get('LOOPFORGE_LIVE_TESTS') == '1', 'set LOOPFORGE_LIVE_TESTS=1 to spend model credits')
class LiveCodeBuddy(unittest.TestCase):
    def run_edition(self, edition):
        binary = shutil.which('codebuddy')
        self.assertTrue(binary, 'Install CodeBuddy Code first')
        key = os.environ.get('CODEBUDDY_API_KEY', '').strip()
        if not key and os.environ.get('LOOPFORGE_TEST_KEY_FILE'):
            key = Path(os.environ['LOOPFORGE_TEST_KEY_FILE']).read_text().strip()
        self.assertTrue(key, 'Provide CODEBUDDY_API_KEY or LOOPFORGE_TEST_KEY_FILE')
        project = harness.new_project(self)
        harness.loopforge('install', 'codebuddy', '--edition', edition, '--project-root', project)
        env = dict(os.environ, CODEBUDDY_API_KEY=key)
        for name in ('CODEBUDDY_BASE_URL','ANTHROPIC_API_KEY','ANTHROPIC_AUTH_TOKEN','OPENAI_API_KEY','OPENAI_BASE_URL'):
            env.pop(name,None)
        env['CODEBUDDY_INTERNET_ENVIRONMENT'] = os.environ.get('LOOPFORGE_CODEBUDDY_ENVIRONMENT', '')
        env['CODEBUDDY_CONFIG_DIR'] = str(project / '.test-codebuddy-config')
        model = os.environ.get('LOOPFORGE_TEST_MODEL', 'deepseek-v4.1-flash')
        entry = '/start-devflow' if edition == 'classic' else '/devflow'
        prompt = entry + ' 本地小任务 live-slugify：修复 slugify.py，使 check_slugify.py 中两个现有测试通过。按 small/SOLO 流程执行并保存状态与总结。只在本项目内工作，不安装依赖、不访问外部网站、不提交 Git。执行 python3 -m unittest check_slugify 验证。'
        if edition == 'classic':
            from classic_terminal import run_classic
            final = run_classic(binary, project, env, model, prompt)
            returncode = 0 if not final['is_error'] else 1
        else:
            result = subprocess.run([
                binary, '-p', '--model', model, '--output-format', 'stream-json', '--max-turns', '40',
                '--permission-mode', 'bypassPermissions', '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}',
                '--setting-sources', 'project,local', '--no-session-persistence', prompt,
            ], cwd=project, env=env, capture_output=True, text=True, timeout=900)
            # Never include raw model output or environment values in failure artifacts.
            events = []
            for line in result.stdout.splitlines():
                try:
                    events.append(json.loads(line))
                except ValueError:
                    pass
            final = next((e for e in reversed(events) if e.get('type') == 'result'), {})
            returncode = result.returncode
        # Optional local diagnostics contain only the reply and saved workflow
        # state, never the environment or raw CLI transcript. They help tell a
        # genuine failure from an intentional request for human confirmation.
        diagnostic_dir = os.environ.get('LOOPFORGE_TEST_DIAGNOSTICS')
        if diagnostic_dir:
            target = Path(diagnostic_dir)
            target.mkdir(parents=True, exist_ok=True, mode=0o700)
            states = []
            for item in (project / 'artifacts').rglob('workflow-state.json'):
                states.append(json.loads(item.read_text()))
            payload = json.dumps({'edition': edition, 'returncode': returncode,
                                  'reply': final.get('result', ''), 'states': states}, ensure_ascii=False)
            payload = payload.replace(key, '[redacted]')
            report = target / (edition + '.json')
            fd = os.open(report, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
            with os.fdopen(fd, 'w') as stream:
                stream.write(payload)
        self.assertEqual(0, returncode, 'CodeBuddy process failed; inspect locally without publishing raw logs')
        self.assertFalse(final.get('is_error', True), 'CodeBuddy did not complete the local workflow')
        verify = subprocess.run([sys.executable, '-m', 'unittest', 'check_slugify'], cwd=project, capture_output=True, text=True)
        self.assertEqual(0, verify.returncode, verify.stderr)
        states = list((project / 'artifacts').rglob('workflow-state.json'))
        self.assertTrue(states, 'No saved workflow state from the real host')
        state = json.loads(states[0].read_text())
        statuses = [state.get('status'), state.get('workflow', {}).get('status'), state.get('summary', {}).get('status')]
        self.assertIn('completed',statuses,'Workflow did not persist completion')
        self.assertTrue(list((project / 'artifacts').rglob('*.md')), 'No delivery artifacts')
        usage = final.get('usage') or {}
        self.assertGreater(sum(usage.get(k, 0) or 0 for k in ('input_tokens', 'output_tokens', 'cache_creation_input_tokens', 'cache_read_input_tokens')), 0)
        print('Live local %s: fixture tests passed, workflow completion persisted, model=%s' % (edition,model))

    def test_classic(self):
        self.run_edition('classic')

    def test_portable(self):
        self.run_edition('portable')
