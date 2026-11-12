import importlib.util
import hashlib
import io
import os
import pty
import select
import shlex
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('install_address', ROOT / 'scripts/install-address.py')
address = importlib.util.module_from_spec(spec)
spec.loader.exec_module(address)
compose_spec = importlib.util.spec_from_file_location('install_compose', ROOT / 'scripts/install-compose.py')
compose_download = importlib.util.module_from_spec(compose_spec)
compose_spec.loader.exec_module(compose_download)


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'scripts').mkdir()
        (self.root / 'migrations/seed').mkdir(parents=True)
        for name in ('install.sh', 'configure.sh', 'docker-access.sh', 'ghcr-mirror.sh', 'install-address.py', 'install-compose.py', 'install-observability-schema.sh'):
            shutil.copyfile(ROOT / 'scripts' / name, self.root / 'scripts' / name)
        shutil.copyfile(ROOT / 'start.sh', self.root / 'start.sh')
        (self.root / 'migrations/seed/001_test.sql').write_text('SELECT 1;\n')
        (self.root / 'mock').mkdir()
        self.executable('docker', '''#!/usr/bin/env bash
printf '%s %s\\n' "${MOCK_PRIVILEGED:-0}" "$*" >> "$MOCK_LOG"
if [[ "$1" == info ]]; then
  [[ "${MOCK_USER:-0}" == 0 || "${MOCK_PRIVILEGED:-0}" == 1 || "${MOCK_DIRECT:-0}" == 1 ]]
  exit
fi
if [[ "$1" == image && "$2" == inspect && "${MOCK_IMAGE_PRESENT:-0}" != 1 ]]; then exit 1; fi
if [[ "$*" == 'compose version' && "${MOCK_COMPOSE_MISSING:-0}" == 1 && ! -f "$MOCK_COMPOSE_INSTALLED" ]]; then exit 1; fi
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
        for name in ('LOOPFORGE_DOCKER_USE_SUDO', 'LOOPFORGE_DATA_DIR', 'LOOPFORGE_IMAGE', 'COMPOSE_FILE', 'LOOPFORGE_BUILD_FROM_SOURCE', 'LOOPFORGE_PULL_POLICY', 'MIRROR', 'MOCK_USER', 'MOCK_DIRECT', 'MOCK_SUDO_DENIED', 'MOCK_IMAGE_PRESENT'):
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
        result = self.install(MOCK_USER='1000', LOOPFORGE_BUILD_FROM_SOURCE='1', MOCK_EXPIRE_AFTER_BUILD='1', MOCK_SUDO_EXPIRED_FILE=str(self.root / 'sudo-expired'))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('sudo -v', result.stderr)
        self.assertNotIn('up -d --no-build', (self.root / 'docker.log').read_text())

    def pinned_image(self):
        text = (ROOT / 'compose.yaml').read_text()
        import re
        return re.search(r'image:\s*\$\{LOOPFORGE_IMAGE:-([^}]+)\}', text).group(1).strip()

    def use_compose_file(self):
        shutil.copyfile(ROOT / 'compose.yaml', self.root / 'compose.yaml')

    def test_default_install_pulls_pinned_release(self):
        self.assert_success(self.install())
        log = (self.root / 'docker.log').read_text()
        self.assertIn('pull --policy missing platform mysql', log)
        self.assertNotIn('build platform', log)
        self.assertNotIn('ghcr.nju.edu.cn', log)
        self.assertIn("LOOPFORGE_IMAGE=''", (self.root / '.env').read_text())

    def test_mirror_host_replaces_ghcr_and_tags_the_pinned_name(self):
        self.use_compose_file()
        image = self.pinned_image()
        for mirror_env, host in (('GHCR.NJU.EDU.CN', 'ghcr.nju.edu.cn'), ('mirror.example.test:5000', 'mirror.example.test:5000')):
            with self.subTest(host=host):
                if (self.root / 'docker.log').exists():
                    (self.root / 'docker.log').unlink()
                mirror = host + '/' + image[len('ghcr.io/'):]
                self.assert_success(self.install(MIRROR=mirror_env))
                log = (self.root / 'docker.log').read_text()
                self.assertLess(log.index(f'pull {mirror}'), log.index(f'tag {mirror} {image}'))
                self.assertLess(log.index(f'tag {mirror} {image}'), log.index('pull --policy missing platform mysql'))
                self.assertNotIn('MIRROR=', (self.root / '.env').read_text())

    def test_mirror_keeps_a_local_image(self):
        self.use_compose_file()
        result = self.install(MIRROR='ghcr.nju.edu.cn', MOCK_IMAGE_PRESENT='1')
        self.assert_success(result)
        log = (self.root / 'docker.log').read_text()
        self.assertNotIn('ghcr.nju.edu.cn', log)
        self.assertIn('pull --policy missing platform mysql', log)
        self.assertIn('Using local image', result.stdout)

    def test_mirror_always_refreshes_from_the_configured_host(self):
        self.use_compose_file()
        self.assert_success(self.install(MIRROR='mirror.example.test', LOOPFORGE_PULL_POLICY='always', MOCK_IMAGE_PRESENT='1'))
        log = (self.root / 'docker.log').read_text()
        image = self.pinned_image()
        mirror = 'mirror.example.test/' + image[len('ghcr.io/'):]
        self.assertIn(f'pull {mirror}', log)
        self.assertIn('pull --policy missing platform', log)
        self.assertIn('pull --policy always mysql', log)
        self.assertNotIn('pull --policy always platform', log)

    def test_upstream_mirror_host_keeps_the_direct_pull(self):
        self.use_compose_file()
        self.assert_success(self.install(MIRROR='ghcr.io'))
        log = (self.root / 'docker.log').read_text()
        self.assertIn('pull --policy missing platform mysql', log)
        self.assertNotIn(' tag ', log)

    def test_invalid_mirror_fails_before_pull(self):
        self.use_compose_file()
        for mirror in ('NJU', 'https://ghcr.nju.edu.cn', 'ghcr.nju.edu.cn/tencent'):
            with self.subTest(mirror=mirror):
                if (self.root / 'docker.log').exists():
                    (self.root / 'docker.log').unlink()
                result = self.install(MIRROR=mirror)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('ghcr.nju.edu.cn', result.stderr)
                self.assertNotIn('pull --policy', (self.root / 'docker.log').read_text())

    def test_start_pull_without_mirror_is_unchanged(self):
        self.assert_success(self.install())
        (self.root / 'docker.log').unlink()
        result = subprocess.run(['bash', 'start.sh', 'pull'], cwd=self.root, env=self.environ, text=True, capture_output=True, stdin=subprocess.DEVNULL, timeout=15)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        log = (self.root / 'docker.log').read_text()
        self.assertIn('pull --ignore-buildable --policy always', log)
        self.assertNotIn('ghcr.nju.edu.cn', log)

    def test_start_pull_with_nju_mirror_does_not_contact_ghcr_for_platform(self):
        self.use_compose_file()
        self.assert_success(self.install())
        (self.root / 'docker.log').unlink()
        result = subprocess.run(['bash', 'start.sh', 'pull'], cwd=self.root, env=dict(self.environ, MIRROR='ghcr.nju.edu.cn'), text=True, capture_output=True, stdin=subprocess.DEVNULL, timeout=15)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        image = self.pinned_image()
        mirror = 'ghcr.nju.edu.cn/' + image[len('ghcr.io/'):]
        log = (self.root / 'docker.log').read_text()
        self.assertIn(f'pull {mirror}', log)
        self.assertIn(f'tag {mirror} {image}', log)
        self.assertIn('pull --policy always mysql', log)
        self.assertNotIn('pull --ignore-buildable --policy always', log)

    def test_source_build_ignores_nju_mirror(self):
        self.use_compose_file()
        self.assert_success(self.install(LOOPFORGE_BUILD_FROM_SOURCE='1', MIRROR='ghcr.nju.edu.cn'))
        log = (self.root / 'docker.log').read_text()
        self.assertIn('build platform', log)
        self.assertNotIn('ghcr.nju.edu.cn', log)

    def test_source_build_persists_dev_compose_file(self):
        self.assert_success(self.install(LOOPFORGE_BUILD_FROM_SOURCE='1'))
        self.assertIn('build platform', (self.root / 'docker.log').read_text())
        config = (self.root / '.env').read_text()
        self.assertEqual(config.count('COMPOSE_FILE='), 1)
        self.assertIn('COMPOSE_FILE=compose.yaml:compose.dev.yaml', config)
        (self.root / 'docker.log').unlink()
        self.assert_success(self.install())
        self.assertIn('build platform', (self.root / 'docker.log').read_text())

    def test_start_switches_source_build_back_to_release(self):
        self.assert_success(self.install(LOOPFORGE_BUILD_FROM_SOURCE='1'))
        (self.root / 'docker.log').unlink()
        result = subprocess.run(['bash', 'start.sh', 'release'], cwd=self.root, env=self.environ, text=True, capture_output=True, stdin=subprocess.DEVNULL, timeout=15)
        self.assert_success(result)
        self.assertNotIn('COMPOSE_FILE=', (self.root / '.env').read_text())
        log = (self.root / 'docker.log').read_text()
        self.assertIn('pull --policy missing platform mysql', log)
        self.assertNotIn('build platform', log)

    def test_start_refuses_local_build(self):
        result = subprocess.run(['bash', 'start.sh', 'build'], cwd=self.root, env=self.environ, text=True, capture_output=True, stdin=subprocess.DEVNULL, timeout=15)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('./start.sh dev', result.stderr)

    def test_prebuilt_image_skips_build(self):
        self.assert_success(self.install(LOOPFORGE_IMAGE='registry.example.test/loopforge:tested'))
        log = (self.root / 'docker.log').read_text()
        self.assertIn('pull --policy missing platform mysql', log)
        self.assertNotIn('build platform', log)
        self.assertIn('--no-build --wait --wait-timeout 180', log)

    def test_missing_python_and_curl_are_installed_with_supported_package_managers(self):
        state = self.root / 'packages'
        state.mkdir()
        shell_env = self.root / 'package-env.sh'
        shell_env.write_text('''command() {
  if [[ "$1" == -v ]]; then
    case "$2" in
      python3|curl) [[ -e "$MOCK_PACKAGE_STATE/$2" ]] || return 1 ;;
      apt-get|dnf|yum) [[ "$2" == "$MOCK_PACKAGE_MANAGER" ]] || return 1 ;;
    esac
  fi
  builtin command "$@"
}
''')
        for manager in ('apt-get', 'dnf', 'yum'):
            self.executable(manager, '''#!/usr/bin/env bash
