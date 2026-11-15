# Windows execution machines

Deploy the platform on Linux using the [deployment guide](deployment.md).
The Windows Runner connects to that platform and executes assigned workflow
tasks with the current Windows account's tools and CLI login. Installation,
registration, reconnect, task cancellation, manual update and uninstall use the
same machine identity and workspace.

Use Windows PowerShell 5.1 or PowerShell 7 on Windows amd64 or arm64. The platform
must provide the matching Windows Runner and Gate binaries, plus the Linux Gate
companion for that architecture. The installer verifies each SHA-256 digest and
executable format before installation. Build these with the platform's existing
`scripts/build-binaries.sh --helpers` distribution step.

Install Git for Windows for checkout operations and shell nodes. Install each
project's normal build tools and a native `.exe` distribution of the selected
Agent CLI, then log into that CLI under the Runner's Windows account. Supply tool
directories through `-CliDirectory` and `-PathEntries` when they are outside
that account's `PATH`. Native CLI conversations that use approval hooks also
require Python 3.8 or newer available as `python.exe` or `python3.exe`. Microsoft
Store App Execution Aliases are not treated as an installed interpreter.

## Connect

Generate a one-time binding code on the Linux platform's **Settings > Execution
machines** page. Download the installer from that same platform and run it under
the Windows account that will own the Runner.

Use HTTPS for remote platform connections. Plain HTTP is accepted only for
loopback addresses when developing locally.

```powershell
$server = 'https://loopforge.example.com'
$installer = Join-Path $env:TEMP 'loopforge-runner-install.ps1'
Invoke-WebRequest -UseBasicParsing -Uri "$server/runners/install.ps1" -OutFile $installer -ErrorAction Stop
powershell -NoProfile -ExecutionPolicy Bypass -File $installer -ServerUrl $server -BindCode '<binding-code>'
```

The default installation directory is
`%LOCALAPPDATA%\LoopForge\runners\<platform-url-hash>`. Each platform URL has
separate configuration, session state, process records and workspaces. An explicit
`-InstallDirectory` selects another installation. Keep the same platform URL
and installation directory for later operations.

Repeating the connect command for a running installation leaves its process in
place. A later start reuses its saved identity and options. Use the stored
`install.ps1` or download a fresh script from the platform.

The Runner runs in the background under the current account. After rebooting
Windows, run the installer again with the same server and installation directory,
omitting `-BindCode`, to reconnect using the saved registration.

## Update

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File $installer -ServerUrl $server -Update
```

The installer validates the complete Runner and Gate bundle before stopping the installed process.
An installation that was running restarts after the update. An installation
that was stopped stays stopped. The Runner identity and workspace path carry
over. If the candidate fails to start, the installer restores the previous
configuration and restarts the previous version when it had been running.

Use `-Stop` to stop the Runner while retaining the installation. Windows updates
use the installer because the current running executable cannot be replaced by
the Runner's Unix self-update path.

## Renew a binding

If a saved session has expired or was revoked, generate a fresh binding code
under the same platform account that owns the machine, then stop and rebind it:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File $installer -ServerUrl $server -Stop
powershell -NoProfile -ExecutionPolicy Bypass -File $installer -ServerUrl $server -Rebind -BindCode '<fresh-binding-code>'
```

Rebinding retains the machine ID, workspace, CLI login and saved options. The
installer waits for the platform to accept the registration before reporting
success. Follow the reported recovery instructions if registration fails.

## Uninstall

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File $installer -ServerUrl $server -Uninstall
```

The installer verifies the recorded process identity, stops that process, and
uses the saved session to deregister the machine from the platform. It removes
the installer-managed executable and configuration after deregistration succeeds.
Workspace data is retained, including workspaces outside the installation
directory. If deregistration fails, the saved configuration and session remain
available so the operation can be retried.

Use `-Uninstall -LocalOnly` when the platform is unreachable or the saved
session has expired. This removes the local installation and retains workspaces.
Remove the offline machine separately on the platform.

## Task execution

Shell nodes use Git Bash. Agent nodes use the selected installed native CLI.
The Runner retains the Windows account's CLI login and Git configuration, and
task credentials continue to follow the platform's selected authentication mode.
Successful task outputs and artifacts are returned through the existing Runner
protocol. Restart and update retain the workspace for subsequent nodes.

A native Windows task owns its process tree. Cancellation immediately stops that tree,
including descendants. Remaining descendants also end when the root command
exits. Run a development server in the foreground for its task's lifetime, or
use an independently managed service when it must survive between tasks.
The workflow task and workspace APIs support native Windows execution; the web
interactive PTY requires a Unix execution machine.

For native execution, select a sandbox with the `local` driver. Optional Linux
containers require Docker Desktop in Linux-container mode and images with the
workflow's tools. The installer includes a Linux Gate companion for these tasks.
For personal Git SSH, follow the [host trust and key setup](deployment.md#git-repositories-over-ssh).
Create a dedicated `known_hosts` file containing verified, exact host entries
for the Runner account. Set its absolute Windows path before starting the Runner,
and persist the setting for future starts:

```powershell
$knownHosts = Join-Path $env:USERPROFILE '.ssh\loopforge_known_hosts'
$env:AGENTFLOW_GIT_SSH_KNOWN_HOSTS = $knownHosts
[Environment]::SetEnvironmentVariable('AGENTFLOW_GIT_SSH_KNOWN_HOSTS', $knownHosts, 'User')
```

Restart an already running Runner after changing this setting.

Personal Git SSH keys follow the task's selected authentication mode for both
host-side checkout and delivery and Git commands inside Linux containers.
Container images need Python 3 at `/usr/bin/python3` or `/usr/local/bin/python3`
for task process supervision, plus Git and the OpenSSH client tools (`ssh`,
`ssh-agent` and `ssh-add`) for repository access.
Successful Docker shell nodes can leave services running for later nodes in the
same reused sandbox; failed or canceled invocations stop their own descendants.
Personal keys use an isolated, short-lived SSH agent for each container command.
Docker tasks use the platform's existing workflow and workspace APIs.
