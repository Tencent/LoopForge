"""Exercise Windows launchers with fake Docker and Runner processes, without services."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
POWERSHELL = shutil.which(os.environ.get("LOOPFORGE_TEST_POWERSHELL", "powershell")) or shutil.which("pwsh")


def ps_quote(value):
    return "'" + str(value).replace("'", "''") + "'"


@unittest.skipUnless(os.name == "nt" and POWERSHELL, "Windows PowerShell is required")
class WindowsStartupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="loopforge startup ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "scripts").mkdir()
        for name in ("start-local.ps1", "start-local-runner.ps1"):
            shutil.copy2(ROOT / "scripts" / name, self.root / "scripts" / name)
        shutil.copy2(ROOT / "compose.yaml", self.root / "compose.yaml")
        (self.root / "migrations" / "seed").mkdir(parents=True)
        for name in ("001_run_obs_generated_columns.sql", "seed/001_public_catalog.sql", "seed/002_welcome_project.sql"):
            shutil.copy2(ROOT / "migrations" / name, self.root / "migrations" / name)
        self.environment = os.environ.copy()
        self.environment["LOOPFORGE_TEST_ROOT"] = str(self.root)
        self.environment["AGENTFLOW_HTTP_PORT"] = "9999"
        self.mock_docker = self.root / "docker.ps1"
        self.mock_docker.write_text(
            "$ErrorActionPreference='Stop'\n"
            "$items=@($args)\n"
            "$root=$env:LOOPFORGE_TEST_ROOT\n"
            "$entry=@{args=$items;http=$env:AGENTFLOW_HTTP_PORT}|ConvertTo-Json -Compress\n"
            "Add-Content -LiteralPath (Join-Path $root 'docker.jsonl') -Value $entry -Encoding UTF8\n"
            "$global:LASTEXITCODE=0\n"
            "if($items[0] -eq 'info'){if($env:LOOPFORGE_TEST_WINDOWS_ENGINE){'windows'}else{'linux'};return}\n"
            "if($items[0] -eq 'volume'){\n"
            " if($items[1] -eq 'inspect'){'/var/lib/docker/volumes/'+$items[-1]+'/_data'}\n"
            " return\n}\n"
            "if($items[0] -eq 'cp'){\n"
            " Copy-Item -LiteralPath $items[1] -Destination (Join-Path $root 'last.sql')\n"
            " Add-Content -LiteralPath (Join-Path $root 'sql-copies.txt') -Value $items[1]\n"
            " return\n}\n"
            "if($items[0] -ne 'compose'){return}\n"
            "if($items[1] -eq 'version'){'2.24.4';return}\n"
            "if($items -contains 'ps'){'abcdef0123456789';return}\n"
            "if($items -contains 'exec'){\n"
            " $sql=[IO.File]::ReadAllText((Join-Path $root 'last.sql'),[Text.Encoding]::UTF8)\n"
            " if($sql -match \"SELECT HEX\\('([^']+)'\\)\"){\n"
            "  if($env:LOOPFORGE_TEST_BAD_UTF8){'3F3F';return}\n"
            "  ([BitConverter]::ToString([Text.Encoding]::UTF8.GetBytes($Matches[1]))).Replace('-','');return\n"
            " }\n"
            " if($sql -match 'COUNT\\(\\*\\)'){ '1';return }\n"
            "}\n",
            encoding="utf-8",
        )

    def run_ps(self, command, expected=0):
        result = subprocess.run(
            [POWERSHELL, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command],
            env=self.environment, capture_output=True, text=True, errors="replace", timeout=40,
        )
        self.assertEqual(result.returncode, expected, result.stdout + result.stderr)
        return result

    def start(self, args="", expected=0):
        return self.run_ps(
            "& " + ps_quote(self.root / "scripts/start-local.ps1") + " -DockerBinary "
            + ps_quote(self.mock_docker) + " -NoBrowser " + args,
            expected,
        )

    def docker_calls(self):
        return [json.loads(line) for line in (self.root / "docker.jsonl").read_text(encoding="utf-8-sig").splitlines()]

    def test_saved_instance_isolation_and_utf8(self):
        self.start("-ProjectName loopforge-test -HttpPort 18090 -MySqlPort 43315 -Image local/test:windows")
        config = self.root / ".local/windows"
        settings = json.loads((config / "settings.json").read_text(encoding="utf-8"))
        self.assertEqual(settings["projectName"], "loopforge-test")
        self.assertEqual(settings["httpPort"], 18090)
        self.assertEqual(settings["mySqlPort"], 43315)
        env_before = (config / ".env").read_bytes()
        self.assertIn(b"AGENTFLOW_HTTP_PORT=18090", env_before)
        self.assertNotIn(b"REPLACE", env_before)
        self.assertFalse((self.root / ".env").exists())
        override = (config / "compose.yaml").read_text(encoding="utf-8")
        self.assertIn("target: '/var/lib/docker/volumes/loopforge-test-workspaces/_data'", override)
        self.assertIn("volumes: !override", override)
        self.start()
        self.assertEqual((config / ".env").read_bytes(), env_before)
        self.start("-Stop")
        calls = self.docker_calls()
        mutations = [call for call in calls if "up" in call["args"] or "stop" in call["args"]]
        self.assertEqual(len(mutations), 3)
        for call in mutations:
            self.assertIn("loopforge-test", call["args"])
            self.assertFalse(call["http"])
        self.assertTrue(any("--default-character-set=utf8mb4" in " ".join(call["args"]) for call in calls))
        for path in (self.root / "sql-copies.txt").read_text(encoding="utf-8-sig").splitlines():
            self.assertFalse(Path(path).exists(), "temporary SQL file must be removed")
        self.start("-HttpPort 18091", expected=1)
        self.assertEqual((config / ".env").read_bytes(), env_before)

    def test_bad_encoding_stops_before_seed(self):
        self.environment["LOOPFORGE_TEST_BAD_UTF8"] = "1"
        self.start(expected=1)
        calls = self.docker_calls()
        self.assertEqual(sum(call["args"][0] == "cp" for call in calls), 1)

    def test_windows_engine_is_rejected_before_mutation(self):
        self.environment["LOOPFORGE_TEST_WINDOWS_ENGINE"] = "1"
        self.start(expected=1)
        self.assertEqual(len(self.docker_calls()), 1)
        self.assertFalse((self.root / ".local").exists())

    def test_changed_saved_environment_cannot_redirect_instance(self):
        self.start("-ProjectName loopforge-test -HttpPort 18090 -MySqlPort 43315")
        config = self.root / ".local/windows/.env"
        config.write_text(config.read_text(encoding="utf-8").replace("AGENTFLOW_HTTP_PORT=18090", "AGENTFLOW_HTTP_PORT=8090"), encoding="utf-8")
        self.start(expected=1)
        self.assertEqual(sum("up" in call["args"] for call in self.docker_calls()), 1)

    def test_runner_stop_checks_pid_identity(self):
        runner_root = self.root / ".local/windows/runner"
        runner_root.mkdir(parents=True)
        record = runner_root / "process.json"
        wrapper = self.root / "runner-stop-test.ps1"
        wrapper.write_text(
            "$ErrorActionPreference='Stop'\n"
            "$started=[datetime]::UtcNow\n"
            "$runner=Join-Path $env:LOOPFORGE_TEST_ROOT 'runner.exe'\n"
            "$record=Join-Path $env:LOOPFORGE_TEST_ROOT '.local/windows/runner/process.json'\n"
            "@{pid=987654;binary=$runner;startedTicks=$started.Ticks}|ConvertTo-Json|Set-Content -LiteralPath $record\n"
            "$global:testStopped=0\n"
            "$global:testWrong=$true\n"
            "function Get-Process {param($Id,$ErrorAction);[pscustomobject]@{Id=$Id;Path=$(if($global:testWrong){'different.exe'}else{$runner});StartTime=$started}}\n"
            "function Stop-Process {param($Id);$global:testStopped++}\n"
            "$launcher=Join-Path $env:LOOPFORGE_TEST_ROOT 'scripts/start-local-runner.ps1'\n"
            "& $launcher -Stop\n"
            "if($global:testStopped -ne 0){throw 'stopped unrelated process'}\n"
            "$global:testWrong=$false\n"
            "& $launcher -Stop\n"
            "if($global:testStopped -ne 1){throw 'owned process not stopped'}\n"
            "if(Test-Path -LiteralPath $record){throw 'PID record retained'}\n",
            encoding="utf-8",
        )
        self.run_ps("& " + ps_quote(wrapper))
        self.assertFalse(record.exists())

    def test_runner_preserves_user_environment_and_saved_options(self):
        (self.root / "bin").mkdir()
        for name in ("agentflow-runner-windows-amd64.exe", "agentflow-gate-mcp-windows-amd64.exe"):
            (self.root / "bin" / name).touch()
        cli = self.root / "custom cli"
        cli.mkdir()
        wrapper = self.root / "runner-test.ps1"
        wrapper.write_text(
            "$ErrorActionPreference='Stop'\n"
            "$beforePath=$env:PATH\n"
            "$beforeHome=$env:USERPROFILE\n"
            "$env:AGENTFLOW_MANAGED_CLI_ROOT='prior-managed-root'\n"
            "function Start-Sleep {}\n"
            "function Get-Process {param($Id,$ErrorAction);return $null}\n"
            "function Start-Process {\n"
            " param($FilePath,$ArgumentList,$WorkingDirectory,$WindowStyle,[switch]$PassThru,$RedirectStandardOutput,$RedirectStandardError)\n"
            " if($WindowStyle -ne 'Hidden'){throw 'visible window'}\n"
            " if($ArgumentList -notcontains '-auto-update=false'){throw 'auto update enabled'}\n"
            " if($env:AGENTFLOW_MANAGED_CLI_ROOT){throw 'managed CLI root inherited'}\n"
            " if($env:USERPROFILE -ne $beforeHome){throw 'user home changed'}\n"
            " if($env:PATH -notlike ($env:LOOPFORGE_TEST_ROOT+'\\custom cli;*')){throw 'missing CLI path'}\n"
            " if($env:AGENTFLOW_SERVER_URL -ne 'http://127.0.0.1:18090'){throw 'incorrect server'}\n"
            " if($ArgumentList -notcontains '3'){throw 'lost concurrency'}\n"
            " $fake=[pscustomobject]@{Id=987654;StartTime=[datetime]::UtcNow;HasExited=$false}\n"
            " $fake|Add-Member -MemberType ScriptMethod -Name Refresh -Value {}\n"
            " return $fake\n}\n"
            "$script=Join-Path $env:LOOPFORGE_TEST_ROOT 'scripts/start-local-runner.ps1'\n"
            "& $script -BindCode 'test-placeholder' -ServerUrl 'http://127.0.0.1:18090' -CliDirectory (Join-Path $env:LOOPFORGE_TEST_ROOT 'custom cli') -Name 'Test runner' -Concurrency 3\n"
            "& $script\n"
            "if($env:PATH -ne $beforePath){throw 'PATH not restored'}\n"
            "if($env:AGENTFLOW_MANAGED_CLI_ROOT -ne 'prior-managed-root'){throw 'environment not restored'}\n",
            encoding="utf-8",
        )
        self.run_ps("& " + ps_quote(wrapper))
        saved = json.loads((self.root / ".local/windows/runner/config.json").read_text(encoding="utf-8"))
        self.assertEqual(saved["Concurrency"], 3)
        self.assertEqual(saved["Name"], "Test runner")
        self.assertEqual(saved["CliDirectory"], str(cli))

    def test_bound_runner_accepts_same_server_with_trailing_slash(self):
        (self.root / "bin").mkdir()
        for name in ("agentflow-runner-windows-amd64.exe", "agentflow-gate-mcp-windows-amd64.exe"):
            (self.root / "bin" / name).touch()
        wrapper = self.root / "runner-server-test.ps1"
        wrapper.write_text(
            "$ErrorActionPreference='Stop'\n"
            "$global:testLaunches=0\n"
            "function Start-Sleep {}\n"
            "function Get-Process {param($Id,$ErrorAction);return $null}\n"
            "function Start-Process {\n"
            " param($FilePath,$ArgumentList,$WorkingDirectory,$WindowStyle,[switch]$PassThru,$RedirectStandardOutput,$RedirectStandardError)\n"
            " if($env:AGENTFLOW_SERVER_URL -ne 'http://127.0.0.1:18090'){throw 'incorrect normalized server'}\n"
            " $global:testLaunches++\n"
            " $fake=[pscustomobject]@{Id=987654;StartTime=[datetime]::UtcNow;HasExited=$false}\n"
            " $fake|Add-Member -MemberType ScriptMethod -Name Refresh -Value {}\n"
            " return $fake\n}\n"
            "$launcher=Join-Path $env:LOOPFORGE_TEST_ROOT 'scripts/start-local-runner.ps1'\n"
            "& $launcher -BindCode 'test-placeholder' -ServerUrl 'http://127.0.0.1:18090'\n"
            "$state=Join-Path $env:LOOPFORGE_TEST_ROOT '.local/windows/runner/state.json'\n"
            "'{\"runnerId\":\"test-runner\"}'|Set-Content -LiteralPath $state\n"
            "& $launcher -ServerUrl 'http://127.0.0.1:18090/'\n"
            "if($global:testLaunches -ne 2){throw 'same server did not restart'}\n"
            "$rejected=$false\n"
            "try { & $launcher -ServerUrl 'http://127.0.0.1:18091/' }\n"
            "catch { if($_.Exception.Message -like '*already bound to another server*'){$rejected=$true}else{throw} }\n"
            "if(-not $rejected -or $global:testLaunches -ne 2){throw 'different server was allowed'}\n",
            encoding="utf-8",
        )
        self.run_ps("& " + ps_quote(wrapper))
        saved = json.loads((self.root / ".local/windows/runner/config.json").read_text(encoding="utf-8"))
        self.assertEqual(saved["ServerUrl"], "http://127.0.0.1:18090")


if __name__ == "__main__":
    unittest.main()
