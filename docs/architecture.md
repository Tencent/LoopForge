# Architecture

The platform keeps the upstream Go module name `agentflow`, package boundaries and `AGENTFLOW_` environment variables to reduce ongoing merge conflicts.

| Directory | Responsibility |
| --- | --- |
| `cmd/agentflow` | API, worker or combined process |
| `cmd/agentflow-runner` | Personal and pool execution machines |
| `cmd/agentflow-mcp`, `cmd/agentflow-gate-mcp` | Public MCP and structured gate results |
| `internal/engine` | LOOP graph, leases, scheduling, sandbox lifecycle and delivery. Ready branches execute together in the run workspace; rerunning one branch after a cancel restores cancelled siblings |
| `internal/repo` | MySQL persistence, one repository per business domain |
| `internal/httpapi` | HTTP/MCP handlers, one handler per business domain |
| `internal/runtime`, `internal/runner` | Executor protocols, credentials, isolation and artifacts |
| `internal/tracker` | Remote issue/PR adapters |
| `web` | Embedded upstream web interface, preferences and public sign-in |
| `migrations` | Explicit data migrations and optional public catalog |
| `local` | Local workflows and host integrations; user guides live in `docs/local/` |

The public edition adds account/password login and GitHub adapters. These feed the existing user, token, repository and delivery contracts; there is no separate public engine or in-memory production store. A remote repository identity beginning `github:` distinguishes GitHub owner/repository names from legacy tracker identities. The upstream `gongfengProjectId` JSON field carries this identity for compatibility.

The CLI's Classic and Portable workflow assets remain separate from each other and from platform role definitions. Sharing behavior contracts is preferable to copying runtime assets. No classic host bundle is required by a platform worker.

## Managed Agents and execution environments

An LOOP role chooses an executor. A CLI role runs in the workspace selected for the run. A WorkBuddy Managed Agent or A2A role sends a text turn to a configured Agent and publishes the reply as `result.md`. Downstream nodes get that artifact through the existing artifact handoff. A remote Agent keeps its own workspace and tool configuration.

| Role executor | Target | How the target is selected |
| --- | --- | --- |
| Existing CLI | Personal Runner | The project or personal default machine, frozen when the run starts |
| Existing CLI | OpenSandbox | The project's configured Docker or Kubernetes lifecycle server, when the run has no default machine and uses an explicit `native` sandbox profile |
| Existing CLI | Existing local runtime or cloud Runner pool | The existing scheduling rules for runs outside OpenSandbox |
| WorkBuddy Managed Agent | Existing WMA session | A server binding for the project and responsible user |
| External A2A Agent | User machine or CVM | The responsible user's personal Runner, which calls its configured loopback service |
| External A2A Agent | Cloud HTTPS service | A server binding for the project and responsible user, explicitly selected on the role |

Managed Agent dispatch records its attempt before sending the turn. An attempt with an unknown remote outcome — an interrupted turn or one that ran past its budget — requires an explicit retry. The node fails instead of consuming its remaining attempts. One WMA or A2A turn may run for up to five minutes; the connector then cancels the remote task, fails the node with a timeout and, because cancellation is only advisory, does not resend the turn on its own. The CLI and sandbox paths retain their normal task leases, workspace handoff, and result handling.

### WorkBuddy Managed Agents

