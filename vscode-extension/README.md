# LoopForge for VS Code

This directory contains the first standalone desktop VS Code extension for LoopForge. It connects to an existing LoopForge HTTP server and reuses the platform Runs API; it does not modify the LoopForge server or install a second workflow engine in the editor.

## Supported scope

- VS Code 1.85 or newer on desktop.
- LoopForge platform API with `POST /api/runs`, the Runs detail/log/approval/artifact endpoints, and the workspace diff endpoint.
- Platform workflows (SOPs). The extension starts a run through the server; model providers and execution environments remain configured by LoopForge.
- A server URL and, when authentication is enabled, a LoopForge Personal API Token. The token is stored in VS Code SecretStorage and is never written to workspace settings.

The extension uses the active file's workspace folder (or the first folder if no file is active). `LoopForge: Start Workflow` asks for a task description and appends the project path, active file path and up to 4,000 characters of selected text as input. This sends the selected content to the configured server. Paths are descriptive: the extension does not upload or mount the local project. Configure a repository on the platform if the workflow needs source files. The server remains the source of truth for project access, workflow permissions, execution, and secrets.

## Install from a VSIX

From the repository root:

```bash
cd vscode-extension
npx --yes @vscode/vsce package --no-dependencies
code --install-extension loopforge-0.1.4.vsix
```

The package can later be published to an extension marketplace with the publisher's normal VSCE credentials. No marketplace account or credential is required to build or install the VSIX locally.

## Configure

Use the command palette:

1. Start or obtain access to a LoopForge platform server. Set `loopforge.serverUrl` in user or remote settings to its base URL (for example `http://127.0.0.1:8090` when the server runs on the extension host). HTTPS is required for non-loopback hosts; HTTP is accepted only for `localhost`, IPv4 addresses in `127.0.0.0/8`, and `::1`. Workspace settings cannot change the server address, to prevent a project from redirecting the stored token.
2. Create a Personal API Token in the platform's account settings if authentication is enabled. `LoopForge: Configure API Token` stores or clears it in SecretStorage. A personal token is not a model-provider key.
3. `LoopForge: Select Project` and `LoopForge: Select Workflow` discover choices from the configured server, or set `loopforge.projectId` and `loopforge.workflowId` in workspace settings. A project owner or authenticated user is required for manual runs.

When switching projects with `LoopForge: Select Project`, the extension clears the workspace's workflow selection so the next run asks for a workflow in the new project. If you edit these settings manually, choose a workflow that belongs to the selected project or is shared with it.

The extension only writes the two explicitly selected LoopForge workspace settings. It does not change unrelated VS Code settings. Uninstalling removes the extension files and leaves the workspace settings and SecretStorage entry under the user's control; clear the token with `LoopForge: Configure API Token` when desired.

## Commands and run view

The LoopForge activity bar view lists recent runs, their status and active stage, waiting human approvals, and artifact metadata. The commands provide:

- Start a workflow with current project/file/selection context.
- Refresh and inspect status, stage output, and logs.
- Approve or send back a stage that is waiting for human input. The client includes the node attempt expectation so stale approval cards are rejected by the server.
- Open an artifact in a VS Code editor.
- Open a changed file from the run workspace in a VS Code editor.
- Open the current workspace change as a VS Code diff. If there are multiple changes, the extension asks which path to compare.

Connection, authentication, timeout, HTTP, and server validation errors are shown in the Runs view or as VS Code error messages. Run polling is bounded by `loopforge.pollInterval` and is disabled on deactivation. To upgrade, install the new VSIX through VS Code; to uninstall, use the Extensions view. Existing LoopForge settings may remain after uninstall; remove them manually if desired.

## Capability boundary

The first release is a platform-connected adapter. It does not claim to execute a workflow locally, provide a model/provider setup wizard, or replace the LoopForge Web workspace. Those remain platform responsibilities and are reported by the same API error messages used by the Web client.

Changed files and diffs are available only while the platform retains the run workspace. In the current platform, a successful or cancelled ordinary workflow can have its workspace reclaimed immediately, so open its changes before completing the workflow when they are needed. After reclamation the extension reports the platform's reason; artifacts stored by the platform remain separately accessible. Workspace snapshots and restoration depend on the platform and are not provided by this extension.

If a connection fails, check that the platform is running and that `loopforge.serverUrl` is reachable from the machine hosting the extension. In a Remote SSH session, `127.0.0.1` refers to the remote extension host, not the desktop running the VS Code window.

## Tests

Run `npm test` in this directory for extension interaction and client tests. To exercise the client against the actual platform HTTP handlers and workflow engine using an isolated in-memory test database, build the repository's web assets as described in the [contributing guide](../CONTRIBUTING.md), then run `go test -v ./vscode-extension/acceptance` from the repository root. The acceptance workflow is a single human approval step and does not need Docker, organization credentials, or a model provider.