printf 'package-manager %s %s\\n' "${0##*/}" "$*" >> "$MOCK_LOG"
printf 'package-environment %s %s\\n' "${DEBIAN_FRONTEND:-}" "${NEEDRESTART_MODE:-}" >> "$MOCK_LOG"
if [[ "$*" == *'install'* ]]; then touch "$MOCK_PACKAGE_STATE/python3" "$MOCK_PACKAGE_STATE/curl"; fi
''')
        for manager in ('apt-get', 'dnf', 'yum'):
            with self.subTest(manager=manager):
                for item in state.iterdir():
                    item.unlink()
                self.assert_success(self.install(BASH_ENV=str(shell_env), MOCK_PACKAGE_STATE=str(state), MOCK_PACKAGE_MANAGER=manager, LOOPFORGE_YES='1'))
                self.assertTrue((state / 'python3').exists())
                self.assertTrue((state / 'curl').exists())
                log = (self.root / 'docker.log').read_text()
                self.assertIn('install -y python3 curl ca-certificates', log)
                if manager == 'apt-get':
                    self.assertIn('package-environment noninteractive a', log)
                    self.assertIn('-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold', log)

    def test_unusable_macos_python_is_replaced_before_configuring(self):
        ready = self.root / 'python-ready'
        self.executable('python3', '#!/usr/bin/env bash\n[[ -f "$MOCK_PYTHON_READY" ]] || exit 1\nexec ' + shlex.quote(sys.executable) + ' "$@"\n')
        self.executable('uname', '#!/usr/bin/env bash\nif [[ "$1" == -s ]]; then echo Darwin; else echo x86_64; fi\n')
        self.executable('brew', '#!/usr/bin/env bash\nprintf "brew %s\\n" "$*" >> "$MOCK_LOG"\ntouch "$MOCK_PYTHON_READY"\n')
        shell_env = self.root / 'macos-env.sh'
        shell_env.write_text('''command() {
  if [[ "$1" == -v && "$2" =~ ^(apt-get|dnf|yum)$ ]]; then return 1; fi
  builtin command "$@"
}
''')
        self.assert_success(self.install(BASH_ENV=str(shell_env), MOCK_PYTHON_READY=str(ready), LOOPFORGE_YES='1'))
        self.assertIn('brew install python3', (self.root / 'docker.log').read_text())

    def mock_compose_install(self, fail_download=False):
        # Exercise the shell wiring without writing a plugin to the host.
        (self.root / 'scripts/install-compose.py').write_text(
            "raise SystemExit('simulated download failure')\n" if fail_download else
            "import pathlib,sys; pathlib.Path(sys.argv[1]).write_text('test plugin')\n")
        self.executable('install', '''#!/usr/bin/env bash
