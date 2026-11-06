import importlib.util
import os
import pty
import select
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('install_address', ROOT / 'scripts/install-address.py')
address = importlib.util.module_from_spec(spec)
spec.loader.exec_module(address)


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        (self.root / 'migrations/seed').mkdir(parents=True)
        for name in ('install.sh', 'configure.sh', 'docker-access.sh', 'install-address.py', 'install-observability-schema.sh'):
            shutil.copyfile(ROOT / 'scripts' / name, self.root / 'scripts' / name)
        (self.root / 'migrations/seed/001_test.sql').write_text('SELECT 1;\n')
        (self.root / 'mock').mkdir()
        self.executable('docker', '''#!/usr/bin/env bash
printf '%s %s\\n' "${MOCK_PRIVILEGED:-0}" "$*" >> "$MOCK_LOG"
if [[ "$1" == info ]]; then
  [[ "${MOCK_USER:-0}" == 0 || "${MOCK_PRIVILEGED:-0}" == 1 || "${MOCK_DIRECT:-0}" == 1 ]]
  exit
fi
if [[ "$*" == *'ps --status running'* ]]; then echo platform; fi
if [[ "${MOCK_EXPIRE_AFTER_BUILD:-0}" == 1 && "$*" == *'build platform'* ]]; then touch "$MOCK_SUDO_EXPIRED_FILE"; fi
if [[ "$*" == *'exec -T mysql'* ]]; then
  if [[ "$*" == *'count(*) from users'* ]]; then echo 0; exit 0; fi
  query=$(cat)
  if [[ "$query" == *'information_schema.columns'* ]]; then echo 1; fi
  if [[ "$*" == *'count(*) from users'* ]]; then echo 0; fi
fi
''')
        self.executable('id', '#!/usr/bin/env bash\nif [[ "$1" == -u ]]; then echo "${MOCK_USER:-0}"; else /usr/bin/id "$@"; fi\n')
        self.executable('sudo', '''#!/usr/bin/env bash
[[ "${MOCK_SUDO_DENIED:-0}" != 1 ]] || exit 1
[[ -z "${MOCK_SUDO_EXPIRED_FILE:-}" || ! -e "$MOCK_SUDO_EXPIRED_FILE" ]] || exit 1
[[ "$1" == -n ]] && shift
export MOCK_PRIVILEGED=1
exec "$@"
''')
        self.environ = dict(os.environ, PATH=str(self.root / 'mock') + ':' + os.environ['PATH'], MOCK_LOG=str(self.root / 'docker.log'), AGENTFLOW_HTTP_PORT='18765', LOOPFORGE_PUBLIC_URL='https://team.example.test', LOOPFORGE_BIND='0.0.0.0')
        for name in ('LOOPFORGE_DOCKER_USE_SUDO', 'LOOPFORGE_DATA_DIR', 'LOOPFORGE_IMAGE', 'MOCK_USER', 'MOCK_DIRECT', 'MOCK_SUDO_DENIED'):
            self.environ.pop(name, None)

    def executable(self, name, contents):
        target = self.root / 'mock' / name
        target.write_text(contents)
        target.chmod(0o755)

    def install(self, **values):
        return subprocess.run(['bash', 'scripts/install.sh'], cwd=self.root, env=dict(self.environ, **values), text=True, capture_output=True, stdin=subprocess.DEVNULL, timeout=15)

    def assert_success(self, result):
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('https://team.example.test', result.stdout)
        self.assertNotIn('running at http://127.0.0.1', result.stdout)

    def test_root_install_and_repeat_preserve_secrets(self):
        self.assert_success(self.install())
        secrets = (self.root / '.env').read_text()
        self.assert_success(self.install())
        self.assertEqual((self.root / '.env').read_text(), secrets)
        self.assertEqual((self.root / '.env').stat().st_mode & 0o777, 0o600)

    def test_non_root_without_docker_group_uses_sudo_everywhere(self):
        self.assert_success(self.install(MOCK_USER='1000'))
        lines = (self.root / 'docker.log').read_text().splitlines()
        operations = [line for line in lines if 'compose' in line]
        self.assertTrue(operations)
        self.assertTrue(all(line.startswith('1 ') for line in operations), operations)
        self.assertTrue(any('exec -T mysql' in line for line in operations))

    def test_non_root_with_direct_access_never_uses_sudo(self):
        self.assert_success(self.install(MOCK_USER='1000', MOCK_DIRECT='1', MOCK_SUDO_DENIED='1'))
        self.assertNotIn('1 compose', (self.root / 'docker.log').read_text())

    def test_noninteractive_without_sudo_fails_immediately(self):
        result = self.install(MOCK_USER='1000', MOCK_SUDO_DENIED='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sudo -v', result.stderr)
        self.assertFalse((self.root / '.env').exists())

    def test_expired_sudo_after_build_has_actionable_error(self):
        result = self.install(MOCK_USER='1000', MOCK_EXPIRE_AFTER_BUILD='1', MOCK_SUDO_EXPIRED_FILE=str(self.root / 'sudo-expired'))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sudo -v', result.stderr)
        self.assertNotIn('up -d --no-build', (self.root / 'docker.log').read_text())

    def test_prebuilt_image_skips_build(self):
        self.assert_success(self.install(LOOPFORGE_IMAGE='registry.example.test/loopforge:tested'))
        log = (self.root / 'docker.log').read_text()
        self.assertIn('pull --policy missing platform mysql', log)
        self.assertNotIn('build platform', log)
        self.assertIn('--no-build --wait --wait-timeout 180', log)

    def test_invalid_public_url_fails_before_build(self):
        result = self.install(LOOPFORGE_PUBLIC_URL='javascript:alert(1)')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('build platform', (self.root / 'docker.log').read_text())

    def test_invalid_public_port_fails_before_build(self):
        result = self.install(LOOPFORGE_PUBLIC_URL='https://team.example.test:invalid')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('build platform', (self.root / 'docker.log').read_text())

    def test_public_url_with_unsupported_subpath_fails_before_build(self):
        result = self.install(LOOPFORGE_PUBLIC_URL='https://team.example.test/loopforge')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('build platform', (self.root / 'docker.log').read_text())

    def test_configuration_quotes_paths_without_shell_execution(self):
        data = str(self.root / 'spaces $(touch should-not-exist)')
        self.assert_success(self.install(LOOPFORGE_DATA_DIR=data))
        self.assertTrue(Path(data, 'home').is_dir())
        self.assertFalse((self.root / 'should-not-exist').exists())

    def test_interactive_install_persists_team_url(self):
        master, slave = pty.openpty()
        process = subprocess.Popen(['bash', 'scripts/install.sh'], cwd=self.root,
            env=dict(self.environ, LOOPFORGE_PUBLIC_URL='', LOOPFORGE_YES='', MOCK_USER='1000', MOCK_DIRECT='1'),
            stdin=slave, stdout=slave, stderr=slave)
        os.close(slave)
        output, answered = b'', False
        try:
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                readable, _, _ = select.select([master], [], [], 0.2)
                if readable:
                    try:
                        chunk = os.read(master, 65536)
                    except OSError:
                        break
                    if not chunk:
                        break
                    output += chunk
                    if b'Public URL (leave blank for LAN testing):' in output and not answered:
                        os.write(master, b'https://team.example.test\n')
                        answered = True
                elif process.poll() is not None:
                    break
            process.wait(timeout=2)
            self.assertEqual(process.returncode, 0, output.decode())
            self.assertTrue(answered)
            self.assertIn(b'Access: https://team.example.test', output)
            self.assertIn('LOOPFORGE_PUBLIC_URL=https://team.example.test', (self.root / '.env').read_text())
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
            os.close(master)


