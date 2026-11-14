# Windows platform and local Runner

The platform runs in Docker Desktop Linux containers. A Windows Runner executes
assigned tasks with the tools installed for the current Windows user. The two
processes can use the same computer, or the Runner can connect to a remote platform.

## Start the platform

Install Git for Windows and Docker Desktop with Linux containers enabled. The
PowerShell scripts support Windows PowerShell 5.1 and PowerShell 7. Docker Compose
2.24.4 or newer is required. Complete Docker Desktop setup before starting the
platform. If installing WSL or enabling virtualization requests a restart, restart
Windows and wait for Docker Desktop to report that its engine is running.

From a checkout of this repository, run the following command in PowerShell.

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\start-local.ps1
```

The script binds HTTP to `127.0.0.1:8090` and MySQL to `127.0.0.1:33315` by
default. It prints the local URL when both containers are healthy. Open that URL
and create the first administrator account. Database passwords and the session
secret are generated randomly. Subsequent public signup is disabled by default.

For another installation, choose unused ports and a unique Compose project name
on its first start. A project name omitted on the first start is derived from
the checkout path.

```powershell
.\scripts\start-local.ps1 -ProjectName loopforge-windows-demo -HttpPort 18090 -MySqlPort 43315 -NoBrowser
```

Instance settings are stored in `.local/windows/settings.json`, secrets in
`.local/windows/.env`, and the generated Compose override in
`.local/windows/compose.yaml`. These paths are Git-ignored. Later starts and stops
reuse the recorded project name and ports. Passing conflicting ports or a project
name produces an error. Use another checkout for another instance. Keep the
configuration with its data volumes when backing up or moving the installation.

```powershell
# Reuse the recorded instance settings.
.\scripts\start-local.ps1 -NoBrowser

# Stop this checkout's Runner and containers, retaining the data volumes.
.\scripts\start-local.ps1 -Stop
```

`-DockerBinary` accepts an explicit Docker executable path. `-Image` selects a
published or locally built platform image and records it for subsequent starts.
For a source build, build the chatbot and platform image according to the
[contribution guide](../CONTRIBUTING.md), then pass its tag using `-Image`.

The workspace uses a Docker named volume. Its Linux mount path is also the path
inside the platform container, so sibling sandbox containers created through the
Docker socket can mount the same files. Docker Desktop's Windows host paths do
not have to be translated by the workflow engine. The platform and MySQL keep
their other data in Compose project-scoped named volumes.

Each invocation explicitly installs absent observability generated columns and
imports the public catalog and welcome project using the shipped SQL scripts.
The catalog scripts preserve existing definitions. SQL is copied as UTF-8 bytes
into the database container, and a Chinese-text round-trip is checked before any
catalog import. A failed encoding check stops initialization and preserves the
existing database. Other migrations remain explicit operator actions as described
in the [deployment guide](deployment.md).

## Connect a Windows execution machine

Install and log into a native Windows `.exe` distribution of the coding CLI you
intend to use under the Windows account that will run the Runner. CLI detection
currently accepts native executables. npm `.cmd` and `.bat` wrappers are excluded
from the advertised runtimes. Check whether the selected CodeBuddy, Codex or
Claude Code distribution supplies a native executable before binding it.
Install Git for Windows for checkout operations and shell workflow nodes. Add
the CLI, Git and any project build tools to that account's `PATH`, or supply their
directories with `-CliDirectory` and `-PathEntries`.

### Connect from the platform page without a source checkout

On a platform built with Windows support, open **Settings > Execution machines >
Connect a new machine** and select the **Windows · PowerShell** tab. Copy the
connection command while its binding code is valid, then run it in Windows
PowerShell 5.1 or PowerShell 7 under the account that owns the installed CLI login.
The **macOS / Linux** tab remains the first and default selection on every
operating system. Switching tabs updates the instructions and commands together.

The Windows command downloads `/runners/install.ps1` from that platform and saves
it to a temporary file before execution. The installer downloads the matching
Runner and Gate MCP executables, checks their SHA-256 digests and Windows
architecture, and starts the Runner in the background. No repository checkout,
Go toolchain or Docker installation is required for native execution. SOPs using
a Docker sandbox still require their configured Docker environment. The
platform must provide both binaries for Windows amd64 or arm64; the installer
does not install the coding CLI, Git or project build tools.

The default installation is stored under
`%LOCALAPPDATA%\LoopForge\runners\<platform-url-hash>`. It keeps configuration,
binding and session state, logs, a process record, and the default workspaces.
Treat this directory as sensitive local state. Each platform URL gets a separate
installation. Use an explicit `-InstallDirectory` when multiple bindings to the
same platform are needed; an existing binding cannot silently change accounts.

After stopping the Runner, use the page's Windows start command to download the
current binaries and start it with saved options. The stop command retains the
binding, configuration and workspaces. Repeating the connection command while
the Runner is running leaves that process unchanged. Custom CLI/tool directories,
workspace root, name and concurrency can be passed to the downloaded script and
are remembered. For example:

```powershell
# Use the same platform URL as the page's command.
powershell -NoProfile -ExecutionPolicy Bypass -File $lfInstaller `
  -ServerUrl 'https://loopforge.example.com' `
  -BindCode '<binding-code>' -CliDirectory 'C:\Tools\CodingCLI' `
  -WorkspaceRoot 'C:\Work\LoopForge'
```