printf 'plugin-install %s\\n' "$*" >> "$MOCK_LOG"
if [[ "${MOCK_PLUGIN_FAIL_OPERATION:-}" == "$1" ]]; then exit 1; fi
if [[ "${!#}" == */docker-compose ]]; then touch "$MOCK_COMPOSE_INSTALLED"; fi
''')
        home = self.root / 'home'
        home.mkdir()
        return dict(HOME=str(home), MOCK_COMPOSE_MISSING='1', MOCK_COMPOSE_INSTALLED=str(self.root / 'compose-installed'), LOOPFORGE_YES='1')

    def test_missing_compose_is_installed_where_root_and_sudo_docker_find_it(self):
        values = self.mock_compose_install()
        self.assert_success(self.install(**values, MOCK_USER='1000'))
        self.assertIn('plugin-install -m 0755', (self.root / 'docker.log').read_text())
        self.assertIn('/usr/local/lib/docker/cli-plugins/docker-compose', (self.root / 'docker.log').read_text())

    def test_missing_compose_uses_user_config_with_direct_docker_access(self):
        values = self.mock_compose_install()
        config = self.root / 'custom-docker-config'
        self.assert_success(self.install(**values, MOCK_USER='1000', MOCK_DIRECT='1', MOCK_SUDO_DENIED='1', DOCKER_CONFIG=str(config)))
        self.assertIn(str(config / 'cli-plugins/docker-compose'), (self.root / 'docker.log').read_text())

    def test_failed_compose_download_stops_before_creating_deployment_config(self):
        temporary = self.root / 'plugin temporary'
        temporary.mkdir()
        result = self.install(**self.mock_compose_install(fail_download=True), TMPDIR=str(temporary))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Docker Compose download failed', result.stderr)
        self.assertFalse((self.root / '.env').exists())
        self.assertNotIn('pull --policy', (self.root / 'docker.log').read_text())
        self.assertEqual(list(temporary.iterdir()), [])

    def test_failed_compose_copy_cleans_download_and_reports_plugin_directory(self):
        values = self.mock_compose_install()
        temporary = self.root / 'plugin temporary'
        temporary.mkdir()
        for direct in ('0', '1'):
            for operation in ('-d', '-m'):
                with self.subTest(direct=direct, operation=operation):
                    result = self.install(**values, TMPDIR=str(temporary), MOCK_USER='1000', MOCK_DIRECT=direct, MOCK_PLUGIN_FAIL_OPERATION=operation)
                    self.assertNotEqual(result.returncode, 0)
                    directory = str(Path(values['HOME']) / '.docker/cli-plugins') if direct == '1' else '/usr/local/lib/docker/cli-plugins'
                    self.assertIn('Could not install Docker Compose in ' + directory, result.stderr)
                    self.assertEqual(list(temporary.iterdir()), [])
                    self.assertFalse((self.root / '.env').exists())

    def test_declining_compose_install_does_not_start_deployment(self):
        values = self.mock_compose_install()
        values['LOOPFORGE_YES'] = '0'
        result = self.install(**values)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Docker Compose is required', result.stderr)
        self.assertFalse((self.root / '.env').exists())

    def test_invalid_public_url_fails_before_build(self):
        result = self.install(LOOPFORGE_PUBLIC_URL='javascript:alert(1)')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('up -d', (self.root / 'docker.log').read_text())

    def test_invalid_public_port_fails_before_build(self):
        result = self.install(LOOPFORGE_PUBLIC_URL='https://team.example.test:invalid')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('up -d', (self.root / 'docker.log').read_text())

    def test_public_url_with_unsupported_subpath_fails_before_build(self):
        result = self.install(LOOPFORGE_PUBLIC_URL='https://team.example.test/loopforge')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('up -d', (self.root / 'docker.log').read_text())

    def test_configuration_quotes_paths_without_shell_execution(self):
        data = str(self.root / 'spaces $(touch should-not-exist)')
        self.assert_success(self.install(LOOPFORGE_DATA_DIR=data))
        self.assertTrue(Path(data, 'home').is_dir())
        self.assertFalse((self.root / 'should-not-exist').exists())

    def test_install_without_public_url_binds_all_interfaces_without_prompting(self):
        self.environ.pop('LOOPFORGE_BIND')
        result = self.install(LOOPFORGE_PUBLIC_URL='')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn('Team URL', result.stdout)
        config = (self.root / '.env').read_text()
        self.assertIn('LOOPFORGE_BIND=0.0.0.0', config)
        self.assertIn("LOOPFORGE_PUBLIC_URL=''", config)

    def test_existing_empty_url_can_be_set_without_rotating_secrets(self):
        self.assert_success(self.install())
        config = self.root / '.env'
        original = config.read_text()
        config.write_text(original.replace('https://team.example.test', "''"))
        self.assert_success(self.install())
        self.assertEqual(config.read_text(), original)

    def test_team_url_rejects_loopback_and_listening_addresses(self):
        for url in ('http://127.0.0.1:8090', 'http://localhost:8090', 'http://0.0.0.0:8090', 'http://[::]:8090', 'http://169.254.1.2:8090'):
            with self.subTest(url=url):
                result = self.install(LOOPFORGE_PUBLIC_URL=url)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('up -d', (self.root / 'docker.log').read_text())

class AddressTests(unittest.TestCase):
    def test_public_url_and_loopback_proxy(self):
        urls, note = address.access_addresses('8090', {'LOOPFORGE_BIND': '127.0.0.1', 'LOOPFORGE_PUBLIC_URL': 'https://team.example.test/'})
        self.assertEqual(urls, [('Access', 'https://team.example.test')])
        self.assertIn('reverse proxy', note)

    def test_ipv6_and_local_mode(self):
        with patch.object(address, 'container_host', return_value=False):
            self.assertEqual(address.access_addresses('8090', {'LOOPFORGE_BIND': '2001:db8::1'})[0], [('LAN', 'http://[2001:db8::1]:8090')])
            urls, note = address.access_addresses('8090', {'LOOPFORGE_BIND': '127.0.0.1'})
        self.assertEqual(urls, [('This machine', 'http://127.0.0.1:8090')])
        self.assertIn('Local access only', note)

    def interfaces(self, *hosts):
        return patch.object(address.socket, 'gethostbyname_ex', return_value=('host', [], ['127.0.0.1', *hosts]))

    def test_private_address_is_labelled_lan_without_a_detected_public_ip(self):
        with patch.object(address, 'container_host', return_value=False), patch.object(address.socket, 'socket', side_effect=OSError), self.interfaces('10.0.0.126'):
            urls, note = address.access_addresses('8090', {})
        self.assertEqual(urls, [('LAN', 'http://10.0.0.126:8090'), ('This machine', 'http://localhost:8090')])
        self.assertIn('No public IP detected', note)

    def test_detected_public_ip_comes_first(self):
        with patch.object(address, 'container_host', return_value=False), patch.object(address.socket, 'socket', side_effect=OSError), self.interfaces('10.0.0.126'):
            urls, note = address.access_addresses('8090', {}, public_ip='43.160.231.118')
        self.assertEqual(urls, [('Public', 'http://43.160.231.118:8090'), ('LAN', 'http://10.0.0.126:8090'), ('This machine', 'http://localhost:8090')])
        self.assertIn('security group', note)

    def test_metadata_answers_must_be_global_ipv4(self):
        self.assertEqual(address.global_ipv4(' 43.160.231.118\n'), '43.160.231.118')
        for answer in ('10.0.0.126', '<html>404</html>', '', '2001:4860::1'):
            self.assertEqual(address.global_ipv4(answer), '')

    def test_public_ip_detection_is_bounded(self):
        def hang(url, headers):
            threading.Event().wait(5)
        with patch.object(address, 'metadata_public_ipv4', side_effect=hang):
            started = time.monotonic()
            self.assertEqual(address.detect_public_ipv4(timeout=0.2), '')
        self.assertLess(time.monotonic() - started, 1)
        with patch.object(address, 'metadata_public_ipv4', side_effect=lambda url, headers: '43.160.231.118' if '169.254.0.23' in url else ''):
            self.assertEqual(address.detect_public_ipv4(), '43.160.231.118')

    def test_container_does_not_publish_its_bridge_address(self):
        with patch.object(address, 'container_host', return_value=True):
            urls, note = address.access_addresses('8090', {})
        self.assertEqual(urls, [])
        self.assertIn('published port', note)


class ComposeDownloadTests(unittest.TestCase):
    def test_pinned_download_verifies_checksum_for_both_architectures(self):
        payload = b'verified compose fixture'
        checksum = hashlib.sha256(payload).hexdigest()
        for architecture, asset in (('amd64', 'x86_64'), ('arm64', 'aarch64')):
            with self.subTest(architecture=architecture), tempfile.TemporaryDirectory() as root:
                calls = []
                def fetch(url, timeout):
                    calls.append((url, timeout))
                    return io.BytesIO(payload)
                target = Path(root) / 'docker-compose'
                with patch.dict(compose_download.SHA256, {asset: checksum}):
                    compose_download.download(target, architecture, fetch)
                self.assertEqual(target.read_bytes(), payload)
                self.assertEqual(calls, [('https://github.com/docker/compose/releases/download/' + compose_download.VERSION + '/docker-compose-linux-' + asset, 120)])

    def test_bad_checksum_preserves_existing_plugin_and_removes_partial_file(self):
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / 'docker-compose'
            target.write_bytes(b'existing plugin')
            with self.assertRaisesRegex(ValueError, 'checksum mismatch'):
                compose_download.download(target, 'x86_64', lambda *a, **kw: io.BytesIO(b'corrupt download'))
            self.assertEqual(target.read_bytes(), b'existing plugin')
            self.assertFalse(target.with_suffix('.download').exists())

    def test_failed_transfer_removes_partial_download(self):
        class BrokenTransfer(io.BytesIO):
            def read(self, *args):
                raise OSError('connection lost')
        with tempfile.TemporaryDirectory() as root:
            target = Path(root) / 'docker-compose'
            with self.assertRaisesRegex(OSError, 'connection lost'):
                compose_download.download(target, 'aarch64', lambda *a, **kw: BrokenTransfer())
            self.assertFalse(target.exists())
            self.assertFalse(target.with_suffix('.download').exists())

    def test_unsupported_architecture_does_not_download(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaisesRegex(ValueError, 'architecture'):
                compose_download.download(Path(root) / 'docker-compose', 'unknown', lambda *a, **kw: self.fail('unexpected download'))


if __name__ == '__main__':
    unittest.main()