Choose **WorkBuddy Managed Agent** in an LOOP role or set `"executorId":"wma"` through `PUT /api/agents`. Configure the model, tools, and access on the Agent version in WMA. Use a dedicated existing session. [WorkBuddy Agent access guide](https://cloud.tencent.com/document/product/1831/140759)

| Server environment variable | Value |
| --- | --- |
| AGENTFLOW_WMA_PROJECT | LoopForge project ID |
| AGENTFLOW_WMA_USER_ID | LoopForge responsible user ID |
| AGENTFLOW_WMA_AGENT_ID | Published Agent ID |
| AGENTFLOW_WMA_SESSION_ID | Existing dedicated session ID |
| AGENTFLOW_WMA_ACP_ORIGIN | Optional exact HTTPS origin for an approved private ACP gateway |
| AGENTFLOW_WMA_SECRET_ID, AGENTFLOW_WMA_SECRET_KEY | Tencent Cloud credentials for the enterprise identity |
| AGENTFLOW_WMA_REGION | Tencent Cloud region, default `ap-beijing` |
| AGENTFLOW_WMA_SECURITY_TOKEN | Optional token for temporary Tencent Cloud credentials |
| AGENTFLOW_WMA_ENTERPRISE_ID, AGENTFLOW_WMA_API_KEY | WorkBuddy enterprise ID and `ck_` or `pt_` key, for WorkBuddy OpenAPI authentication |
| AGENTFLOW_WMA_ENDPOINT | OpenAPI control-plane origin for a dedicated tenant. SaaS defaults to `https://www.workbuddy.cn`. |

Choose one control-plane authentication method. Both methods use the returned ACP endpoint and an access token for the data plane. The configured session is reused. A worker serializes prompts to the session until each request settles.

### External A2A Agents

Choose **自带 Agent（A2A）** in the LOOP role and select the personal Runner or cloud HTTPS target. Through the role API, use `"executorId":"a2a"` and `"externalAgentTarget":"runner"` or `"remote"`. The default A2A target is the personal Runner. The service uses the A2A v0.3 JSON-RPC binding. The connector sends a text message, follows asynchronous tasks with `tasks/get`, and requests cancellation when it knows the task ID. [A2A v0.3 specification](https://a2a-protocol.org/v0.3.0/specification/)

For a user's machine or cloud VM, bind the LoopForge Runner, share it with the project and make it the responsible user's execution machine. Place the following configuration at `~/.agentflow-runner/a2a.json` and restart the Runner. The endpoint uses a literal loopback address, so the Agent can stay on a local port. An optional `token` is sent only to this endpoint. `AGENTFLOW_RUNNER_A2A_ENDPOINT` and `AGENTFLOW_RUNNER_A2A_TOKEN` can set the same values in a service environment.

```json
{"endpoint":"http://127.0.0.1:9000/a2a"}
```

For a cloud HTTPS Agent, set `AGENTFLOW_A2A_BINDINGS_FILE` to a deployment-managed JSON file. A binding applies to one project and responsible user. The file can contain multiple entries. Include an optional `token` when the service requires bearer authentication.

```json
[
  {
    "projectId": "pilot-project",
    "responsibleUserId": 42,
    "endpoint": "https://agent.example.com/a2a"
  }
]
```

Managed text roles can reference earlier node replies with `{{upstream}}` or `{{node.<id>.output}}`. Their model and tools are set in the Agent service. Use a CLI role for worktree operations, verification gates, per-repository execution, and file-based artifact contracts.

### OpenSandbox on Docker and Kubernetes

Run an OpenSandbox lifecycle server for each backend. Both runtimes expose the same lifecycle API to LoopForge. [Docker runtime configuration](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.2.0/server/README.md) · [Kubernetes Helm deployment](https://github.com/opensandbox-group/OpenSandbox/blob/release-1.2.0/manifests/HELM-DEPLOYMENT.md)

Prepare a Runner image with Git and the CLI tools used by the projects. The base image below can run script-only workflows.

```sh
runner_context=$(mktemp -d)
CGO_ENABLED=0 go build -o "$runner_context/agentflow-runner" ./cmd/agentflow-runner
docker build -t loopforge-opensandbox-runner:pilot -f deploy/opensandbox-runner.Dockerfile "$runner_context"
```

Use `AGENTFLOW_OPENSANDBOX_BINDINGS_FILE` to configure both backends in one deployment. Each entry has `id`, `projectIds`, `endpoint`, `apiKey`, `runnerUrl`, `runnerImage`, `scope`, and `runnerSigningKey`. Use a stable backend ID and a distinct scope for each deployment and backend. The signing key must be at least 32 bytes and match on the LoopForge API and worker processes. The Runner URL must be reachable from sandbox containers. Assign each project to one backend.

In a configured project, select a `native` profile explicitly on the LOOP. OpenSandbox handles its workspace when the project has no default machine and uses fixed placement. The Runner inherits environment variables from the selected profile. Use the backend deployment to set the container image and resource policies.

A single-backend deployment can use `AGENTFLOW_OPENSANDBOX_PROJECTS`, `AGENTFLOW_OPENSANDBOX_ENDPOINT`, `AGENTFLOW_OPENSANDBOX_API_KEY`, `AGENTFLOW_OPENSANDBOX_RUNNER_URL`, `AGENTFLOW_OPENSANDBOX_RUNNER_IMAGE`, `AGENTFLOW_OPENSANDBOX_SCOPE`, and `AGENTFLOW_OPENSANDBOX_RUNNER_SIGNING_KEY`.

LoopForge persists the chosen backend, reserves one sandbox per shared LOOP workspace, and waits for the workspace-scoped Runner to register. The container provides isolation. The dedicated Runner executes inside it. The backend releases the sandbox after artifact collection on successful runs and during cancellation cleanup. Failed and paused runs can reuse their original workspace before the provider's two-hour expiration. The workspace is ephemeral. Long-lived workspaces require persistent storage and recovery policies.

The integration tests use the lifecycle HTTP contract and the real Runner. To run a mixed LOOP against a disposable Docker or Kubernetes backend, set `LF_OPENSANDBOX_E2E_URL`, `LF_OPENSANDBOX_E2E_API_KEY`, `LF_OPENSANDBOX_E2E_IMAGE`, and `LF_OPENSANDBOX_E2E_BIND_HOST`. The bind host is the isolated test network interface that the Runner can reach. Set `LF_WMA_E2E_AGENT_ID` and `LF_WMA_E2E_SESSION_ID` with the WMA Tencent Cloud environment variables to run a real turn in the mixed LOOP. For A2A, the tests can use services configured through `LF_A2A_E2E_RUNNER_URL` and `LF_A2A_E2E_REMOTE_URL` with their matching `_TOKEN` and `_CA` variables.

```sh
go test ./internal/managedagent ./internal/runner -run 'TestWMA|TestA2A|TestExternalA2ASOP|TestOpenSandboxMixedSOP'
```
