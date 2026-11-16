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
        self.environment["LOOPFORGE_TEST_ARCH"] = "AMD64"
        self.environment["LOOPFORGE_TEST_PYTHON"] = sys.executable
        self.install = self.root / "installation"
        (self.root / "other user data").mkdir()
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
        for architecture, machine in (("amd64", 62), ("arm64", 183)):
            binary = bytearray(128)
            binary[:7] = b"\x7fELF\x02\x01\x01"
            struct.pack_into("<HHI", binary, 16, 2, machine, 1)
            name = "agentflow-gate-mcp-linux-" + architecture
            binary[64:64 + len(name)] = name.encode()
            (self.root / (name + ".fixture")).write_bytes(binary)
            self.artifacts[name] = hashlib.sha256(binary).hexdigest()
        self.mock = self.root / "mock.ps1"
        self.mock.write_text(r'''
$ErrorActionPreference='Stop'
$env:PROCESSOR_ARCHITEW6432=$env:LOOPFORGE_TEST_ARCH
$global:testServerUrl='https://platform.example.test'
$global:testDownloads=0
$global:testLaunches=0
$global:testStops=0
$global:testWaits=0
$global:testDeregisters=0
$global:testRunning=$false
$global:testWrongPID=$false
$global:testWrongStart=$false
$global:testCorrupt=$false
$global:testStartFailure=$false
$global:testFailLaunches=0
$global:testLaunchException=$false
$global:testDownloadFailure=$false
$global:testGateDownloadFailure=$false
$global:testLinuxDownloadFailure=$false
$global:testLinuxCorrupt=$false
$global:testStopHang=$false
$global:testDeregisterFailure=$false
$global:testCaptureArguments=$false
$global:testReadyProbes=0
$global:testSleeps=0
$global:testMockClock=$null
$global:testRefreshes=0
$global:testStateDelaySleeps=0
$global:testSuppressState=$false
$global:testRegistrationFailure=$false
$global:testProbeFailure=$false
$global:testExitAfterRefresh=0
$global:testNewSession='fixture-session'
$global:testLaunchInputs=@()
$global:testPendingRegistration=$null
$global:testRegistrationStatePath=''
$global:testLockConfigAtReady=$false
$global:testCommitLock=$null
$global:testReturnedRunnerID=''
$global:testStarted=[datetime]::UtcNow
$global:testBinary=''
$beforePath=$env:PATH
$beforeHome=$env:USERPROFILE
$beforeCodexHome=$env:CODEX_HOME
$env:AGENTFLOW_RUNNER_CONCURRENCY='7'
$beforeConcurrency=$env:AGENTFLOW_RUNNER_CONCURRENCY
$env:AGENTFLOW_MANAGED_CLI_ROOT='prior-managed-root'
Remove-Item -LiteralPath Env:AGENTFLOW_GATE_MCP_BINARY -ErrorAction SilentlyContinue
[Environment]::SetEnvironmentVariable('AGENTFLOW_RUNNER_WORKSPACE','','Process')
$emptyExists=Test-Path -LiteralPath Env:AGENTFLOW_RUNNER_WORKSPACE
$emptyValue=[Environment]::GetEnvironmentVariable('AGENTFLOW_RUNNER_WORKSPACE','Process')
function Get-Fixture {
 param($Uri)
 $architecture=$env:PROCESSOR_ARCHITEW6432.ToLowerInvariant()
 if($Uri -match ('^'+[regex]::Escape($global:testServerUrl)+'/runners/(agentflow-runner|agentflow-gate-mcp)\?os=windows&arch='+$architecture+'(?:&metadata=1)?$')){
   return Join-Path $env:LOOPFORGE_TEST_ROOT ($Matches[1]+'.fixture')
 }
 if($Uri -match ('^'+[regex]::Escape($global:testServerUrl)+'/runners/agentflow-gate-mcp\?os=linux&arch='+$architecture+'(?:&metadata=1)?$')){
   return Join-Path $env:LOOPFORGE_TEST_ROOT ('agentflow-gate-mcp-linux-'+$architecture+'.fixture')
 }
 throw ('unexpected download endpoint: '+$Uri+' (expected platform '+$global:testServerUrl+')')
}
function Invoke-RestMethod {
 param($Uri,$Method,$MaximumRedirection,$TimeoutSec,$Headers,$ContentType,$Body)
 if($MaximumRedirection -ne 0){throw 'redirects allowed'}
 if($Uri -eq ($global:testServerUrl+'/runners/tasks/claim')){
   $probe=$Body|ConvertFrom-Json
   $state=Get-Content -LiteralPath $env:AGENTFLOW_RUNNER_STATE_PATH -Raw|ConvertFrom-Json
   if($Method -ne 'Post' -or $Headers.Authorization -cne ('Bearer '+$state.sessionToken) -or $ContentType -ne 'application/json' -or -not $probe.preflight -or -not $probe.noWait -or $probe.capacity -ne 1 -or ($probe.availableRuntimes -join ',') -ne 'mock'){throw 'incorrect readiness probe'}
   $global:testReadyProbes++
   if($global:testProbeFailure){
     $global:testRunning=$false
     $failure=New-Object System.Exception('fixture session was rejected')
     $failure|Add-Member -NotePropertyName Response -NotePropertyValue ([pscustomobject]@{StatusCode=401})
     throw $failure
   }
   if($global:testLockConfigAtReady){
     $global:testCommitLock=[IO.File]::Open((Join-Path (Split-Path -Parent $env:AGENTFLOW_RUNNER_STATE_PATH) 'config.json'),[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
   }
   return @{taskAvailable=$false}
 }
 if($Uri -eq ($global:testServerUrl+'/runners/deregister')){
   if($Method -ne 'Post' -or $Headers.Authorization -cne ('Bearer '+$global:testNewSession) -or $ContentType -ne 'application/json' -or $Body -ne '{}'){throw 'incorrect deregistration request'}
   if($global:testRunning -and -not $global:testWrongPID -and -not $global:testWrongStart){throw 'deregistered before stopping runner'}
   $global:testDeregisters++
   if($global:testDeregisterFailure){throw 'fixture network failure'}
   return @{ok=$true}
 }
 $algorithm=[Security.Cryptography.SHA256]::Create()
 try{return @{sha256=([BitConverter]::ToString($algorithm.ComputeHash([IO.File]::ReadAllBytes((Get-Fixture $Uri))))).Replace('-','').ToLowerInvariant()}}
 finally{$algorithm.Dispose()}
}
function Invoke-WebRequest {
 param($Uri,$OutFile,[switch]$UseBasicParsing,$MaximumRedirection,$TimeoutSec)
 if(-not $UseBasicParsing -or $MaximumRedirection -ne 0){throw 'unsafe PowerShell download options'}
 $global:testDownloads++
 if($global:testDownloadFailure){throw 'fixture download failure'}
 if($global:testGateDownloadFailure -and $Uri -match '/agentflow-gate-mcp\?'){throw 'fixture Gate download failure'}
 if($global:testLinuxDownloadFailure -and $Uri -match '\?os=linux&'){throw 'fixture Linux download failure'}
 if($global:testCorrupt -or ($global:testLinuxCorrupt -and $Uri -match '\?os=linux&')){[IO.File]::WriteAllText($OutFile,'not the downloaded executable')}
 else{Copy-Item -LiteralPath (Get-Fixture $Uri) -Destination $OutFile}
}
function Complete-FixtureRegistration {
 if(-not $global:testPendingRegistration -or $global:testSuppressState -or $global:testRegistrationFailure){return}
 [IO.File]::WriteAllText($global:testRegistrationStatePath,($global:testPendingRegistration|ConvertTo-Json),(New-Object Text.UTF8Encoding($false)))
 $global:testPendingRegistration=$null
}
function Start-Sleep {
 param($Seconds,$Milliseconds)
 $global:testSleeps++
 if($global:testMockClock){
   $advance=if($Seconds){$Seconds}else{30}
   $global:testMockClock=$global:testMockClock.AddSeconds($advance)
 }
 if($global:testStateDelaySleeps -gt 0 -and $global:testSleeps -ge $global:testStateDelaySleeps){Complete-FixtureRegistration}
}
function Get-Date {
 if($global:testMockClock){return $global:testMockClock}
 return [DateTime]::Now
}
function Get-Process {
 param($Id,$ErrorAction)
 if(-not $global:testRunning){return $null}
 $fake=[pscustomobject]@{Id=$Id;Path=$(if($global:testWrongPID){'unrelated.exe'}else{$global:testBinary});StartTime=$(if($global:testWrongStart){$global:testStarted.AddSeconds(-1)}else{$global:testStarted});HasExited=$false}
 $fake|Add-Member -MemberType ScriptMethod -Name WaitForExit -Value {param($Milliseconds);$global:testWaits++;return -not $global:testStopHang}
 return $fake
}
function Stop-Process {param($InputObject,$ErrorAction);if(-not $InputObject){throw 'missing owned process handle'};$global:testStops++;if(-not $global:testStopHang){$global:testRunning=$false}}
function Start-Process {
 param($FilePath,$ArgumentList,$WorkingDirectory,$WindowStyle,[switch]$PassThru,$RedirectStandardOutput,$RedirectStandardError)
 if($WindowStyle -ne 'Hidden'){throw 'visible window'}
 if($ArgumentList -notcontains '-auto-update=false'){throw 'auto update enabled'}
 if($env:AGENTFLOW_RUNNER_CONCURRENCY -cne [string]$ArgumentList[-1]){throw 'saved concurrency overridden by inherited environment'}
 if($env:USERPROFILE -cne $beforeHome -or $env:CODEX_HOME -cne $beforeCodexHome){throw 'CLI home changed'}
 if(Test-Path -LiteralPath Env:AGENTFLOW_MANAGED_CLI_ROOT){throw 'managed CLI root inherited'}
 if((Get-Item -LiteralPath $env:PATH.Split([IO.Path]::PathSeparator)[0]).FullName -ine (Get-Item -LiteralPath (Join-Path $env:LOOPFORGE_TEST_ROOT 'custom cli')).FullName){throw 'custom CLI path not prepended'}
 if(-not(Test-Path -LiteralPath $env:AGENTFLOW_GATE_MCP_BINARY) -or (Split-Path -Parent $env:AGENTFLOW_GATE_MCP_BINARY) -cne (Split-Path -Parent $FilePath)){throw 'wrong Gate bundle'}
 $linuxGate=Join-Path (Split-Path -Parent $FilePath) ('agentflow-gate-mcp-linux-'+$env:PROCESSOR_ARCHITEW6432.ToLowerInvariant())
 if((Split-Path -Leaf (Split-Path -Parent $FilePath)) -match '^[a-f0-9]{16}-[a-f0-9]{16}-[a-f0-9]{16}$' -and -not(Test-Path -LiteralPath $linuxGate)){throw 'Linux Gate companion missing'}
 if($env:AGENTFLOW_SERVER_URL -ne $global:testServerUrl){throw 'incorrect server'}
 if($ArgumentList -contains 'fixture-binding-code'){throw 'binding code exposed in process arguments'}
 if($global:testCaptureArguments){
   $probe=Join-Path $env:LOOPFORGE_TEST_ROOT 'capture_arguments.py'
   $probeArgs=@(('"'+$probe+'"'))+@($ArgumentList)
   $captured=Microsoft.PowerShell.Management\Start-Process -FilePath $env:LOOPFORGE_TEST_PYTHON -ArgumentList $probeArgs -WindowStyle Hidden -PassThru -Wait
   if($captured.ExitCode -ne 0){throw 'native argument capture failed'}
 }
 $global:testLaunches++
 if($global:testLaunchException){$global:testLaunchException=$false;throw 'fixture launch exception'}
 $global:testBinary=$FilePath
 $global:testRunning=$true
 $global:testStarted=[datetime]::UtcNow
 $global:testRefreshes=0
 $failed=$global:testStartFailure -or $global:testFailLaunches -gt 0
 if($global:testFailLaunches -gt 0){$global:testFailLaunches--}
 if($failed){$global:testRunning=$false}
 $global:testRegistrationStatePath=$env:AGENTFLOW_RUNNER_STATE_PATH
 $savedRaw=if(Test-Path -LiteralPath $global:testRegistrationStatePath){[IO.File]::ReadAllText($global:testRegistrationStatePath)}else{''}
 $global:testLaunchInputs+=@{state=$savedRaw;bootstrap=$env:AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN}
 $savedState=if($savedRaw){$savedRaw|ConvertFrom-Json}else{$null}
 $runnerId=if($savedState -and $savedState.PSObject.Properties['runnerId'] -and $savedState.runnerId){$savedState.runnerId}else{'fixture-runner'}
 $hasSession=$savedState -and $savedState.PSObject.Properties['sessionToken'] -and $savedState.sessionToken
 if(-not $failed -and -not $hasSession){
   if($global:testReturnedRunnerID){$runnerId=$global:testReturnedRunnerID}
   $global:testPendingRegistration=@{runnerId=$runnerId;sessionToken=$global:testNewSession;expiresAt='2099-01-01T00:00:00Z'}
   if($global:testStateDelaySleeps -eq 0){Complete-FixtureRegistration}
 }
 if($global:testRegistrationFailure -and $global:testExitAfterRefresh -eq 0){$global:testExitAfterRefresh=3}
 $fake=[pscustomobject]@{Id=987654;Path=$FilePath;StartTime=$global:testStarted;HasExited=$failed}
 $fake|Add-Member -MemberType ScriptMethod -Name Refresh -Value {$global:testRefreshes++;if($global:testExitAfterRefresh -gt 0 -and $global:testRefreshes -ge $global:testExitAfterRefresh){$global:testRunning=$false};if(-not $global:testRunning){$this.HasExited=$true}}
 $fake|Add-Member -MemberType ScriptMethod -Name WaitForExit -Value {param($Milliseconds);$global:testWaits++;return -not $global:testStopHang}
 return $fake
}
function Assert-RestoredEnvironment {
 if($env:PATH -cne $beforePath){throw 'PATH not restored'}
 if($env:USERPROFILE -cne $beforeHome -or $env:CODEX_HOME -cne $beforeCodexHome){throw 'local login environment changed'}
 if($env:AGENTFLOW_RUNNER_CONCURRENCY -cne $beforeConcurrency){throw 'concurrency environment not restored'}
 if($env:AGENTFLOW_MANAGED_CLI_ROOT -ne 'prior-managed-root'){throw 'managed root not restored'}
 if(Test-Path -LiteralPath Env:AGENTFLOW_GATE_MCP_BINARY){throw 'absent environment became empty'}
 if((Test-Path -LiteralPath Env:AGENTFLOW_RUNNER_WORKSPACE) -ne $emptyExists){throw 'empty environment presence changed'}
 if([Environment]::GetEnvironmentVariable('AGENTFLOW_RUNNER_WORKSPACE','Process') -cne $emptyValue){throw 'empty environment value changed'}
}
$installer=Join-Path $env:LOOPFORGE_TEST_ROOT 'downloaded installer.ps1'
$install=Join-Path $env:LOOPFORGE_TEST_ROOT 'installation'
$initial=@{ServerUrl='https://platform.example.test';BindCode='fixture-binding-code';InstallDirectory=$install;CliDirectory=(Join-Path $env:LOOPFORGE_TEST_ROOT 'custom cli');Name='Fixture runner';Concurrency=3}
function Write-FixtureState {
 '{"runnerId":"fixture-runner","sessionToken":"fixture-session","expiresAt":"2099-01-01T00:00:00Z"}'|Set-Content -LiteralPath (Join-Path $install 'state.json')
}
function Set-NewArtifact {
 param([string]$Name='agentflow-runner')
 $path=Join-Path $env:LOOPFORGE_TEST_ROOT ($Name+'.fixture')
 $bytes=[IO.File]::ReadAllBytes($path)
 $bytes[127]++
 [IO.File]::WriteAllBytes($path,$bytes)
}
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
        self.assertNotIn("fixture-session", result.stdout + result.stderr)
        self.assertNotIn("fixture-renewed-binding-code", result.stdout + result.stderr)
        self.assertNotIn("fixture-rebound-session", result.stdout + result.stderr)
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

    def test_http_remote_platform_is_rejected_before_downloads_or_installation(self):
        self.run_ps(r'''
foreach($url in @('http://platform.example.test','http://192.168.1.10:8080/api','http://10.0.0.1','http://172.16.0.1','http://localhost.example.test','http://127.0.0.1.example.test','http://[::2]')){
 $initial.ServerUrl=$url
 $rejected=$false
 try { & $installer @initial -NoStart }
 catch { if($_.Exception.Message -notlike '*requires HTTPS*'){throw};$rejected=$true }
 if(-not $rejected){throw 'remote HTTP platform was accepted'}
 if(Test-Path -LiteralPath $install){throw 'insecure URL validation created installation files'}
}
if($global:testDownloads -ne 0 -or $global:testLaunches -ne 0){throw 'insecure URL reached downloads or process startup'}
''')

    def test_https_and_http_loopback_platforms_install_with_matching_endpoints(self):
        self.run_ps(r'''
$index=0
foreach($url in @('https://platform.example.test','https://192.168.1.10:8443/api','http://localhost:8080/api','http://127.0.0.1:8080','http://127.42.1.2:8080/api','http://[::1]:8080/api')){
 $index++
 $global:testServerUrl=([Uri]$url).AbsoluteUri.TrimEnd('/')
 $initial.ServerUrl=$url
 $initial.InstallDirectory=Join-Path $env:LOOPFORGE_TEST_ROOT ('allowed-'+$index)
 & $installer @initial -NoStart
 $config=Get-Content -LiteralPath (Join-Path $initial.InstallDirectory 'config.json') -Raw|ConvertFrom-Json
 if($config.ServerUrl -cne $global:testServerUrl){throw 'accepted platform URL or API prefix changed'}
}
if($global:testDownloads -ne 18 -or $global:testLaunches -ne 0){throw 'accepted URL did not use all validated artifact endpoints'}
''')

    def test_legacy_http_configuration_allows_only_offline_stop_and_local_cleanup(self):
        self.run_ps(r'''
& $installer @initial
$configPath=Join-Path $install 'config.json'
$config=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
$config.ServerUrl='http://192.168.1.10:8080/api'
$config|ConvertTo-Json|Set-Content -LiteralPath $configPath
$original=[IO.File]::ReadAllBytes($configPath)
$workspace=$config.WorkspaceRoot
New-Item -ItemType Directory -Force -Path $workspace|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $workspace 'retained.txt')
function Invoke-RestMethod { throw 'legacy HTTP cleanup attempted a network request' }
function Invoke-WebRequest { throw 'legacy HTTP cleanup attempted a download' }
foreach($operation in @(@{},@{Uninstall=$true},@{Update=$true},@{Rebind=$true;BindCode='fixture-renewed-binding-code'})){
 $rejected=$false
 try { & $installer -InstallDirectory $install @operation }
 catch { if($_.Exception.Message -notlike '*requires HTTPS*'){throw};$rejected=$true }
 if(-not $rejected){throw 'legacy remote HTTP configuration permitted an online operation'}
 if([Convert]::ToBase64String([IO.File]::ReadAllBytes($configPath)) -cne [Convert]::ToBase64String($original)){throw 'HTTP rejection changed saved configuration'}
 if(-not $global:testRunning -or $global:testStops -ne 0){throw 'HTTP rejection stopped the existing Runner'}
}
& $installer -InstallDirectory $install -Stop
if($global:testRunning -or $global:testStops -ne 1 -or -not(Test-Path -LiteralPath $configPath)){throw 'legacy HTTP local stop failed'}
& $installer -InstallDirectory $install -Uninstall -LocalOnly
if(Test-Path -LiteralPath $configPath){throw 'legacy HTTP local uninstall retained managed configuration'}
if(-not(Test-Path -LiteralPath (Join-Path $workspace 'retained.txt'))){throw 'legacy HTTP local cleanup removed workspace'}
if($global:testDownloads -ne 3 -or $global:testLaunches -ne 1 -or $global:testDeregisters -ne 0){throw 'legacy HTTP offline cleanup performed an online operation'}
''')

    def test_explicit_legacy_http_url_only_allows_offline_cleanup_without_requests(self):
        self.run_ps(r'''