`$lfInstaller` is the downloaded script path set by the page's command. Stop an
existing Runner before changing these options. The saved `install.ps1` in its
installation directory also accepts `-InstallDirectory '<installation-path>'
-Stop`, without downloading a new script. Connecting the machine enables assigned
SOP tasks; it does not move the homepage assistant or requirement clarification
to the local machine. Their existing server-side credential requirements remain.

### Use the source checkout launcher manually

Obtain the matching Windows Runner and Gate MCP binaries from a platform built
with Windows support, or build them from this checkout with Go. The example uses
Windows amd64. For Windows arm64, build for `GOARCH=arm64` and pass the resulting
paths explicitly to the launcher.

```powershell
New-Item -ItemType Directory -Force .\bin | Out-Null
$env:CGO_ENABLED = '0'
go build -o .\bin\agentflow-runner-windows-amd64.exe .\cmd\agentflow-runner
go build -o .\bin\agentflow-gate-mcp-windows-amd64.exe .\cmd\agentflow-gate-mcp
```

In the platform, open **Settings > Execution machines** and generate a binding
code. In PowerShell, start the Runner with that code. The server URL is inferred
from this checkout's Windows instance settings when available.

```powershell
.\scripts\start-local-runner.ps1 -BindCode '<binding-code>'

# Or connect to another platform and use explicitly installed binaries/tools.
.\scripts\start-local-runner.ps1 -ServerUrl 'https://loopforge.example.com' `
  -BindCode '<binding-code>' `
  -RunnerBinary 'C:\Tools\LoopForge\agentflow-runner.exe' `
  -GateBinary 'C:\Tools\LoopForge\agentflow-gate-mcp.exe' `
  -CliDirectory 'C:\Tools\CodingCLI' `
  -PathEntries 'C:\Program Files\Git\bin','C:\Program Files\nodejs'
```

For Codex standalone installations, point `-CliDirectory` at the installed package's
binary directory with its Windows sandbox helper files available in the matching
package. Run `codex login status` in the same Windows account to check its login.
Other CLIs use their own login commands and configuration. Select the Windows
execution machine in the platform and set its installed runtime's authentication
mode to **local** to reuse that account's CLI login. Git operations use that
account's configured Git credential helper or SSH setup.

Runner configuration, its one-time binding code, session state, logs and PID
record are stored under `.local/windows/runner`. Treat that directory as sensitive
local state. The launcher keeps the current user's home and restores environment
variables in the invoking terminal after starting the child process. It records
the process path and start time so a later stop cannot target a recycled PID.

```powershell
# Restart with saved options after stopping the previous process.
.\scripts\start-local-runner.ps1
.\scripts\start-local-runner.ps1 -Stop
```

`-WorkspaceRoot`, `-Name` and `-Concurrency` can be set on the launcher and are
remembered. Stop the Runner before changing these options. The default concurrency
is one. The platform launcher starts the platform containers. Start the Runner
separately when it is needed.

Windows Runner self-updates are disabled, including when the binary is started
directly or passed `-auto-update=true`. Windows cannot replace a running executable
with the current updater. Stop the Runner, replace the Runner and Gate MCP binaries
with a matching build, then start it again. The Windows
workspace browser and task execution are supported, while the web interactive
PTY terminal is unavailable on Windows. Use a local terminal for interactive
commands. These scripts do not change which platform features execute on the
server. In particular, the homepage assistant and requirement clarification keep
their existing server-side credential requirements.

Cancelling a Windows command immediately terminates its process tree; it does not
have the Unix SIGTERM grace period. When the root command exits, its Windows Job
Object also ends any remaining descendants. Background commands such as
`nohup ... &` therefore do not keep a service running after that task exits. Keep
services in the foreground for the lifetime of their task, or manage them
separately from the Runner.

## Launcher regression checks

Run the mock suite on Windows with Python 3 and PowerShell available. It exercises
instance isolation, saved options, UTF-8 failure handling and the Runner process
environment without starting Docker containers or a real Runner.

```powershell
python .\scripts\test-windows-startup.py
```

The standalone installer suite covers raw HTTP downloads and PowerShell parsing,
binary checksums and architecture, saved bindings, process identity and retained
workspaces. It uses fixture downloads and processes, without account credentials:

```powershell
python .\scripts\test-windows-installer.py
$env:LOOPFORGE_TEST_POWERSHELL = 'pwsh'
python .\scripts\test-windows-installer.py
```
