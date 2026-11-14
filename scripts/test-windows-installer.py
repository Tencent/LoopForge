"""Exercise the standalone Windows installer with fake downloads and Runner processes."""
import ctypes
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import threading
import unittest


ROOT = Path(__file__).resolve().parents[1]
POWERSHELL = shutil.which(os.environ.get("LOOPFORGE_TEST_POWERSHELL", "powershell")) or shutil.which("pwsh")


def ps_quote(value):
    return "'" + str(value).replace("'", "''") + "'"


@unittest.skipUnless(os.name == "nt" and POWERSHELL, "Windows PowerShell is required")
class WindowsInstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="loopforge installer ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.installer = self.root / "downloaded installer.ps1"
        shutil.copy2(ROOT / "deploy/bind-cli.ps1", self.installer)
        self.environment = os.environ.copy()
        self.environment["LOOPFORGE_TEST_ROOT"] = str(self.root)
        self.environment["PROCESSOR_ARCHITEW6432"] = "AMD64"
        self.environment["LOOPFORGE_TEST_PYTHON"] = sys.executable
        self.install = self.root / "installation"
        (self.root / "custom cli").mkdir()
        self.artifacts = {}
        for name in ("agentflow-runner", "agentflow-gate-mcp"):
            binary = bytearray(128)
            struct.pack_into("<H", binary, 0, 0x5A4D)
            struct.pack_into("<I", binary, 60, 64)
            struct.pack_into("<IH", binary, 64, 0x4550, 0x8664)
            binary[80:80 + len(name)] = name.encode()
            fixture = self.root / (name + ".fixture")
            fixture.write_bytes(binary)
            self.artifacts[name] = hashlib.sha256(binary).hexdigest()
        self.mock = self.root / "mock.ps1"
        self.mock.write_text(r'''
$ErrorActionPreference='Stop'
$global:testDownloads=0
$global:testLaunches=0
$global:testStops=0
$global:testRunning=$false
$global:testWrongPID=$false
$global:testCorrupt=$false
$global:testStartFailure=$false
$global:testCaptureArguments=$false
$global:testStarted=[datetime]::UtcNow
$global:testBinary=''
$beforePath=$env:PATH
$beforeHome=$env:USERPROFILE
$beforeCodexHome=$env:CODEX_HOME
$env:AGENTFLOW_MANAGED_CLI_ROOT='prior-managed-root'
Remove-Item -LiteralPath Env:AGENTFLOW_GATE_MCP_BINARY -ErrorAction SilentlyContinue
[Environment]::SetEnvironmentVariable('AGENTFLOW_RUNNER_WORKSPACE','','Process')
$emptyExists=Test-Path -LiteralPath Env:AGENTFLOW_RUNNER_WORKSPACE
$emptyValue=[Environment]::GetEnvironmentVariable('AGENTFLOW_RUNNER_WORKSPACE','Process')
function Get-Fixture {
 param($Uri)
 if($Uri -notmatch '^https://platform\.example\.test/runners/(agentflow-runner|agentflow-gate-mcp)\?os=windows&arch=amd64(?:&metadata=1)?$'){throw 'unexpected download endpoint'}
 return Join-Path $env:LOOPFORGE_TEST_ROOT ($Matches[1]+'.fixture')
}
function Invoke-RestMethod {
 param($Uri,$Method,$MaximumRedirection,$TimeoutSec)
 if($MaximumRedirection -ne 0){throw 'redirects allowed'}
 $algorithm=[Security.Cryptography.SHA256]::Create()
 try{return @{sha256=([BitConverter]::ToString($algorithm.ComputeHash([IO.File]::ReadAllBytes((Get-Fixture $Uri))))).Replace('-','').ToLowerInvariant()}}
 finally{$algorithm.Dispose()}
}
function Invoke-WebRequest {
 param($Uri,$OutFile,[switch]$UseBasicParsing,$MaximumRedirection,$TimeoutSec)
 if(-not $UseBasicParsing -or $MaximumRedirection -ne 0){throw 'unsafe PowerShell download options'}
 $global:testDownloads++
 if($global:testCorrupt){[IO.File]::WriteAllText($OutFile,'not the downloaded executable')}
 else{Copy-Item -LiteralPath (Get-Fixture $Uri) -Destination $OutFile}
}
function Start-Sleep {}
function Get-Process {
 param($Id,$ErrorAction)
 if(-not $global:testRunning){return $null}
 return [pscustomobject]@{Id=$Id;Path=$(if($global:testWrongPID){'unrelated.exe'}else{$global:testBinary});StartTime=$global:testStarted}
}
function Stop-Process {param($Id);$global:testStops++;$global:testRunning=$false}
function Start-Process {
 param($FilePath,$ArgumentList,$WorkingDirectory,$WindowStyle,[switch]$PassThru,$RedirectStandardOutput,$RedirectStandardError)
 if($WindowStyle -ne 'Hidden'){throw 'visible window'}
 if($ArgumentList -notcontains '-auto-update=false'){throw 'auto update enabled'}
 if($env:USERPROFILE -cne $beforeHome -or $env:CODEX_HOME -cne $beforeCodexHome){throw 'CLI home changed'}
 if(Test-Path -LiteralPath Env:AGENTFLOW_MANAGED_CLI_ROOT){throw 'managed CLI root inherited'}
 if($env:AGENTFLOW_SERVER_URL -ne 'https://platform.example.test'){throw 'incorrect server'}
 if($ArgumentList -contains 'fixture-binding-code'){throw 'binding code exposed in process arguments'}
 if($global:testCaptureArguments){
   $probe=Join-Path $env:LOOPFORGE_TEST_ROOT 'capture_arguments.py'
   $probeArgs=@(('"'+$probe+'"'))+@($ArgumentList)
   $captured=Microsoft.PowerShell.Management\Start-Process -FilePath $env:LOOPFORGE_TEST_PYTHON -ArgumentList $probeArgs -WindowStyle Hidden -PassThru -Wait
   if($captured.ExitCode -ne 0){throw 'native argument capture failed'}
 }
 $global:testLaunches++
 $global:testBinary=$FilePath
 $global:testRunning=$true
 $global:testStarted=[datetime]::UtcNow
 $fake=[pscustomobject]@{Id=987654;StartTime=$global:testStarted;HasExited=$global:testStartFailure}
 $fake|Add-Member -MemberType ScriptMethod -Name Refresh -Value {}
 return $fake
}
function Assert-RestoredEnvironment {
 if($env:PATH -cne $beforePath){throw 'PATH not restored'}
 if($env:AGENTFLOW_MANAGED_CLI_ROOT -ne 'prior-managed-root'){throw 'managed root not restored'}
 if(Test-Path -LiteralPath Env:AGENTFLOW_GATE_MCP_BINARY){throw 'absent environment became empty'}
 if((Test-Path -LiteralPath Env:AGENTFLOW_RUNNER_WORKSPACE) -ne $emptyExists){throw 'empty environment presence changed'}
 if([Environment]::GetEnvironmentVariable('AGENTFLOW_RUNNER_WORKSPACE','Process') -cne $emptyValue){throw 'empty environment value changed'}
}
$installer=Join-Path $env:LOOPFORGE_TEST_ROOT 'downloaded installer.ps1'
$install=Join-Path $env:LOOPFORGE_TEST_ROOT 'installation'
$initial=@{ServerUrl='https://platform.example.test';BindCode='fixture-binding-code';InstallDirectory=$install;CliDirectory=(Join-Path $env:LOOPFORGE_TEST_ROOT 'custom cli');Name='Fixture runner';Concurrency=3}
''', encoding="utf-8")

    def run_ps(self, body):
        wrapper = self.root / "case.ps1"
        wrapper.write_text(". " + ps_quote(self.mock) + "\n" + body, encoding="utf-8")
        result = subprocess.run(
            [POWERSHELL, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(wrapper)],
            env=self.environment, capture_output=True, text=True, errors="replace", timeout=40,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("fixture-binding-code", result.stdout + result.stderr)
        return result

    def test_http_download_preserves_raw_bytes_and_powershell_encoding(self):
        payload = (ROOT / "deploy/bind-cli.ps1").read_bytes()
        self.assertTrue(payload.startswith(b"\xef\xbb\xbf"), "PowerShell 5.1 requires the UTF-8 BOM")

        class InstallerHandler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "text/plain; charset=utf-8")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def log_message(self, *_):
                pass

        downloaded = self.root / "http downloaded installer.ps1"
        wrapper = self.root / "download.ps1"
        with http.server.ThreadingHTTPServer(("127.0.0.1", 0), InstallerHandler) as server:
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            wrapper.write_text(
                "$ErrorActionPreference='Stop'\n"
                f"Invoke-WebRequest -Uri 'http://127.0.0.1:{server.server_port}/runners/install.ps1' "
                f"-OutFile {ps_quote(downloaded)} -UseBasicParsing\n"
                "$tokens=$null; $parseErrors=$null\n"
                f"[System.Management.Automation.Language.Parser]::ParseFile({ps_quote(downloaded)},"
                "[ref]$tokens,[ref]$parseErrors) | Out-Null\n"
                "if($parseErrors){throw 'Downloaded installer failed PowerShell parsing.'}\n",
                encoding="utf-8",
            )
            try:
                result = subprocess.run(
                    [POWERSHELL, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", str(wrapper)],
                    env=self.environment, capture_output=True, text=True, errors="replace", timeout=40,
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(downloaded.read_bytes(), payload)
            finally:
                server.shutdown()
                worker.join(timeout=5)

    def test_standalone_downloads_match_platform_and_keep_local_login(self):
        self.assert_standalone_installation()

    def test_standalone_installation_with_short_path_alias(self):
        get_short_path = ctypes.WinDLL("kernel32", use_last_error=True).GetShortPathNameW
        get_short_path.argtypes = [ctypes.c_wchar_p, ctypes.c_wchar_p, ctypes.c_uint32]
        get_short_path.restype = ctypes.c_uint32
        buffer = ctypes.create_unicode_buffer(32768)
        length = get_short_path(str(self.root), buffer, len(buffer))
        if not length:
            raise ctypes.WinError(ctypes.get_last_error())
        self.assertLess(length, len(buffer), "temporary directory alias exceeded the path buffer")
        alias = Path(buffer.value)
        if os.path.normcase(str(alias)) == os.path.normcase(str(self.root)):
            self.skipTest("the temporary volume does not provide an 8.3 path alias")
        # TemporaryDirectory retains its original path for cleanup.
        self.root = alias
        self.install = alias / "installation"
        self.installer = alias / "downloaded installer.ps1"
        self.mock = alias / "mock.ps1"
        self.environment["LOOPFORGE_TEST_ROOT"] = str(alias)
        self.assert_standalone_installation()

    def assert_standalone_installation(self):
        self.run_ps(r'''
& $installer @initial
Assert-RestoredEnvironment
if($global:testLaunches -ne 1 -or $global:testDownloads -ne 2){throw 'unexpected install operations'}
# 运行中重复接入不得下载、覆盖或启动第二个进程。
& $installer @initial
if($global:testLaunches -ne 1 -or $global:testDownloads -ne 2){throw 'running installation was changed'}
& $installer -ServerUrl 'https://platform.example.test/' -InstallDirectory $install -Stop
if($global:testStops -ne 1){throw 'owned runner not stopped'}
if(Test-Path (Join-Path $install 'process.json')){throw 'PID record retained'}
# 停止后重启会保留工具目录、名称、并发配置。
& $installer -InstallDirectory $install
Assert-RestoredEnvironment
if($global:testLaunches -ne 2){throw 'saved installation did not restart'}
''')
        config = json.loads((self.install / "config.json").read_text(encoding="utf-8"))
        self.assertEqual(config["ServerUrl"], "https://platform.example.test")
        self.assertEqual(config["Concurrency"], 3)
        self.assertEqual(config["Name"], "Fixture runner")
        self.assertTrue(Path(config["CliDirectory"]).samefile(self.root / "custom cli"))
        for name, field in (("agentflow-runner", "RunnerBinary"), ("agentflow-gate-mcp", "GateBinary")):
            self.assertEqual(hashlib.sha256(Path(config[field]).read_bytes()).hexdigest(), self.artifacts[name])
            self.assertTrue(Path(config[field]).resolve().is_relative_to((self.install / "bin").resolve()))
        self.assertEqual((self.install / "install.ps1").read_bytes(), self.installer.read_bytes())

    def test_bad_checksum_preserves_installed_files_and_configuration(self):
        self.run_ps(r'''
& $installer @initial -NoStart
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$global:testCorrupt=$true
$rejected=$false
try { & $installer -InstallDirectory $install } catch {if($_.Exception.Message -like '*checksum mismatch*'){$rejected=$true}else{throw}}
if(-not $rejected){throw 'bad checksum accepted'}
if([IO.File]::ReadAllText($configPath) -cne $before){throw 'failed upgrade changed configuration'}
if($global:testLaunches -ne 0){throw 'corrupt binary launched'}
if(@(Get-ChildItem -LiteralPath $install -Filter '.download-*').Count){throw 'temporary download retained'}
''')

    def test_runner_name_preserves_trailing_backslash_argument_boundary(self):
        probe = self.root / "capture_arguments.py"
        probe.write_text(
            "import json, os, pathlib, sys\n"
            "pathlib.Path(os.environ['LOOPFORGE_TEST_ROOT'], 'arguments.json').write_text(json.dumps(sys.argv[1:]))\n",
            encoding="utf-8",
        )
        self.run_ps(r'''
$global:testCaptureArguments=$true
$initial.Name='Runner with spaces\'
& $installer @initial
Assert-RestoredEnvironment
''')
        arguments = json.loads((self.root / "arguments.json").read_text(encoding="utf-8"))
        self.assertEqual(arguments, ["-auto-update=false", "-name", "Runner with spaces\\", "-concurrency", "3"])

    def test_wrong_platform_and_new_binding_do_not_reuse_saved_state(self):
        self.run_ps(r'''
& $installer @initial -NoStart
$statePath=Join-Path $install 'state.json'
'{"runnerId":"fixture-runner","sessionToken":"fixture-session"}'|Set-Content -LiteralPath $statePath
$before=[IO.File]::ReadAllText((Join-Path $install 'config.json'))
$rejected=$false
try { & $installer -ServerUrl 'https://other.example.test' -InstallDirectory $install -Stop }
catch {if($_.Exception.Message -like '*another server*'){$rejected=$true}else{throw}}
if(-not $rejected){throw 'cross-platform installation allowed'}
$rejected=$false
try { & $installer -InstallDirectory $install -BindCode 'different-code' }
catch {if($_.Exception.Message -like '*already bound*'){$rejected=$true}else{throw}}
if(-not $rejected){throw 'saved account binding silently reused'}
if($global:testDownloads -ne 2 -or $global:testLaunches -ne 0){throw 'conflicting setup performed operations'}
if([IO.File]::ReadAllText((Join-Path $install 'config.json')) -cne $before){throw 'conflicting setup changed configuration'}
if(-not(Test-Path -LiteralPath $statePath)){throw 'state removed'}
''')

    def test_stop_does_not_target_recycled_pid_and_retains_workspaces(self):
        self.run_ps(r'''
& $installer @initial
$workspace=Join-Path $install 'workspaces'
New-Item -ItemType Directory -Path $workspace|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $workspace 'user-work.txt')
$global:testWrongPID=$true
& $installer -InstallDirectory $install -Stop
if($global:testStops -ne 0){throw 'unrelated process stopped'}
$global:testWrongPID=$false
& $installer -InstallDirectory $install -Stop
if($global:testStops -ne 1){throw 'owned process not stopped'}
if(-not(Test-Path -LiteralPath (Join-Path $workspace 'user-work.txt'))){throw 'workspace removed'}
if(-not(Test-Path -LiteralPath (Join-Path $install 'config.json'))){throw 'configuration removed'}
''')

    def test_rejects_wrong_architecture_even_with_valid_checksum(self):
        fixture = self.root / "agentflow-runner.fixture"
        binary = bytearray(fixture.read_bytes())
        struct.pack_into("<H", binary, 68, 0xAA64)
        fixture.write_bytes(binary)
        self.run_ps(r'''
$rejected=$false
try { & $installer @initial } catch {if($_.Exception.Message -like '*architecture does not match*'){$rejected=$true}else{throw}}
if(-not $rejected -or $global:testLaunches -ne 0){throw 'wrong architecture accepted'}
if(Test-Path -LiteralPath (Join-Path $install 'config.json')){throw 'bad binary persisted configuration'}
''')

    def test_failed_launch_restores_environment(self):
        self.run_ps(r'''
$global:testStartFailure=$true
$rejected=$false
try { & $installer @initial } catch {if($_.Exception.Message -like '*exited during startup*'){$rejected=$true}else{throw}}
if(-not $rejected){throw 'failed runner startup accepted'}
Assert-RestoredEnvironment
''')


if __name__ == "__main__":
    unittest.main()
