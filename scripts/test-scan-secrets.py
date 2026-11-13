from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import threading
import unittest


ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ('scripts/scan-secrets.sh', 'local/scripts/scan-secrets.sh')


class ScanDownloadTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.requests = []
        self.responses = {}
        case = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                case.requests.append((self.path, self.headers.get('Accept'),
                                      self.headers.get('Authorization')))
                status, body = case.responses[self.path].pop(0)
                self.send_response(status)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(thread.join)
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        wrapper = Path(self.temp.name) / 'curl'
        wrapper.write_text('''#!/usr/bin/env python3
import os, sys
args = sys.argv[1:]
for i, arg in enumerate(args):
    if arg.startswith('https://github.com/gitleaks/gitleaks/releases/download/'):
        args[i] = os.environ['SCAN_TEST_BASE'] + '/release'
    elif arg.startswith('https://api.github.com/repos/gitleaks/gitleaks/releases/assets/'):
        args[i] = os.environ['SCAN_TEST_BASE'] + '/api'
os.execv(os.environ['SCAN_TEST_CURL'], ['curl', *args])
''')
        wrapper.chmod(0o755)
        self.env = dict(os.environ, PATH=self.temp.name + os.pathsep + os.environ['PATH'],
                        SCAN_TEST_BASE=f'http://127.0.0.1:{self.server.server_port}',
                        SCAN_TEST_CURL=shutil.which('curl'))
        self.env.pop('LOOPFORGE_GITLEAKS_TOKEN', None)

    def scan(self, script, token=None):
        self.requests.clear()
        env = self.env.copy()
        if token:
            env['LOOPFORGE_GITLEAKS_TOKEN'] = token
        return subprocess.run(['bash', str(ROOT / script)], env=env,
                              capture_output=True, text=True, timeout=20)

    def test_retries_transient_error_and_checks_primary_download_checksum(self):
        for script in SCRIPTS:
            with self.subTest(script=script):
                self.responses = {'/release': [(504, b'gateway timeout'), (200, b'invalid archive')]}
                result = self.scan(script)
                self.assertEqual([path for path, _, _ in self.requests], ['/release', '/release'])
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('FAILED', result.stdout)

    def test_uses_api_after_release_failure_and_checks_fallback_checksum(self):
        for script in SCRIPTS:
            with self.subTest(script=script):
                self.responses = {'/release': [(404, b'missing')], '/api': [(200, b'invalid archive')]}
                result = self.scan(script)
                self.assertEqual([path for path, _, _ in self.requests], ['/release', '/api'])
                self.assertEqual(self.requests[-1][1], 'application/octet-stream')
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('FAILED', result.stdout)

    def test_sends_the_token_only_to_the_api_fallback(self):
        for script in SCRIPTS:
            with self.subTest(script=script):
                self.responses = {'/release': [(404, b'missing')], '/api': [(200, b'invalid archive')]}
                result = self.scan(script, token='test-api-token')
                self.assertEqual(self.requests, [
                    ('/release', '*/*', None),
                    ('/api', 'application/octet-stream', 'Bearer test-api-token'),
                ])
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('FAILED', result.stdout)

    def test_fails_when_both_download_paths_fail(self):
        for script in SCRIPTS:
            with self.subTest(script=script):
                self.responses = {'/release': [(404, b'missing')], '/api': [(404, b'missing')]}
                result = self.scan(script)
                self.assertEqual([path for path, _, _ in self.requests], ['/release', '/api'])
                self.assertEqual(result.returncode, 22)


if __name__ == '__main__':
    unittest.main()
