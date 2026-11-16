"""Run actual Windows Runner tasks in a local Linux Docker daemon.

Prerequisites: Docker Desktop/another local Linux daemon, Go, Node.js, Git for
Windows, and the three helper paths used by test-windows-runner-lifecycle.py.
The default fixture image installs a pinned real Codex CLI. Model responses use
an in-process, zero-cost gateway; no provider account or real key is used.

Run: python scripts/test-windows-docker-runner.py -v
Set LOOPFORGE_DOCKER_TEST_IMAGE to reuse a prepared image containing codex,
node, git, python3, ssh-agent, ssh-add and sshd. The image is checked before use.
"""

import importlib.util
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import threading
import time
import unittest
import uuid
import http.server
import hashlib
from urllib.parse import urlsplit


HERE = Path(__file__).resolve().parent
SOURCE_ROOT = HERE.parent
_spec = importlib.util.spec_from_file_location("windows_runner_fixture", HERE / "test-windows-runner-lifecycle.py")
fixture = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(fixture)

MODEL_KEY = "loopforge-docker-local-model-fixture"
MODEL_NAME = "docker-fixture-model"
CODEX_VERSION = os.environ.get("LOOPFORGE_DOCKER_CODEX_VERSION", "0.162.1")


class ModelGateway(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self):
        # Linux containers reach this Windows listener through host.docker.internal.
        super().__init__(("0.0.0.0", 0), ModelHandler)
        self.lock = threading.Lock()
        self.requests = []
        self.errors = []


class ModelHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        try:
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            if self.path != "/v1/responses" or self.headers.get("Authorization") != "Bearer " + MODEL_KEY:
                raise ValueError("unexpected model endpoint or credential")
            if body.get("model") != MODEL_NAME:
                raise ValueError("unexpected model")
            with self.server.lock:
                self.server.requests.append(body)
                number = len(self.server.requests)
            if number == 1:
                names = {tool.get("name") for tool in body.get("tools", [])}
                tool = next((name for name in ("shell_command", "exec_command") if name in names), None)
                if not tool:
                    raise ValueError("real Codex did not expose a shell tool")
                override = getattr(self.server, "shell_command", None)
                command = override or "node -e 'require(\"fs\").writeFileSync(\"codex-container.txt\",\"REAL_CONTAINER_CODEX\")'"
                arguments = {"command": command, "workdir": "/workspace"}
                if override:
                    arguments["timeout_ms"] = 60000
                if tool == "exec_command":
                    arguments = {"cmd": command, "workdir": "/workspace", "yield_time_ms": 1000}
                item = {"id": "fc_docker", "type": "function_call", "call_id": "call_docker",
                        "name": tool, "arguments": json.dumps(arguments), "status": "completed"}
            else:
                release = getattr(self.server, "force_stop_release", None)
                if release and not release.wait(timeout=60):
                    raise ValueError("forced-stop model fixture was not released")
                item = {"id": "msg_docker", "type": "message", "role": "assistant", "status": "completed",
                        "content": [{"type": "output_text", "text": "REAL_DOCKER_CODEX_FINISHED", "annotations": []}]}
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for event, payload in (
                ("response.created", {"response": {"id": "docker-response-" + str(number), "status": "in_progress", "output": []}}),
                ("response.output_item.added", {"output_index": 0, "item": item}),
                ("response.output_item.done", {"output_index": 0, "item": item}),
                ("response.completed", {"response": {"id": "docker-response-" + str(number), "object": "response", "status": "completed",
                                                    "model": MODEL_NAME, "output": [item],
                                                    "usage": {"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}}}),
            ):
                payload["type"] = event
                self.wfile.write(("event: " + event + "\ndata: " + json.dumps(payload) + "\n\n").encode())
                self.wfile.flush()
        except Exception as error:
            if getattr(self.server, "allow_client_disconnect", False) and isinstance(error, (BrokenPipeError, ConnectionResetError)):
                return
            with self.server.lock:
                self.server.errors.append(repr(error))
            if not self.wfile.closed:
                try:
                    self.send_error(500, "local fixture error")
                except OSError:
                    pass


@unittest.skipUnless(os.name == "nt", "requires Windows with a real Linux Docker daemon")
class WindowsDockerRunnerTest(fixture.WindowsRunnerLifecycleTest):
    # Reuse process/platform plumbing without re-running the native/installer suite.
    test_actual_installer_connect_update_uninstall_powershell_51 = None
    test_actual_installer_connect_update_uninstall_powershell_7 = None
    test_actual_installer_rebind_powershell_51 = None
    test_actual_installer_rebind_powershell_7 = None
    test_actual_installer_waits_for_delayed_registration_failure = None
    test_actual_rebind_keeps_issued_session_when_config_commit_fails = None
    test_real_tasks_cancel_and_resume_after_authenticated_reconnect = None

    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        cls.docker_binary = shutil.which("docker.exe")
        if not cls.docker_binary:
            raise RuntimeError("Docker CLI is missing; install/start the local Linux Docker environment first")
        endpoint = os.environ.get("DOCKER_HOST")
        if not endpoint:
            result = subprocess.run([cls.docker_binary, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}"],
                                    capture_output=True, text=True, timeout=20)
            if result.returncode:
                raise RuntimeError("cannot resolve local Docker endpoint: " + result.stderr)
            endpoint = result.stdout.strip()
        if not endpoint.startswith(("npipe://", "tcp://127.0.0.1:", "tcp://localhost:")):
            raise RuntimeError("acceptance requires a local Windows Docker endpoint, got " + endpoint)
        cls.docker_environment = os.environ.copy()
        cls.docker_environment.update({"DOCKER_HOST": endpoint, "DOCKER_CONFIG": str(Path(cls.fixture_temp.name) / "empty-docker-config")})
        cls.docker_environment.pop("DOCKER_CONTEXT", None)
        cls.docker_environment.pop("DOCKER_TLS_VERIFY", None)
        Path(cls.docker_environment["DOCKER_CONFIG"]).mkdir()
        # Per-user Docker Desktop keeps buildx outside system plugin locations.
        # Expose only this installed build tool; unrelated Desktop plugins can
        # require user configuration or start background services on discovery.
        plugins = Path(cls.docker_binary).resolve().parent.parent / "cli-plugins"
        if (plugins / "docker-buildx.exe").is_file():
            isolated_plugins = Path(cls.docker_environment["DOCKER_CONFIG"]) / "cli-plugins"
            isolated_plugins.mkdir()
            shutil.copy2(plugins / "docker-buildx.exe", isolated_plugins / "docker-buildx.exe")
        proxy = os.environ.get("LOOPFORGE_DOCKER_TEST_HTTP_PROXY")
        if proxy:
            parsed_proxy = urlsplit(proxy)
            if parsed_proxy.scheme != "http" or not parsed_proxy.hostname or parsed_proxy.username or parsed_proxy.password:
                raise ValueError("test HTTP proxy must be an explicit credential-free HTTP endpoint")
            # This opt-in affects only the disposable test client's build/run
            # transport. Keep local model fixtures and npm TLS traffic direct.
            proxy_config = {"proxies": {"default": {"httpProxy": proxy, "noProxy":
                "localhost,127.0.0.1,::1,host.docker.internal,registry.npmjs.org,registry.npmmirror.com,.npmjs.org,.npmmirror.com"}}}
            (Path(cls.docker_environment["DOCKER_CONFIG"]) / "config.json").write_text(json.dumps(proxy_config), encoding="utf-8")
        info = cls.docker("info", "--format", "{{.OSType}}", timeout=30)
        if info.strip() != "linux":
            raise RuntimeError("Docker must run Linux containers, got " + info.strip())
        source = (SOURCE_ROOT / "internal/runtime/sandbox_docker.go").read_text(encoding="utf-8")
        match = re.search(r'^const IsolatedDockerfile = ("[^\n]+")$', source, re.MULTILINE)
        if not match:
            raise RuntimeError("could not read the production isolated image definition")
        cls.frozen_dockerfile = json.loads(match.group(1))
        cls.image = os.environ.get("LOOPFORGE_DOCKER_TEST_IMAGE")
        if not cls.image:
            cls.image = "loopforge-windows-docker-test:" + uuid.uuid4().hex
            build = Path(cls.fixture_temp.name) / "docker-build"
            build.mkdir()
            shutil.copy2(cls.source_linux_gate, build / "gate-mcp")
            (build / "Dockerfile").write_text(
                "FROM node:22-bookworm\n"
                "RUN apt-get update && apt-get install -y --no-install-recommends git openssh-client openssh-server python3 ripgrep ca-certificates curl "
                "&& rm -rf /var/lib/apt/lists/*\n"
                "RUN npm install --global --no-audit --no-fund @openai/codex@" + CODEX_VERSION + "\n"
                "COPY gate-mcp /usr/local/bin/agentflow-gate-mcp\nRUN chmod 755 /usr/local/bin/agentflow-gate-mcp\n"
                "WORKDIR /workspace\n", encoding="utf-8")
            cls.addClassCleanup(lambda: cls.docker("image", "rm", "-f", cls.image, check=False, timeout=60))
            cls.docker("build", "--progress=plain", "-t", cls.image, str(build), timeout=900)
        checked = cls.docker("run", "--rm", "--entrypoint", "sh", cls.image, "-ec",
                             "node --version; git --version; python3 --version; command -v ssh-agent; command -v ssh-add; test -x /usr/sbin/sshd; codex --version; "
                             "test -x /usr/local/bin/agentflow-gate-mcp", timeout=60)
        if "codex-cli " + CODEX_VERSION not in checked:
            raise RuntimeError("fixture must use pinned real Codex " + CODEX_VERSION + ": " + checked)

    @classmethod
    def docker(cls, *arguments, check=True, timeout=30):
        result = subprocess.run([cls.docker_binary, *arguments], env=cls.docker_environment,
                                capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=timeout)
        if check and result.returncode:
            raise RuntimeError("docker " + " ".join(arguments) + " failed:\n" + result.stdout + result.stderr)
        return result.stdout + result.stderr

    def setUp(self):
        super().setUp()
        self.run_id = "windows-docker-" + uuid.uuid4().hex[:16]
        self.workspace = self.root / "workspace" / "runs" / self.run_id / "workspace"
        self.workspace.mkdir(parents=True)
        # The Linux helper is a real compiled ELF; production copies it into the
        # script-view companion and checks its protocol version before execution.
        shutil.copy2(self.source_gate, self.binary.with_name("agentflow-gate-mcp.exe"))
        shutil.copy2(self.source_linux_gate, self.binary.with_name("agentflow-gate-mcp-linux-" + self.arch))
        self.gateway = ModelGateway()
        self.gateway_worker = threading.Thread(target=self.gateway.serve_forever, daemon=True)
        self.gateway_worker.start()
        self.addCleanup(self.close_gateway)
        self.addCleanup(self.cleanup_containers)

    def close_gateway(self):
        self.gateway.shutdown()
        self.gateway.server_close()
        self.gateway_worker.join(timeout=5)

    def container_ids(self):
        output = self.docker("ps", "-aq", "--filter", "name=^/agentflow-rt-run-" + self.run_id + "-", timeout=20)
        return output.split()

    def cleanup_containers(self):
        for process, _ in self.processes:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=10)
        ids = self.container_ids()
        if ids:
            self.docker("rm", "-f", *ids, timeout=40)
        self.assertEqual(self.container_ids(), [], "test-owned Docker containers leaked")

    def isolated_environment(self):
        environment = super().isolated_environment()
        environment["PATH"] += os.pathsep + str(Path(self.docker_binary).parent)
        environment.update({"DOCKER_HOST": self.docker_environment["DOCKER_HOST"],
                            "DOCKER_CONFIG": self.docker_environment["DOCKER_CONFIG"],
                            "AGENTFLOW_ISOLATED_IMAGE": self.image,
                            "AGENTFLOW_GATE_MCP_BINARY": str(self.binary.with_name("agentflow-gate-mcp.exe"))})
        return environment

    def await_condition(self, predicate, process, message):
        deadline = time.monotonic() + 150
        with self.server.condition:
            while not predicate():
                if (process is not None and process.poll() is not None) or time.monotonic() >= deadline:
                    self.fail(message + "\n" + self.diagnostics())
                self.server.condition.wait(timeout=0.1)

    def docker_task(self, task_id, command, outputs=()):
        return {"id": task_id, "runnerId": fixture.RUNNER_ID, "provider": "terminal",
                "workspace": "runs/" + self.run_id + "/workspace", "runId": self.run_id,
                "attempt": 1, "maxAttempts": 1, "leaseUntil": self.server.expiry, "command": command,
                "sandbox": {"profile": {"id": "real-windows-docker", "driver": "docker"},
                            "dockerfile": {"id": "isolated", "content": self.frozen_dockerfile},
                            "requirements": {"privateHome": True}},
                "invocation": {"reportOutputs": True, "outputFiles": list(outputs),
                               "artifacts": [{"name": path, "path": path, "required": True} for path in outputs]}}

    def assert_success(self, result):
        self.assertEqual(result["status"], "success", str(result) + "\n" + self.diagnostics())

    def assert_container_process_gone(self, container, identity):
        probe = "const fs=require('fs');try{const s=fs.readFileSync('/proc/'+process.argv[1]+'/stat','utf8').replace(/^.*\\) /,'').split(' ');if(s[19]===process.argv[2]){console.error(JSON.stringify({pid:process.argv[1],state:s[0],start:s[19]}));process.exit(1)}}catch(e){process.exit(e.code==='ENOENT'?0:2)}"
        self.docker("exec", container, "node", "-e", probe, str(identity["pid"]), identity["start"])

    def run_runtime_acceptance(self, pattern):
        go = os.environ.get("LOOPFORGE_TEST_GO_BINARY") or shutil.which("go.exe")
        caches = subprocess.run([go, "env", "-json", "GOMODCACHE", "GOCACHE", "GOROOT"],
                                capture_output=True, text=True, timeout=20, check=True)
        environment = self.isolated_environment()
        environment.update(json.loads(caches.stdout))
        environment.update({"GOTOOLCHAIN": "local", "CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off",
                            "LOOPFORGE_WINDOWS_DOCKER_LIVE": "1"})
        print("Running actual container acceptance: " + pattern, flush=True)
        result = subprocess.run([go, "test", "./internal/runtime", "-run", pattern, "-count=1", "-timeout=25m", "-v"],
                                cwd=SOURCE_ROOT, env=environment, capture_output=True, text=True,
                                encoding="utf-8", errors="replace", timeout=1800)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("--- SKIP", result.stdout, "live Docker acceptance was not executed")
        print(result.stdout, end="", flush=True)

    def test_real_docker_browser_runtime(self):
        self.run_runtime_acceptance("^TestWindowsLiveDockerBrowserEntrypoint$")

    def test_real_docker_conversation_runtime(self):
        self.run_runtime_acceptance("^TestWindowsLiveDockerConversation")

    def test_real_docker_personal_gitssh_runtime(self):
        self.run_runtime_acceptance("^TestWindowsLiveDockerPersonalGitSSHIsolationAndCleanup$")

    def test_real_docker_tasks_gate_codex_cancel_and_continue(self):
        process = self.start_runner(1)
        first = self.docker_task("docker-shell-first",
                                 "set -eu; test \"$(uname -s)\" = Linux; printf REAL_DOCKER > /tmp/loopforge-runtime-retained; "
                                 "printf HOST_BIND_MOUNT > docker-result.txt; hostname > container-hostname.txt; echo REAL_DOCKER_SHELL",
                                 ["docker-result.txt", "container-hostname.txt"])
        result = self.run_task(process, first)
        self.assert_success(result)
        self.assertEqual(result["artifacts"]["docker-result.txt"], "HOST_BIND_MOUNT")
        self.assertEqual((self.workspace / "docker-result.txt").read_text(), "HOST_BIND_MOUNT")

        gate_frames = [
            {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18"}},
            {"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "submit_gate_result", "arguments": {"decision": "pass", "reason": "real Docker gate acceptance"}}},
        ]
        gate_input = "\n".join(json.dumps(frame) for frame in gate_frames) + "\n"
        gate_command = "printf %s " + shlex.quote(gate_input) + " | AGENTFLOW_GATE_RESULT_PATH=docker-gate.json /usr/local/bin/agentflow-gate-mcp"
        result = self.run_task(process, self.docker_task("docker-gate", gate_command, ["docker-gate.json"]))
        self.assert_success(result)
        self.assertEqual(json.loads(result["artifacts"]["docker-gate.json"])["decision"], "pass")

        codex = self.docker_task("docker-real-codex", "", ["codex-container.txt"])
        codex["provider"] = "codex"
        codex["prompt"] = "Use the requested fixture shell tool once and finish."
        codex["invocation"].update({"runId": self.run_id, "nodeId": "codex", "invocationId": "docker-real-codex",
                                    "executor": {"kind": "codex", "binary": "codex", "model": MODEL_NAME,
                                                 "args": ["exec", "--json", "--skip-git-repo-check", "--ephemeral", "-c", "features.plugins=false", "-c", "features.code_mode=false", "-c", "features.unified_exec=false"]},
                                    "agent": {"id": "docker-acceptance", "name": "Docker acceptance"},
                                    "credentialSource": "personal", "codexApiKey": MODEL_KEY,
                                    "codexBaseUrl": "http://host.docker.internal:%d/v1" % self.gateway.server_port})
        result = self.run_task(process, codex)
        self.assert_success(result)
        self.assertEqual(result["artifacts"]["codex-container.txt"], "REAL_CONTAINER_CODEX")
        self.assertGreaterEqual(len(self.gateway.requests), 2)
        self.assertEqual(self.gateway.errors, [])

        containers = self.docker("ps", "--format", "{{.Names}}", "--filter", "name=^/agentflow-rt-run-" + self.run_id + "-").split()
        canonical = [name for name in containers if not name.endswith(("-scripts", "-browser"))]
        self.assertEqual(len(canonical), 1)
        service_source = "const fs=require('fs');const role=process.argv[2];fs.writeFileSync(role+'.tmp',JSON.stringify({pid:process.pid,start:fs.readFileSync('/proc/self/stat','utf8').replace(/^.*\\) /,'').split(' ')[19]}));fs.renameSync(role+'.tmp',role+'.json');setInterval(()=>{},1000);"
        (self.workspace / "service.js").write_text(service_source, encoding="utf-8")
        warm = self.docker_task("docker-success-background-service",
                               "node service.js warm-service >/dev/null 2>&1 </dev/null & while [ ! -f warm-service.json ]; do sleep 0.02; done; printf WARM_SERVICE_STARTED")
        self.assert_success(self.run_task(process, warm))
        warm_identity = json.loads((self.workspace / "warm-service.json").read_text())
        warm_probe = "const fs=require('fs');const i=JSON.parse(fs.readFileSync('warm-service.json'));const s=fs.readFileSync('/proc/'+i.pid+'/stat','utf8').replace(/^.*\\) /,'').split(' ');if(s[19]!==i.start||s[0]==='Z')process.exit(1);"
        self.docker("exec", canonical[0], "node", "-e", warm_probe)

        failure = self.docker_task("docker-expected-failure",
                                   "node service.js failed-service >/dev/null 2>&1 </dev/null & while [ ! -f failed-service.json ]; do sleep 0.02; done; printf DOCKER_FAILURE >&2; exit 7")
        result = self.run_task(process, failure)
        self.assertEqual(result["status"], "failed", result)
        self.assertEqual(result["exitCode"], 7, result)
        self.assert_container_process_gone(canonical[0], json.loads((self.workspace / "failed-service.json").read_text()))
        self.docker("exec", canonical[0], "node", "-e", warm_probe)

        tree = self.workspace / "process-tree.js"
        tree_source = fixture.NODE_PROCESS_TREE.replace("windowsHide: true", "detached: true")
        tree_source = tree_source.replace("{pid: process.pid, role}",
                                         "{pid: process.pid, role, start: fs.readFileSync('/proc/self/stat', 'utf8').replace(/^.*\\) /, '').split(' ')[19]}")
        tree.write_text(tree_source, encoding="utf-8")
        cancel = self.docker_task("docker-cancel-tree", "node process-tree.js")
        self.server.enqueue(cancel)
        receipts = [self.workspace / (role + ".pid.json") for role in ("parent", "child", "grandchild")]
        self.await_condition(lambda: all(path.exists() for path in receipts), process, "Docker process tree did not start")
        identities = [json.loads(path.read_text()) for path in receipts]
        self.assertTrue(all(item.get("start", "").isdigit() for item in identities))
        containers = self.docker("ps", "--format", "{{.Names}}", "--filter", "name=^/agentflow-rt-run-" + self.run_id + "-").split()
        canonical = [name for name in containers if not name.endswith(("-scripts", "-browser"))]
        self.assertEqual(len(canonical), 1, "cancellation fixture must own exactly one canonical PID namespace")
        with self.server.condition:
            self.server.desired_cancel.add(cancel["id"])
        self.await_condition(lambda: cancel["id"] in self.server.completed, process, "Docker cancellation did not return")
        self.assertEqual(self.server.completed[cancel["id"]]["status"], "canceled")
        for identity in identities:
            # Match kernel start time as well as PID, so fast PID reuse cannot
            # falsely report an unrelated process as an escaped descendant.
            self.assert_container_process_gone(canonical[0], identity)
        self.docker("exec", canonical[0], "node", "-e", warm_probe)
        # Cross the deferred side-effect deadline: a quick marker check misses
        # detached processes that survive docker exec's client disconnect.
        time.sleep(11)
        self.assertEqual(list(self.workspace.glob("orphan-*")), [], "a detached container descendant survived cancellation")

        continued = self.docker_task("docker-live-continuation",
                                     "set -eu; test \"$(cat /tmp/loopforge-runtime-retained)\" = REAL_DOCKER; "
                                     "test \"$(cat docker-result.txt)\" = HOST_BIND_MOUNT; printf LIVE_CONTAINER_CONTINUED > continued.txt",
                                     ["continued.txt"])
        result = self.run_task(process, continued)
        self.assert_success(result)
        self.assertEqual(result["artifacts"]["continued.txt"], "LIVE_CONTAINER_CONTINUED")
        self.docker("exec", canonical[0], "node", "-e", warm_probe)
        # The real slim image has no Python. Reject it before the requested
        # shell can touch the Windows bind, with a useful dependency message.
        missing = self.docker_task("docker-missing-python", "printf SHOULD_NOT_RUN > missing-python-side-effect.txt")
        missing_content = "FROM node:22-bookworm-slim\nLABEL org.loopforge.acceptance=" + self.run_id + "\nWORKDIR /workspace\n"
        missing["sandbox"]["profile"]["id"] = "missing-python"
        missing["sandbox"]["dockerfile"] = {"id": "missing-python", "content": missing_content}
        missing_image = "agentflow-img-" + hashlib.sha256((missing_content + "\0").encode()).hexdigest()[:16]
        self.addCleanup(lambda: self.docker("image", "rm", "-f", missing_image, check=False))
        result = self.run_task(process, missing)
        self.assertEqual(result["status"], "failed", result)
        self.assertIn("requires Python 3", result.get("result", "") + result.get("error", ""), result)
        self.assertFalse((self.workspace / "missing-python-side-effect.txt").exists())
        saved_state = self.state.read_bytes()
        self.stop_runner(process)
        self.assertEqual(self.container_ids(), [], "graceful shutdown retained a test container")
        restarted = self.start_runner(2)
        resumed = self.docker_task("docker-workspace-restart", "test -f continued.txt && printf WORKSPACE_RETAINED > restarted.txt", ["restarted.txt"])
        result = self.run_task(restarted, resumed)
        self.assert_success(result)
        self.assertEqual(result["artifacts"]["restarted.txt"], "WORKSPACE_RETAINED")
        self.assertEqual(self.state.read_bytes(), saved_state)
        self.assertEqual(self.server.count("/api/runners/register"), 1)
        self.stop_runner(restarted)
        self.assertEqual(self.container_ids(), [])
        self.assertEqual(self.server.errors, [], self.diagnostics())
        self.assertNotIn(MODEL_KEY, self.diagnostics())

    def exercise_real_docker_forced_runner_stop(self, codex=False):
        print("FORCED_STOP_FIXTURE_OWNER " + json.dumps({"runId": self.run_id, "provider": "codex" if codex else "terminal"}), flush=True)
        process = self.start_runner(1)
        warm_source = r'''const fs=require('fs');
const stat=fs.readFileSync('/proc/self/stat','utf8').replace(/^.*\) /,'').split(' ');
fs.writeFileSync('force-warm-service.tmp',JSON.stringify({pid:process.pid,start:stat[19]}));
fs.renameSync('force-warm-service.tmp','force-warm-service.json');
fs.appendFileSync('force-warm-service-ticks.txt','started\n');
setInterval(()=>fs.appendFileSync('force-warm-service-ticks.txt','.'),100);
'''
        (self.workspace / "force-warm-service.js").write_text(warm_source, encoding="utf-8")
        warm = self.docker_task(
            "docker-force-stop-completed-service",
            "node force-warm-service.js >/dev/null 2>&1 </dev/null & "
            "while [ ! -f force-warm-service.json ]; do sleep 0.02; done; printf WARM_SERVICE_COMPLETED")
        self.assert_success(self.run_task(process, warm))
        warm_identity = json.loads((self.workspace / "force-warm-service.json").read_text())
        containers = self.docker("ps", "--no-trunc", "--format", "{{.ID}}|{{.Names}}",
                                 "--filter", "name=^/agentflow-rt-run-" + self.run_id + "-").split()
        fixture_containers = [dict(zip(("id", "name"), entry.split("|", 1))) for entry in containers]
        canonical = [entry["name"] for entry in fixture_containers if not entry["name"].endswith(("-scripts", "-browser"))]
        self.assertEqual(len(canonical), 1, "forced-stop fixture must own one canonical PID namespace")
        ticks_before = (self.workspace / "force-warm-service-ticks.txt").stat().st_size

        tree_source = fixture.NODE_PROCESS_TREE.replace("windowsHide: true", "detached: true")
        tree_source = tree_source.replace(
            "{pid: process.pid, role}",
            "{pid: process.pid, role, start: fs.readFileSync('/proc/self/stat', 'utf8').replace(/^.*\\) /, '').split(' ')[19]}")
        (self.workspace / "force-process-tree.js").write_text(tree_source, encoding="utf-8")
        active = self.docker_task("docker-force-stop-active-tree", "node force-process-tree.js")
        if codex:
            self.gateway.shell_command = "node force-process-tree.js"
            # exec_command can yield a live session. Keep the real CLI waiting
            # on this local model until the owner has died and cleanup is probed.
            self.gateway.force_stop_release = threading.Event()
            self.gateway.allow_client_disconnect = True
            self.addCleanup(self.gateway.force_stop_release.set)
            active["id"] = "docker-force-stop-codex-tree"
            active["provider"] = "codex"
            active["prompt"] = "Use the requested fixture shell tool once and finish."
            active["command"] = ""
            active["invocation"].update({
                "runId": self.run_id, "nodeId": "codex", "invocationId": active["id"],
                "executor": {"kind": "codex", "binary": "codex", "model": MODEL_NAME,
                             "args": ["exec", "--json", "--skip-git-repo-check", "--ephemeral",
                                      "-c", "features.plugins=false", "-c", "features.code_mode=false",
                                      "-c", "features.unified_exec=false"]},
                "agent": {"id": "docker-forced-stop", "name": "Docker forced-stop acceptance"},
                "credentialSource": "personal", "codexApiKey": MODEL_KEY,
                "codexBaseUrl": "http://host.docker.internal:%d/v1" % self.gateway.server_port})
        self.server.enqueue(active)
        receipts = [self.workspace / (role + ".pid.json") for role in ("parent", "child", "grandchild")]
        self.await_condition(lambda: all(path.exists() for path in receipts) or active["id"] in self.server.completed, process,
                             "forced-stop Docker process tree did not start")
        if not all(path.exists() for path in receipts):
            tool_output = [item.get("output") for request in self.gateway.requests
                           for item in request.get("input", [])
                           if isinstance(item, dict) and item.get("type") == "function_call_output"]
            self.fail("forced-stop task exited before creating its process tree: "
                      + str(self.server.completed.get(active["id"])) + "; tool outputs: " + str(tool_output)
                      + "; gateway errors: " + str(self.gateway.errors) + "\n" + self.diagnostics())
        identities = [json.loads(path.read_text()) for path in receipts]
        self.assertTrue(all(identity.get("start", "").isdigit() for identity in identities))
        # No Docker/Go subprocess launch may sit between these receipts and
        # TerminateProcess: a slow host launch could cross the orphan deadline.
        process.kill()
        process.wait(timeout=10)
        self.assertNotIn(active["id"], self.server.completed,
                         "forced-stop fixture completed before its Runner owner died")
        time.sleep(12)

        probe = r'''const fs=require('fs');
function inspect(identity){
  try{
    const fields=fs.readFileSync('/proc/'+identity.pid+'/stat','utf8').replace(/^.*\) /,'').split(' ');
    return {pid:identity.pid,expectedStart:identity.start,start:fields[19],state:fields[0],matching:fields[19]===identity.start};
  }catch(error){if(error.code!=='ENOENT')throw error;return {pid:identity.pid,matching:false,state:'gone'};}
}
console.log(JSON.stringify({tasks:JSON.parse(process.argv[1]).map(inspect),warm:inspect(JSON.parse(process.argv[2])),
  warmTicks:fs.statSync('force-warm-service-ticks.txt').size}));
'''
        evidence = json.loads(self.docker("exec", canonical[0], "node", "-e", probe,
                                          json.dumps(identities), json.dumps(warm_identity)))
        evidence["provider"] = "codex" if codex else "terminal"
        evidence["fixtureContainers"] = fixture_containers
        evidence["orphanMarkers"] = sorted(path.name for path in self.workspace.glob("orphan-*"))
        print("FORCED_RUNNER_STOP_EVIDENCE " + json.dumps(evidence, sort_keys=True), flush=True)
        self.assertTrue(evidence["warm"]["matching"], "completed service lost its exact process identity: " + str(evidence))
        self.assertNotEqual(evidence["warm"]["state"], "Z", "completed service became a zombie: " + str(evidence))
        self.assertGreater(evidence["warmTicks"], ticks_before, "completed service stopped making progress")
        self.assertFalse(any(identity["matching"] for identity in evidence["tasks"]),
                         "forced Runner termination retained an active container process (including zombies): " + str(evidence))
        self.assertEqual(evidence["orphanMarkers"], [], "an active descendant wrote after its Runner was forcibly stopped")
        self.assertEqual(self.server.errors, [], self.diagnostics())
        if codex:
            self.assertGreaterEqual(len(self.gateway.requests), 1)
            self.assertEqual(self.gateway.errors, [])
        self.assertNotIn(MODEL_KEY, self.diagnostics())

    def test_real_docker_forced_runner_stop(self):
        self.exercise_real_docker_forced_runner_stop()

    def test_real_docker_forced_codex_runner_stop(self):
        self.exercise_real_docker_forced_runner_stop(codex=True)


if __name__ == "__main__":
    unittest.main()