& $installer @initial
$configPath=Join-Path $install 'config.json'
$config=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
$legacyUrl='http://192.168.1.10:8080/api'
$config.ServerUrl=$legacyUrl
$config|ConvertTo-Json|Set-Content -LiteralPath $configPath
$original=[IO.File]::ReadAllBytes($configPath)
$workspace=$config.WorkspaceRoot
New-Item -ItemType Directory -Force -Path $workspace|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $workspace 'retained.txt')
$global:testOfflineRequests=0
function Invoke-RestMethod { $global:testOfflineRequests++;throw 'explicit legacy HTTP URL attempted a network request' }
function Invoke-WebRequest { $global:testOfflineRequests++;throw 'explicit legacy HTTP URL attempted a download' }
foreach($operation in @(@{},@{Uninstall=$true},@{Update=$true},@{Rebind=$true;BindCode='fixture-renewed-binding-code'})){
 $rejected=$false
 try { & $installer -ServerUrl $legacyUrl -InstallDirectory $install @operation }
 catch { if($_.Exception.Message -notlike '*requires HTTPS*'){throw};$rejected=$true }
 if(-not $rejected){throw 'explicit legacy HTTP URL permitted an online operation'}
 if([Convert]::ToBase64String([IO.File]::ReadAllBytes($configPath)) -cne [Convert]::ToBase64String($original)){throw 'HTTP rejection changed saved configuration'}
 if(-not $global:testRunning -or $global:testStops -ne 0){throw 'HTTP rejection interrupted the existing Runner'}
}
& $installer -ServerUrl $legacyUrl -InstallDirectory $install -Stop
if($global:testRunning -or $global:testStops -ne 1 -or -not(Test-Path -LiteralPath $configPath)){throw 'explicit legacy HTTP local stop failed'}
& $installer -ServerUrl $legacyUrl -InstallDirectory $install -Uninstall -LocalOnly
if(Test-Path -LiteralPath $configPath){throw 'explicit legacy HTTP local uninstall retained configuration'}
if(-not(Test-Path -LiteralPath (Join-Path $workspace 'retained.txt'))){throw 'explicit legacy HTTP cleanup removed workspace'}
if($global:testOfflineRequests -ne 0 -or $global:testDownloads -ne 3 -or $global:testLaunches -ne 1 -or $global:testDeregisters -ne 0){throw 'explicit legacy HTTP cleanup performed an online operation'}
''')

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
if($global:testLaunches -ne 1 -or $global:testDownloads -ne 3){throw 'unexpected install operations'}
# 运行中重复接入不得下载、覆盖或启动第二个进程。
& $installer @initial
if($global:testLaunches -ne 1 -or $global:testDownloads -ne 3){throw 'running installation was changed'}
& $installer -ServerUrl 'https://platform.example.test/' -InstallDirectory $install -Stop
if($global:testStops -ne 1){throw 'owned runner not stopped'}
if(Test-Path (Join-Path $install 'process.json')){throw 'PID record retained'}
# 停止后重启会保留名称、并发配置。
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
        linux_gate = Path(config["GateBinary"]).with_name("agentflow-gate-mcp-linux-amd64")
        self.assertEqual(hashlib.sha256(linux_gate.read_bytes()).hexdigest(), self.artifacts[linux_gate.name])
        self.assertEqual(Path(config["RunnerBinary"]).parent.name, "-".join(self.artifacts[name][:16] for name in ("agentflow-runner", "agentflow-gate-mcp", linux_gate.name)))
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
if($global:testDownloads -ne 3 -or $global:testLaunches -ne 0){throw 'conflicting setup performed operations'}
if([IO.File]::ReadAllText((Join-Path $install 'config.json')) -cne $before){throw 'conflicting setup changed configuration'}
if(-not(Test-Path -LiteralPath $statePath)){throw 'state removed'}
''')

    def test_rebind_replaces_expired_and_revoked_sessions_without_resetting_installation(self):
        for expiry in ("2000-01-01T00:00:00Z", "2099-01-01T00:00:00Z"):
            with self.subTest(expiry=expiry):
                self.run_ps(r'''
$install=Join-Path $env:LOOPFORGE_TEST_ROOT 'INSTALL_NAME'
$initial.InstallDirectory=$install
$initial.PathEntries=@((Join-Path $env:LOOPFORGE_TEST_ROOT 'other user data'))
& $installer @initial -NoStart
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
$before=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
'{ "runnerId": "fixture-runner", "sessionToken": "old-expired-or-revoked-token", "expiresAt": "OLD_EXPIRY" }'|Set-Content -LiteralPath $statePath
$workspace=$before.WorkspaceRoot
New-Item -ItemType Directory -Path $workspace|Out-Null
'keep-work'|Set-Content -LiteralPath (Join-Path $workspace 'work.txt')
$login=Join-Path $before.CliDirectory 'fixture-login.json'
'keep-login'|Set-Content -LiteralPath $login
$global:testNewSession='fixture-rebound-session'
& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'
$after=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
$state=Get-Content -LiteralPath $statePath -Raw|ConvertFrom-Json
if($state.runnerId -cne 'fixture-runner' -or $state.sessionToken -cne $global:testNewSession){throw 'rebind reset identity or retained revoked token'}
$launched=$global:testLaunchInputs[-1]
$startState=$launched.state|ConvertFrom-Json
if($startState.runnerId -cne 'fixture-runner' -or $startState.sessionToken){throw 'rebind did not force registration with the original identity'}
if($launched.bootstrap -cne 'fixture-renewed-binding-code' -or $after.bootstrapToken -cne 'fixture-renewed-binding-code'){throw 'rebind did not use and save the fresh code'}
foreach($key in @('ServerUrl','WorkspaceRoot','CliDirectory','Name','Concurrency','RunnerBinary','GateBinary')){if($after.$key -cne $before.$key){throw 'rebind changed saved installation options'}}
if(($after.PathEntries -join ';') -cne ($before.PathEntries -join ';')){throw 'rebind changed tool directories'}
if((Get-Content -LiteralPath (Join-Path $workspace 'work.txt') -Raw).Trim() -cne 'keep-work' -or (Get-Content -LiteralPath $login -Raw).Trim() -cne 'keep-login'){throw 'rebind changed work or CLI login'}
if($global:testLaunches -ne 1 -or $global:testDownloads -ne 6 -or $global:testReadyProbes -ne 1 -or -not $global:testRunning){throw 'rebind did not validate bundle and confirm a running session'}
Assert-RestoredEnvironment
'''.replace("INSTALL_NAME", "expired" if expiry.startswith("2000") else "revoked").replace("OLD_EXPIRY", expiry))

    def test_rebind_requires_saved_identity_fresh_code_and_exclusive_options(self):
        self.run_ps(r'''
$rejected=$false
try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{$rejected=$true}
if(-not $rejected -or (Test-Path -LiteralPath $install)){throw 'missing rebind installation was created'}
& $installer @initial -NoStart
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
$before=[IO.File]::ReadAllText($configPath)
$stateBefore=[Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath))
foreach($arguments in @(
 @{Rebind=$true},@{Rebind=$true;BindCode='fixture-renewed-binding-code';NoStart=$true},
 @{Rebind=$true;BindCode='fixture-renewed-binding-code';Stop=$true},@{Rebind=$true;BindCode='fixture-renewed-binding-code';Update=$true},
 @{Rebind=$true;BindCode='fixture-renewed-binding-code';Uninstall=$true},@{Rebind=$true;BindCode='fixture-renewed-binding-code';Name='other'},
 @{Rebind=$true;BindCode='fixture-renewed-binding-code';Concurrency=2},@{Rebind=$true;BindCode='fixture-renewed-binding-code';WorkspaceRoot='other'},
 @{Rebind=$true;BindCode='fixture-renewed-binding-code';CliDirectory='other'},@{Rebind=$true;BindCode='fixture-renewed-binding-code';PathEntries=@('other')},
 @{Rebind=$true;BindCode='fixture-renewed-binding-code';ServerUrl='https://other.example.test'})){
 $arguments.InstallDirectory=$install
 $rejected=$false
 try{& $installer @arguments}catch{$rejected=$true}
 if(-not $rejected -or $global:testDownloads -ne 3 -or $global:testLaunches -ne 0){throw 'invalid rebind performed operations'}
 if([IO.File]::ReadAllText($configPath) -cne $before -or [Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath)) -cne $stateBefore){throw 'invalid rebind changed saved data'}
}
'{"sessionToken":"old-token"}'|Set-Content -LiteralPath $statePath
$rejected=$false
try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{$rejected=$true}
if(-not $rejected -or $global:testDownloads -ne 3 -or $global:testLaunches -ne 0){throw 'rebind without saved identity was accepted'}
''')

    def test_rebind_rejects_running_machine_before_downloading_or_stopping(self):
        self.run_ps(r'''
& $installer @initial
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
$before=[IO.File]::ReadAllText($configPath)
$stateBefore=[Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath))
$rejected=$false
try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{$rejected=$true}
if(-not $rejected -or $global:testDownloads -ne 3 -or $global:testLaunches -ne 1 -or $global:testStops -ne 0 -or -not $global:testRunning){throw 'running rebind interrupted the machine'}
if([IO.File]::ReadAllText($configPath) -cne $before -or [Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath)) -cne $stateBefore){throw 'running rebind changed credentials'}
''')

    def test_rebind_validates_complete_bundle_before_touching_saved_session(self):
        self.run_ps(r'''
& $installer @initial -NoStart
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
$before=[IO.File]::ReadAllText($configPath)
$stateBefore=[Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath))
foreach($kind in @('network','checksum','linux')){
 $global:testDownloadFailure=$kind -eq 'network'
 $global:testCorrupt=$kind -eq 'checksum'
 $global:testLinuxDownloadFailure=$kind -eq 'linux'
 $rejected=$false
 try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{$rejected=$true}
 if(-not $rejected -or $global:testLaunches -ne 0 -or $global:testStops -ne 0){throw 'invalid rebind bundle started a process'}
 if([IO.File]::ReadAllText($configPath) -cne $before -or [Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath)) -cne $stateBefore){throw 'invalid rebind bundle changed state or configuration'}
 if(@(Get-ChildItem -LiteralPath $install -Filter '.download-*').Count){throw 'failed rebind retained download files'}
}
''')

    def test_rebind_registration_and_spawn_failures_restore_original_raw_state(self):
        for failure in ("$global:testRegistrationFailure=$true", "$global:testLaunchException=$true", "$global:testReturnedRunnerID='different-runner'"):
            with self.subTest(failure=failure):
                self.run_ps(r'''
$install=Join-Path $env:LOOPFORGE_TEST_ROOT 'INSTALL_NAME'
$initial.InstallDirectory=$install
& $installer @initial -NoStart
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
'{ "runnerId":"fixture-runner", "sessionToken":"old-token", "expiresAt":"2000-01-01T00:00:00Z", "extra":"preserve bytes" }'|Set-Content -LiteralPath $statePath -Encoding Unicode
$before=[IO.File]::ReadAllText($configPath)
$stateBefore=[Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath))
$global:testNewSession='fixture-rebound-session'
FAILURE_INJECTION
$rejected=$false
try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{$rejected=$true}
if(-not $rejected -or $global:testRunning -or (Test-Path -LiteralPath (Join-Path $install 'process.json'))){throw 'failed rebind retained a candidate process or accepted failure'}
if([IO.File]::ReadAllText($configPath) -cne $before -or [Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath)) -cne $stateBefore){throw 'failed rebind did not restore exact original state and configuration'}
Assert-RestoredEnvironment
'''.replace("INSTALL_NAME", "failure-" + str(len(failure))).replace("FAILURE_INJECTION", failure))

    def test_rebind_rejected_replacement_session_stops_candidate_and_retains_recovery_state(self):
        self.run_ps(r'''
& $installer @initial -NoStart
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$global:testNewSession='fixture-rebound-session'
$global:testProbeFailure=$true
$rejected=$false
try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{if($_.Exception.Message -like '*replacement session*'){$rejected=$true}else{throw}}
$state=Get-Content -LiteralPath (Join-Path $install 'state.json') -Raw|ConvertFrom-Json
if(-not $rejected -or $global:testRunning -or (Test-Path -LiteralPath (Join-Path $install 'process.json'))){throw 'rejected replacement session left a candidate running'}
if($state.runnerId -cne 'fixture-runner' -or $state.sessionToken -cne 'fixture-rebound-session' -or [IO.File]::ReadAllText($configPath) -cne $before){throw 'replacement-session failure destroyed recovery data'}
Assert-RestoredEnvironment
''')

    def test_rebind_locked_config_fails_before_clearing_state_or_starting(self):
        self.run_ps(r'''
& $installer @initial -NoStart
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
$before=[IO.File]::ReadAllText($configPath)
$stateBefore=[Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath))
$handle=[IO.File]::Open($configPath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
try{
 $rejected=$false
 try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{$rejected=$true}
 if(-not $rejected -or $global:testLaunches -ne 0 -or $global:testStops -ne 0){throw 'locked config allowed rebind startup'}
 if([IO.File]::ReadAllText($configPath) -cne $before -or [Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath)) -cne $stateBefore){throw 'locked config rebind changed saved data'}
}finally{$handle.Dispose()}
''')

    def test_rebind_commit_failure_keeps_new_usable_session_and_previous_configuration(self):
        self.run_ps(r'''
& $installer @initial -NoStart
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$global:testNewSession='fixture-rebound-session'
$global:testLockConfigAtReady=$true
try{
 $rejected=$false
 try{& $installer -InstallDirectory $install -Rebind -BindCode 'fixture-renewed-binding-code'}catch{if($_.Exception.Message -like '*replacement session*'){$rejected=$true}else{throw}}
 if(-not $rejected -or $global:testRunning -or (Test-Path -LiteralPath (Join-Path $install 'process.json'))){throw 'failed rebind commit retained a running candidate'}
 if([IO.File]::ReadAllText($configPath) -cne $before){throw 'failed rebind commit changed previous configuration'}
 $state=Get-Content -LiteralPath (Join-Path $install 'state.json') -Raw|ConvertFrom-Json
 if($state.runnerId -cne 'fixture-runner' -or $state.sessionToken -cne 'fixture-rebound-session'){throw 'failed local commit restored a remotely revoked token'}
}finally{if($global:testCommitLock){$global:testCommitLock.Dispose()};$global:testLockConfigAtReady=$false}
& $installer -InstallDirectory $install
if(-not $global:testRunning -or $global:testLaunches -ne 2 -or $global:testReadyProbes -ne 2){throw 'retained replacement session could not restart without another binding code'}
if($global:testLaunchInputs[-1].bootstrap -cne 'fixture-binding-code'){throw 'recovery unexpectedly required a new bootstrap configuration'}
Assert-RestoredEnvironment
''')

    def test_start_waits_for_delayed_registration_and_checks_authenticated_session(self):
        self.run_ps(r'''
$global:testStateDelaySleeps=3
& $installer @initial
$state=Get-Content -LiteralPath (Join-Path $install 'state.json') -Raw|ConvertFrom-Json
if($global:testSleeps -lt 3 -or $global:testReadyProbes -ne 1 -or $state.runnerId -cne 'fixture-runner' -or -not $global:testRunning){throw 'installer returned before registration readiness'}
Assert-RestoredEnvironment
''')

    def test_start_rejects_delayed_failure_and_revoked_saved_session(self):
        self.run_ps(r'''
$global:testSuppressState=$true
$global:testExitAfterRefresh=3
$rejected=$false
try{& $installer @initial}catch{if($_.Exception.Message -like '*exited during startup*'){$rejected=$true}else{throw}}
if(-not $rejected -or $global:testRunning -or (Test-Path -LiteralPath (Join-Path $install 'state.json')) -or (Test-Path -LiteralPath (Join-Path $install 'process.json'))){throw 'delayed registration failure was accepted'}
$global:testSuppressState=$false
$global:testExitAfterRefresh=0
Write-FixtureState
$global:testProbeFailure=$true
$rejected=$false
try{& $installer -InstallDirectory $install}catch{if($_.Exception.Message -like '*session was rejected*'){$rejected=$true}else{throw}}
if(-not $rejected -or $global:testReadyProbes -ne 1 -or $global:testRunning -or (Test-Path -LiteralPath (Join-Path $install 'process.json'))){throw 'revoked saved session was reported ready'}
Assert-RestoredEnvironment
''')

    def test_start_timeout_uses_two_minute_budget_without_real_waiting(self):
        self.run_ps(r'''
$global:testSuppressState=$true
$global:testMockClock=([DateTime]'2026-11-15T00:00:00Z').ToUniversalTime()
$before=$global:testMockClock
$wall=[Diagnostics.Stopwatch]::StartNew()
$rejected=$false
try{& $installer @initial}catch{if($_.Exception.Message -like '*within 120 seconds*'){$rejected=$true}else{throw}}
if(-not $rejected -or $global:testMockClock.Subtract($before).TotalSeconds -ne 122 -or $global:testSleeps -ne 5){throw 'readiness did not use the full 120-second budget'}
if($wall.Elapsed.TotalSeconds -gt 10){throw 'mocked readiness timeout performed a real wait'}
if($global:testRunning -or (Test-Path -LiteralPath (Join-Path $install 'process.json')) -or $global:testReadyProbes -ne 0){throw 'readiness timeout retained a process or probed an unissued session'}
Assert-RestoredEnvironment
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

    def test_update_running_machine_preserves_identity_options_and_workspaces(self):
        self.run_ps(r'''
$initial.PathEntries=@((Join-Path $env:LOOPFORGE_TEST_ROOT 'other user data'))
& $installer @initial
Write-FixtureState
$statePath=Join-Path $install 'state.json'
$stateBefore=[IO.File]::ReadAllText($statePath)
$before=Get-Content -LiteralPath (Join-Path $install 'config.json') -Raw|ConvertFrom-Json
$workspace=Join-Path $install 'workspaces'
New-Item -ItemType Directory -Path $workspace|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $workspace 'work.txt')
Set-NewArtifact
& $installer -InstallDirectory $install -Update
Assert-RestoredEnvironment
$after=Get-Content -LiteralPath (Join-Path $install 'config.json') -Raw|ConvertFrom-Json
if($after.RunnerBinary -eq $before.RunnerBinary){throw 'update did not select new artifact'}
if(-not(Test-Path -LiteralPath $before.RunnerBinary)){throw 'old binary removed'}
if($after.GateBinary -eq $before.GateBinary -or -not(Test-Path -LiteralPath $before.GateBinary)){throw 'Gate bundle version not preserved'}
foreach($key in @('ServerUrl','WorkspaceRoot','CliDirectory','Name','Concurrency','bootstrapToken')){
 if($after.$key -cne $before.$key){throw 'saved option changed'}
}
if(($after.PathEntries -join ';') -cne ($before.PathEntries -join ';')){throw 'tool path changed'}
if($global:testLaunches -ne 2 -or $global:testStops -ne 1 -or $global:testWaits -ne 1 -or -not $global:testRunning){throw 'update did not stop wait and restart'}
if([IO.File]::ReadAllText($statePath) -cne $stateBefore){throw 'identity reset'}
if(-not(Test-Path -LiteralPath (Join-Path $workspace 'work.txt'))){throw 'workspace removed'}
''')

    def test_update_stopped_installation_stays_stopped(self):
        self.run_ps(r'''