class AddressTests(unittest.TestCase):
    def test_public_url_and_loopback_proxy(self):
        urls, note = address.access_addresses('8090', {'LOOPFORGE_BIND': '127.0.0.1', 'LOOPFORGE_PUBLIC_URL': 'https://team.example.test/'})
        self.assertEqual(urls, ['https://team.example.test'])
        self.assertIn('reverse proxy', note)

    def test_ipv6_and_local_mode(self):
        self.assertEqual(address.access_addresses('8090', {'LOOPFORGE_BIND': '2001:db8::1'})[0], ['http://[2001:db8::1]:8090'])
        urls, note = address.access_addresses('8090', {'LOOPFORGE_BIND': '127.0.0.1'})
        self.assertEqual(urls, ['http://127.0.0.1:8090'])
        self.assertIn('Local access only', note)

    def test_cloud_address_is_not_claimed_to_be_public(self):
        with patch.object(address.socket, 'socket', side_effect=OSError), patch.object(address.socket, 'gethostbyname_ex', return_value=('host', [], ['127.0.0.1', '192.168.1.2'])):
            urls, note = address.access_addresses('8090', {})
        self.assertEqual(urls, ['http://192.168.1.2:8090'])
        self.assertIn('NAT', note)


if __name__ == '__main__':
    unittest.main()