& $installer @initial -NoStart
Write-FixtureState
$before=Get-Content -LiteralPath (Join-Path $install 'config.json') -Raw|ConvertFrom-Json
Set-NewArtifact
& $installer -InstallDirectory $install -Update
$after=Get-Content -LiteralPath (Join-Path $install 'config.json') -Raw|ConvertFrom-Json
if($after.RunnerBinary -eq $before.RunnerBinary -or $global:testLaunches -ne 0 -or $global:testStops -ne 0){throw 'stopped update changed running state'}
if(Test-Path -LiteralPath (Join-Path $install 'process.json')){throw 'stopped update retained process record'}
''')

    def test_update_no_start_leaves_running_installation_stopped(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
Set-NewArtifact
& $installer -InstallDirectory $install -Update -NoStart
if($global:testLaunches -ne 1 -or $global:testStops -ne 1 -or $global:testWaits -ne 1 -or $global:testRunning){throw 'NoStart update restarted runner'}
if(Test-Path -LiteralPath (Join-Path $install 'process.json')){throw 'stopped update retained process record'}
''')

    def test_failed_update_download_keeps_old_process_and_configuration(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
foreach($kind in @('network','checksum')){
 $global:testDownloadFailure=$kind -eq 'network'
 $global:testCorrupt=$kind -eq 'checksum'
 $rejected=$false
 try{& $installer -InstallDirectory $install -Update}catch{$rejected=$true}
 if(-not $rejected -or $global:testStops -ne 0 -or $global:testLaunches -ne 1 -or -not $global:testRunning){throw 'download failure interrupted old runner'}
 if([IO.File]::ReadAllText($configPath) -cne $before){throw 'download failure changed configuration'}
 if(@(Get-ChildItem -LiteralPath $install -Filter '.download-*').Count){throw 'failed download retained temporary file'}
}
''')

    def test_gate_update_download_failure_preserves_previous_running_bundle(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$old=$before|ConvertFrom-Json
Set-NewArtifact
$global:testGateDownloadFailure=$true
$rejected=$false
try{& $installer -InstallDirectory $install -Update}catch{if($_.Exception.Message -like '*Gate download failure*'){$rejected=$true}else{throw}}
if(-not $rejected -or $global:testDownloads -ne 5 -or $global:testStops -ne 0 -or $global:testLaunches -ne 1 -or -not $global:testRunning){throw 'Gate failure interrupted previous runner'}
if([IO.File]::ReadAllText($configPath) -cne $before -or -not(Test-Path -LiteralPath $old.RunnerBinary) -or -not(Test-Path -LiteralPath $old.GateBinary)){throw 'Gate failure altered installed bundle'}
if(@(Get-ChildItem -LiteralPath $install -Filter '.download-*').Count){throw 'Gate failure retained staged download'}
''')

    def test_linux_gate_validation_failure_preserves_running_installation(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$linuxFixture=Join-Path $env:LOOPFORGE_TEST_ROOT 'agentflow-gate-mcp-linux-amd64.fixture'
$original=[IO.File]::ReadAllBytes($linuxFixture)
foreach($kind in @('network','checksum','magic','class','endian','architecture','truncated')){
 $bytes=$original.Clone()
 $global:testLinuxDownloadFailure=$kind -eq 'network'
 $global:testLinuxCorrupt=$kind -eq 'checksum'
 $expected=''
 switch($kind){
  'network'{$expected='*Linux download failure*'}
  'checksum'{$expected='*Linux helper checksum mismatch*'}
  'magic'{$bytes[0]=0;$expected='*not an ELF*'}
  'class'{$bytes[4]=1;$expected='*64-bit little-endian ELF*'}
  'endian'{$bytes[5]=2;$expected='*64-bit little-endian ELF*'}
  'architecture'{$bytes[18]=183;$expected='*Linux helper architecture does not match*'}
  'truncated'{$bytes=[byte[]]@(0x7f,0x45,0x4c,0x46);$expected='*not an ELF*'}
 }
 [IO.File]::WriteAllBytes($linuxFixture,$bytes)
 $rejected=$false
 try{& $installer -InstallDirectory $install -Update}catch{if($_.Exception.Message -like $expected){$rejected=$true}else{throw}}
 if(-not $rejected -or $global:testStops -ne 0 -or $global:testLaunches -ne 1 -or -not $global:testRunning){throw 'Linux failure interrupted previous runner'}
 if([IO.File]::ReadAllText($configPath) -cne $before){throw 'Linux validation failure changed configuration'}
 if(@(Get-ChildItem -LiteralPath $install -Filter '.download-*').Count){throw 'Linux failure retained temporary downloads'}
}
''')

    def test_linux_only_update_uses_new_bundle_and_rolls_back_safely(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$old=$before|ConvertFrom-Json
$oldLinux=Join-Path (Split-Path -Parent $old.GateBinary) 'agentflow-gate-mcp-linux-amd64'
$stateBefore=[IO.File]::ReadAllText((Join-Path $install 'state.json'))
Set-NewArtifact -Name 'agentflow-gate-mcp-linux-amd64'
$global:testFailLaunches=1
$rejected=$false
try{& $installer -InstallDirectory $install -Update}catch{if($_.Exception.Message -like '*Previous Runner restarted*'){$rejected=$true}else{throw}}
if(-not $rejected -or [IO.File]::ReadAllText($configPath) -cne $before -or $global:testBinary -cne $old.RunnerBinary){throw 'Linux-only update did not rollback original bundle'}
if(-not(Test-Path -LiteralPath $oldLinux)){throw 'Linux-only rollback lost original helper'}
& $installer -InstallDirectory $install -Update
$after=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
$oldVersion=(Split-Path -Leaf (Split-Path -Parent $old.RunnerBinary)).Split('-')
$newVersion=(Split-Path -Leaf (Split-Path -Parent $after.RunnerBinary)).Split('-')
if($oldVersion[0] -cne $newVersion[0] -or $oldVersion[1] -cne $newVersion[1] -or $oldVersion[2] -ceq $newVersion[2]){throw 'Linux helper digest not represented in bundle identity'}
if($global:testLaunches -ne 4 -or $global:testStops -ne 2 -or -not $global:testRunning){throw 'Linux-only update process lifecycle incorrect'}
if([IO.File]::ReadAllText((Join-Path $install 'state.json')) -cne $stateBefore){throw 'Linux-only update reset identity'}
if(-not(Test-Path -LiteralPath $old.RunnerBinary) -or -not(Test-Path -LiteralPath $old.GateBinary) -or -not(Test-Path -LiteralPath $oldLinux)){throw 'Linux-only update removed old bundle'}
Assert-RestoredEnvironment
''')

    def test_arm64_installation_downloads_native_linux_companion(self):
        self.environment["PROCESSOR_ARCHITEW6432"] = "ARM64"
        self.environment["LOOPFORGE_TEST_ARCH"] = "ARM64"
        for name in ("agentflow-runner", "agentflow-gate-mcp"):
            fixture = self.root / (name + ".fixture")
            binary = bytearray(fixture.read_bytes())
            struct.pack_into("<H", binary, 68, 0xAA64)
            fixture.write_bytes(binary)
        self.run_ps(r'''
& $installer @initial
$config=Get-Content -LiteralPath (Join-Path $install 'config.json') -Raw|ConvertFrom-Json
$bundle=Split-Path -Parent $config.RunnerBinary
if(-not(Test-Path -LiteralPath (Join-Path $bundle 'agentflow-gate-mcp-linux-arm64')) -or (Test-Path -LiteralPath (Join-Path $bundle 'agentflow-gate-mcp-linux-amd64'))){throw 'incorrect Linux companion architecture installed'}
if($global:testDownloads -ne 3 -or $global:testLaunches -ne 1){throw 'ARM64 bundle did not install'}
''')

    def test_upgrade_and_uninstall_recognize_previous_two_digest_bundles(self):
        self.run_ps(r'''
& $installer @initial -NoStart
$configPath=Join-Path $install 'config.json'
$config=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
$version=(Split-Path -Leaf (Split-Path -Parent $config.RunnerBinary)).Split('-')
$oldDirectory=Join-Path (Join-Path $install 'bin') ($version[0]+'-'+$version[1])
New-Item -ItemType Directory -Path $oldDirectory|Out-Null
$oldRunner=Join-Path $oldDirectory 'agentflow-runner.exe'
$oldGate=Join-Path $oldDirectory 'agentflow-gate-mcp.exe'
Copy-Item -LiteralPath $config.RunnerBinary -Destination $oldRunner
Copy-Item -LiteralPath $config.GateBinary -Destination $oldGate
'keep'|Set-Content -LiteralPath (Join-Path $oldDirectory 'user-file.txt')
$config.RunnerBinary=$oldRunner
$config.GateBinary=$oldGate
$config|ConvertTo-Json|Set-Content -LiteralPath $configPath
& $installer -InstallDirectory $install -Update
$after=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
if((Split-Path -Leaf (Split-Path -Parent $after.RunnerBinary)) -notmatch '^[a-f0-9]{16}-[a-f0-9]{16}-[a-f0-9]{16}$'){throw 'legacy installation did not gain complete bundle'}
if($global:testLaunches -ne 0){throw 'legacy stopped update started runner'}
& $installer -InstallDirectory $install -Uninstall
if((Test-Path -LiteralPath $oldRunner) -or (Test-Path -LiteralPath $oldGate) -or -not(Test-Path -LiteralPath (Join-Path $oldDirectory 'user-file.txt'))){throw 'legacy bundle cleanup removed wrong files'}
''')

    def test_failed_update_start_restores_previous_binary_and_process(self):
        for injection in ("$global:testFailLaunches=1", "$global:testLaunchException=$true"):
            with self.subTest(injection=injection):
                # Each PowerShell process resets its mocks. The previous run is stopped first.
                self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$old=$before|ConvertFrom-Json
$stateBefore=[IO.File]::ReadAllText((Join-Path $install 'state.json'))
Set-NewArtifact
FAILURE_INJECTION
$rejected=$false
try{& $installer -InstallDirectory $install -Update}catch{if($_.Exception.Message -like '*Previous Runner restarted*'){$rejected=$true}else{throw}}
if(-not $rejected -or -not $global:testRunning -or $global:testLaunches -ne 3){throw 'failed update did not restart old runner'}
if($global:testBinary -cne $old.RunnerBinary -or [IO.File]::ReadAllText($configPath) -cne $before){throw 'rollback lost previous version'}
if([IO.File]::ReadAllText((Join-Path $install 'state.json')) -cne $stateBefore){throw 'rollback reset identity'}
Assert-RestoredEnvironment
& $installer -InstallDirectory $install -Stop
'''.replace("FAILURE_INJECTION", injection))

    def test_failed_update_commit_stops_candidate_and_restarts_old_version(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=[IO.File]::ReadAllText($configPath)
$old=$before|ConvertFrom-Json
Set-NewArtifact
$handle=[IO.File]::Open($configPath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
try{
 $rejected=$false
 try{& $installer -InstallDirectory $install -Update}catch{if($_.Exception.Message -like '*Previous Runner restarted*'){$rejected=$true}else{throw}}
 if(-not $rejected){throw 'config commit failure accepted'}
}finally{$handle.Dispose()}
if($global:testLaunches -ne 3 -or $global:testStops -ne 2 -or $global:testBinary -cne $old.RunnerBinary){throw 'commit failure did not rollback processes'}
if([IO.File]::ReadAllText($configPath) -cne $before){throw 'failed config commit changed old configuration'}
Assert-RestoredEnvironment
''')

    def test_uninstall_unregisters_after_stopping_and_retains_user_files(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$before=Get-Content -LiteralPath $configPath -Raw|ConvertFrom-Json
$workspace=Join-Path $install 'workspaces'
New-Item -ItemType Directory -Path $workspace|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $workspace 'work.txt')
'keep'|Set-Content -LiteralPath (Join-Path $install 'unrelated.txt')
'keep'|Set-Content -LiteralPath (Join-Path (Split-Path -Parent $before.RunnerBinary) 'user-file.txt')
foreach($name in @('stdout.log','stderr.log')){'log'|Set-Content -LiteralPath (Join-Path $install $name)}
& $installer -InstallDirectory $install -Uninstall
if($global:testDeregisters -ne 1 -or $global:testStops -ne 1 -or $global:testWaits -ne 1){throw 'uninstall ordering incorrect'}
foreach($path in @($before.RunnerBinary,$before.GateBinary,(Join-Path (Split-Path -Parent $before.GateBinary) 'agentflow-gate-mcp-linux-amd64'),$configPath,(Join-Path $install 'state.json'),(Join-Path $install 'process.json'),(Join-Path $install 'install.ps1'),(Join-Path $install 'stdout.log'),(Join-Path $install 'stderr.log'))){if(Test-Path -LiteralPath $path){throw 'managed file retained'}}
foreach($path in @((Join-Path $workspace 'work.txt'),(Join-Path $install 'unrelated.txt'),(Join-Path (Split-Path -Parent $before.RunnerBinary) 'user-file.txt'))){if(-not(Test-Path -LiteralPath $path)){throw 'user file removed'}}
& $installer -InstallDirectory $install -Uninstall
if($global:testDeregisters -ne 1 -or $global:testStops -ne 1){throw 'repeated uninstall performed operations'}
''')

    def test_uninstall_failure_keeps_retry_credentials_then_succeeds(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
$before=[IO.File]::ReadAllText($configPath)
$stateBefore=[IO.File]::ReadAllText($statePath)
$binary=($before|ConvertFrom-Json).RunnerBinary
$global:testDeregisterFailure=$true
$rejected=$false
try{& $installer -InstallDirectory $install -Uninstall}catch{if($_.Exception.Message -like '*deregistration failed*'){$rejected=$true}else{throw}}
if(-not $rejected -or $global:testRunning -or $global:testDeregisters -ne 1){throw 'unregister failure did not stop and fail clearly'}
if([IO.File]::ReadAllText($configPath) -cne $before -or [IO.File]::ReadAllText($statePath) -cne $stateBefore -or -not(Test-Path -LiteralPath $binary)){throw 'unregister failure destroyed retry data'}
$global:testDeregisterFailure=$false
& $installer -InstallDirectory $install -Uninstall
if($global:testDeregisters -ne 2 -or (Test-Path -LiteralPath $configPath)){throw 'uninstall retry did not succeed'}
''')

    def test_uninstall_locked_config_fails_before_stopping_or_deregistering(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$statePath=Join-Path $install 'state.json'
$before=[IO.File]::ReadAllText($configPath)
$stateBefore=[IO.File]::ReadAllText($statePath)
$handle=[IO.File]::Open($configPath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
try{
 $rejected=$false
 try{& $installer -InstallDirectory $install -Uninstall}catch{$rejected=$true}
 if(-not $rejected -or $global:testDeregisters -ne 0 -or $global:testStops -ne 0 -or -not $global:testRunning){throw 'locked configuration changed the running installation'}
 if([IO.File]::ReadAllText($configPath) -cne $before -or [IO.File]::ReadAllText($statePath) -cne $stateBefore){throw 'locked configuration changed retry data'}
}finally{$handle.Dispose()}
& $installer -InstallDirectory $install -Uninstall
if($global:testDeregisters -ne 1 -or $global:testStops -ne 1 -or (Test-Path -LiteralPath $configPath)){throw 'uninstall did not recover after releasing configuration'}
''')

    def test_uninstall_cleanup_failure_can_retry_without_deregistering_twice(self):
        self.run_ps(r'''
& $installer @initial -NoStart
Write-FixtureState
$statePath=Join-Path $install 'state.json'
$configPath=Join-Path $install 'config.json'
$handle=[IO.File]::Open($statePath,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::Read)
try{
 $rejected=$false
 try{& $installer -InstallDirectory $install -Uninstall}catch{$rejected=$true}
 if(-not $rejected -or $global:testDeregisters -ne 1){throw 'fixture cleanup failure was not reached'}
}finally{$handle.Dispose()}
& $installer -InstallDirectory $install -Uninstall
if($global:testDeregisters -ne 1 -or (Test-Path -LiteralPath $configPath)){throw 'cleanup retry re-used deleted session'}
''')

    def test_uninstall_preserves_external_workspace_and_other_installations(self):
        self.run_ps(r'''
$external=Join-Path $env:LOOPFORGE_TEST_ROOT 'external workspace'
New-Item -ItemType Directory -Path $external|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $external 'work.txt')
$initial.WorkspaceRoot=$external
& $installer @initial -NoStart
& $installer -InstallDirectory $install -Uninstall
if($global:testDeregisters -ne 0){throw 'never-started runner deregistered'}
if(-not(Test-Path -LiteralPath (Join-Path $external 'work.txt'))){throw 'external workspace removed'}
if(-not(Test-Path -LiteralPath (Join-Path $env:LOOPFORGE_TEST_ROOT 'other user data'))){throw 'other user directory removed'}
''')

    def test_uninstall_retains_workspace_containing_installation_and_handles_bad_state(self):
        self.run_ps(r'''
$initial.WorkspaceRoot=$install
& $installer @initial -NoStart
$work=Join-Path $install 'user work'
New-Item -ItemType Directory -Path $work|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $work 'work.txt')
'{"sessionToken":"fixture-session",broken'|Set-Content -LiteralPath (Join-Path $install 'state.json')
$rejected=$false
try{& $installer -InstallDirectory $install -Uninstall}catch{
 if($_.Exception.Message -like '*Cannot read saved state.json*'){$rejected=$true}else{throw}
 if($_.Exception.Message -like '*fixture-session*'){throw 'saved secret leaked in parser diagnostic'}
}
if(-not $rejected -or -not(Test-Path -LiteralPath (Join-Path $install 'config.json'))){throw 'bad state did not retain retry files'}
& $installer -InstallDirectory $install -Uninstall -LocalOnly
if(-not(Test-Path -LiteralPath (Join-Path $work 'work.txt')) -or (Test-Path -LiteralPath (Join-Path $install 'config.json'))){throw 'overlapping workspace cleanup lost user data or retained credentials'}
''')

    def test_recycled_pid_start_time_is_never_stopped_during_update_or_uninstall(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$global:testWrongStart=$true
Set-NewArtifact
& $installer -InstallDirectory $install -Update
if($global:testStops -ne 0 -or $global:testLaunches -ne 1){throw 'recycled PID affected by update'}
if(Test-Path -LiteralPath (Join-Path $install 'process.json')){throw 'stale process record not cleared by update'}
# The unrelated process is still alive; no runner owns it.
& $installer -InstallDirectory $install -Uninstall
if($global:testStops -ne 0 -or $global:testDeregisters -ne 1 -or -not $global:testRunning){throw 'recycled PID affected by uninstall'}
''')

    def test_stop_failure_prevents_uninstall_and_update_commit(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$before=[IO.File]::ReadAllText((Join-Path $install 'config.json'))
$global:testStopHang=$true
foreach($operation in @('Update','Uninstall')){
 $arguments=@{InstallDirectory=$install}
 $arguments[$operation]=$true
 $rejected=$false
 try{& $installer @arguments}catch{if($_.Exception.Message -like '*did not stop*'){$rejected=$true}else{throw}}
 if(-not $rejected -or $global:testDeregisters -ne 0 -or $global:testLaunches -ne 1){throw 'failed stop allowed lifecycle operation'}
 if([IO.File]::ReadAllText((Join-Path $install 'config.json')) -cne $before){throw 'failed stop changed config'}
 if(-not(Test-Path -LiteralPath (Join-Path $install 'process.json'))){throw 'failed stop cleared process record'}
}
''')

    def test_lifecycle_rejects_conflicts_and_missing_update_before_mutation(self):
        self.run_ps(r'''
foreach($arguments in @(@{Update=$true},@{Update=$true;Stop=$true},@{Uninstall=$true;Stop=$true},@{Update=$true;BindCode='fixture-binding-code'},@{Uninstall=$true;WorkspaceRoot='other'},@{LocalOnly=$true})){
 $arguments.InstallDirectory=$install
 $rejected=$false
 try{& $installer @arguments}catch{$rejected=$true}
 if(-not $rejected -or (Test-Path -LiteralPath $install) -or $global:testDownloads -ne 0){throw 'invalid lifecycle mutated installation'}
}
& $installer -InstallDirectory $install -Uninstall
if(Test-Path -LiteralPath $install){throw 'absent uninstall created directory'}
''')

    def test_local_only_uninstall_cleans_expired_session_and_retains_workspace(self):
        self.run_ps(r'''
& $installer @initial
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$workspace=Join-Path $install 'workspaces'
New-Item -ItemType Directory -Path $workspace|Out-Null
'keep'|Set-Content -LiteralPath (Join-Path $workspace 'work.txt')
$global:testDeregisterFailure=$true
$rejected=$false
try{& $installer -InstallDirectory $install -Uninstall}catch{if($_.Exception.Message -like '*deregistration failed*'){$rejected=$true}else{throw}}
if(-not $rejected -or -not(Test-Path -LiteralPath $configPath)){throw 'expired session lost retry data'}
& $installer -InstallDirectory $install -Uninstall -LocalOnly
if($global:testDeregisters -ne 1 -or (Test-Path -LiteralPath $configPath)){throw 'local-only uninstall tried expired credential again'}
if(-not(Test-Path -LiteralPath (Join-Path $workspace 'work.txt'))){throw 'local-only uninstall removed workspace'}
''')

    def test_uninstall_rejects_escaping_binary_and_junction_before_mutation(self):
        self.run_ps(r'''
& $installer @initial -NoStart
Write-FixtureState
$configPath=Join-Path $install 'config.json'
$original=[IO.File]::ReadAllText($configPath)
$config=$original|ConvertFrom-Json
$config.RunnerBinary=Join-Path $env:LOOPFORGE_TEST_ROOT 'outside.exe'
$config|ConvertTo-Json|Set-Content -LiteralPath $configPath
$rejected=$false
try{& $installer -InstallDirectory $install -Uninstall}catch{if($_.Exception.Message -like '*escapes*'){$rejected=$true}else{throw}}
if(-not $rejected -or $global:testDeregisters -ne 0){throw 'escaping saved path accepted'}
[IO.File]::WriteAllText($configPath,$original)
$outside=Join-Path $env:LOOPFORGE_TEST_ROOT 'outside'
New-Item -ItemType Directory -Path $outside|Out-Null
$junction=Join-Path (Join-Path $install 'bin') (('a'*16)+'-'+('b'*16))
New-Item -ItemType Junction -Path $junction -Target $outside|Out-Null
try{
 $rejected=$false
 try{& $installer -InstallDirectory $install -Uninstall}catch{if($_.Exception.Message -like '*junctions*'){$rejected=$true}else{throw}}
 if(-not $rejected -or $global:testDeregisters -ne 0){throw 'junction cleanup was allowed'}
 if(-not(Test-Path -LiteralPath $configPath)){throw 'invalid path removed configuration'}
}finally{[IO.Directory]::Delete($junction)}
''')


if __name__ == "__main__":
    unittest.main()
