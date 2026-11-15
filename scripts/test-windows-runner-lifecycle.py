"""Execute real Windows Runner tasks against an isolated, loopback-only platform.

Set LOOPFORGE_TEST_RUNNER_BINARY to the native agentflow-runner.exe. This test
requires Go, Node.js and Git for Windows, but no account, paid model or Docker.
Installer update/uninstall transactions are covered by test-windows-installer.py.
"""

import base64
import ctypes
from datetime import datetime, timedelta, timezone
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import struct
import subprocess
import tempfile
import threading
import time
import unittest
from urllib.parse import parse_qs, urlsplit


RUNNER_ID = "runner-windows-lifecycle"
SESSION = "fixture-runner-session"
BIND_CODE = "fixture-one-time-bind-code"
CREDENTIAL_CANARY = "fixture-local-cli-login-must-stay-local"

# This is a native CLI process, not a mocked subprocess call. It checks its
# real local login file and session continuation, without contacting a model.
NATIVE_CLI_SOURCE = r'''
package main
import ("encoding/json"; "fmt"; "os"; "path/filepath"; "strings")
func main() {
    for _, arg := range os.Args[1:] { if arg == "--version" { fmt.Println("codex-fixture 1.0.0"); return } }
    home, err := os.UserHomeDir(); if err != nil { panic(err) }
    codexHome := os.Getenv("CODEX_HOME")
    login, err := os.ReadFile(filepath.Join(codexHome, "auth.json"))
    if err != nil || !strings.Contains(string(login), "fixture-local-cli-login-must-stay-local") { panic("local fixture login was not available") }
    if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("CODEX_API_KEY") != "" { panic("unexpected model credential") }
    resumed := strings.Contains(strings.Join(os.Args, " "), "NATIVE_FIXTURE_RESUME")
    output := "native-first.json"
    if resumed {
        if _, err := os.Stat("native-session.txt"); err != nil { panic("workspace session was not retained") }
        if !strings.Contains(strings.Join(os.Args, " "), "resume fixture-session") { panic("resume session was not passed") }
        output = "native-resume.json"
    } else if err := os.WriteFile("native-session.txt", []byte("fixture-session"), 0600); err != nil { panic(err) }
    raw, _ := json.Marshal(map[string]any{"loginVerified":true, "home":home, "codexHome":codexHome, "resumed":resumed, "argv":os.Args[1:]})
    if err := os.WriteFile(output, raw, 0600); err != nil { panic(err) }
    fmt.Println("NATIVE_CLI_EXECUTED")
}
'''

NODE_PROCESS_TREE = r'''
const fs = require('fs');
const {spawn} = require('child_process');
const role = process.argv[2] || 'parent';
fs.writeFileSync(role + '.pid.tmp', JSON.stringify({pid: process.pid, role}));
fs.renameSync(role + '.pid.tmp', role + '.pid.json');
if (role !== 'grandchild') {
  spawn(process.execPath, [__filename, role === 'parent' ? 'child' : 'grandchild'],
        {stdio: 'ignore', windowsHide: true});
}
setTimeout(() => fs.writeFileSync('orphan-' + role, 'cancel did not reap this process'), 10000);
setInterval(() => {}, 1000);
'''


class FakePlatform(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self):
        super().__init__(("127.0.0.1", 0), PlatformHandler)
        self.condition = threading.Condition()
        self.requests = []
        self.hellos = []
        self.errors = []
        self.sockets = set()
        self.tasks = []
        self.completed = {}
        self.started = set()
        self.events = {}
        self.desired_cancel = set()
        self.artifacts = {}
        self.bind_code = BIND_CODE
        self.used_bind_codes = set()
        self.session = SESSION
        self.next_session = SESSION
        self.registration_delay = 0
        self.registration_status = 200
        self.expected_rejections = False
        self.on_preflight = None
        self.expiry = (datetime.now(timezone.utc) + timedelta(days=30)).isoformat()

    def record(self, collection, value):
        with self.condition:
            collection.append(value)
            self.condition.notify_all()

    def count(self, path):
        with self.condition:
            return sum(request[1] == path for request in self.requests)

    def enqueue(self, task):
        with self.condition:
            self.tasks.append(task)
            self.condition.notify_all()


class PlatformHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def respond(self, status, value=None):
        payload = json.dumps(value).encode() if value is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def read_request(self):
        path = urlsplit(self.path).path
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.record(self.server.requests, (self.command, path, dict(self.headers), body))
        public = path in {"/api/runners/register", "/api/runners/agentflow-runner", "/api/runners/agentflow-gate-mcp"}
        if not public and self.headers.get("Authorization") != "Bearer " + self.server.session:
            if not self.server.expected_rejections:
                self.server.record(self.server.errors, "missing runner session on " + path)
            self.respond(401, {"error": "runner session required"})
            return None, None
        return path, json.loads(body) if body else {}

    def do_POST(self):
        try:
            path, body = self.read_request()
            if path is None:
                return
            if path == "/api/runners/register":
                if self.server.registration_delay:
                    time.sleep(self.server.registration_delay)
                code = self.headers.get("X-Bootstrap-Token")
                if code != self.server.bind_code or code in self.server.used_bind_codes or self.server.registration_status != 200:
                    if not self.server.expected_rejections:
                        self.server.record(self.server.errors, "unexpected binding code")
                    self.respond(401, {"error": "invalid binding code"})
                    return
                self.server.used_bind_codes.add(code)
                self.server.session = self.server.next_session
                self.respond(200, {
                    "runnerId": RUNNER_ID,
                    "sessionToken": self.server.session,
                    "expiresAt": self.server.expiry,
                    "heartbeatInterval": 1,
                    "leaseTTL": 30,
                })
            elif path == "/api/runners/heartbeat":
                self.respond(200, {"expiresAt": self.server.expiry})
            elif path == "/api/runners/tasks/claim":
                with self.server.condition:
                    if body.get("preflight"):
                        if self.server.on_preflight:
                            self.server.on_preflight()
                        available = bool(self.server.tasks)
                        self.respond(200, {"taskAvailable": available})
                        return
                    if not self.server.tasks:
                        self.server.condition.wait(timeout=0.1)
                    task = self.server.tasks.pop(0) if self.server.tasks else None
                self.respond(200, {"task": task}) if task else self.respond(204)
            elif path in {"/api/runners/disconnect", "/api/runners/deregister"}:
                self.respond(204)
            elif path.startswith("/api/runners/tasks/"):
                task_id, operation = path[len("/api/runners/tasks/"):].split("/", 1)
                with self.server.condition:
                    if operation == "start":
                        self.server.started.add(task_id)
                    elif operation == "events":
                        self.server.events.setdefault(task_id, []).extend(body["events"])
                    elif operation == "complete":
                        if task_id in self.server.completed:
                            self.server.errors.append("task completed more than once: " + task_id)
                        self.server.completed[task_id] = body
                    elif operation != "lease/renew":
                        raise ValueError("unexpected task operation: " + operation)
                    self.server.condition.notify_all()
                if operation == "lease/renew":
                    self.respond(200, self.task_control(task_id))
                else:
                    self.respond(204)
            else:
                self.server.record(self.server.errors, "unexpected endpoint " + path)
                self.respond(404, {"error": "unexpected endpoint"})
        except ConnectionError:
            pass  # A long poll can be cancelled by a graceful daemon stop.
        except Exception as error:
            self.server.record(self.server.errors, repr(error))

    def do_GET(self):
        try:
            path, _ = self.read_request()
            if path is None:
                return
            name = path.rsplit("/", 1)[-1]
            if name in {"agentflow-runner", "agentflow-gate-mcp"}:
                query = parse_qs(urlsplit(self.path).query)
                if query.get("os") not in (["windows"], ["linux"]) or query.get("arch") not in (["amd64"], ["arm64"]):
                    raise ValueError("installer requested an unsupported artifact")
                with self.server.condition:
                    payload = self.server.artifacts[(query["os"][0], name)]
                if query.get("metadata") == ["1"]:
                    self.respond(200, {"sha256": hashlib.sha256(payload).hexdigest()})
                else:
                    self.send_response(200)
                    self.send_header("Content-Type", "application/octet-stream")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                return
            if path.startswith("/api/runners/tasks/") and path.endswith("/control"):
                self.respond(200, self.task_control(path.split("/")[-2]))
                return
            if path != "/api/runners/connect":
                self.server.record(self.server.errors, "unexpected endpoint " + path)
                self.respond(404, {"error": "unexpected endpoint"})
                return
            key = self.headers.get("Sec-WebSocket-Key", "")
            accept = base64.b64encode(hashlib.sha1(
                (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode()
            ).digest()).decode()
            self.send_response(101)
            self.send_header("Upgrade", "websocket")
            self.send_header("Connection", "Upgrade")
            self.send_header("Sec-WebSocket-Accept", accept)
            self.end_headers()
            self.wfile.flush()
            self.connection.settimeout(20)
            with self.server.condition:
                self.server.sockets.add(self.connection)
            hello = self.read_websocket_json()
            self.server.record(self.server.hellos, hello)
            # Drop the first connection to verify authenticated control-channel
            # reconnection without consuming the one-time binding code again.
            if len(self.server.hellos) == 1:
                return
            while self.rfile.read(1):
                pass
        except (ConnectionError, socket.timeout):
            pass
        except Exception as error:
            self.server.record(self.server.errors, repr(error))
        finally:
            with self.server.condition:
                self.server.sockets.discard(self.connection)
            self.close_connection = True

    def task_control(self, task_id):
        with self.server.condition:
            desired = "cancel" if task_id in self.server.desired_cancel else ""
        return {"leaseUntil": self.server.expiry, "desiredState": desired}

    def read_websocket_json(self):
        header = self.rfile.read(2)
        if len(header) != 2 or header[0] != 0x81 or not header[1] & 0x80:
            raise ValueError("expected a masked WebSocket text hello")
        length = header[1] & 0x7f
        if length == 126:
            length = struct.unpack("!H", self.rfile.read(2))[0]
        elif length == 127:
            length = struct.unpack("!Q", self.rfile.read(8))[0]
        if length > 4096:
            raise ValueError("oversized WebSocket hello")
        mask = self.rfile.read(4)
        payload = self.rfile.read(length)
        return json.loads(bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload)))


@unittest.skipUnless(os.name == "nt", "requires an actual Windows Runner process")
class WindowsRunnerLifecycleTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        binary = os.environ.get("LOOPFORGE_TEST_RUNNER_BINARY")
        if not binary or not Path(binary).is_file():
            raise RuntimeError("set LOOPFORGE_TEST_RUNNER_BINARY to a compiled Windows agentflow-runner.exe")
        cls.source_binary = Path(binary).resolve()
        gate = os.environ.get("LOOPFORGE_TEST_GATE_BINARY")
        cls.source_gate = Path(gate) if gate else cls.source_binary.with_name("agentflow-gate-mcp.exe")
        if not cls.source_gate.is_file():
            raise RuntimeError("set LOOPFORGE_TEST_GATE_BINARY to a compiled Windows agentflow-gate-mcp.exe")
        cls.arch = (os.environ.get("PROCESSOR_ARCHITEW6432") or os.environ["PROCESSOR_ARCHITECTURE"]).lower()
        linux_gate = os.environ.get("LOOPFORGE_TEST_LINUX_GATE_BINARY")
        cls.source_linux_gate = Path(linux_gate) if linux_gate else cls.source_gate.with_name("agentflow-gate-mcp-linux-" + cls.arch)
        if not cls.source_linux_gate.is_file() or not cls.source_linux_gate.read_bytes().startswith(b"\x7fELF"):
            raise RuntimeError("set LOOPFORGE_TEST_LINUX_GATE_BINARY to a real Linux Gate ELF binary")
        cls.node_binary = shutil.which("node.exe")
        git = shutil.which("git.exe")
        go = os.environ.get("LOOPFORGE_TEST_GO_BINARY") or shutil.which("go.exe")
        if not cls.node_binary or not git or not go:
            raise RuntimeError("real task acceptance requires Node.js, Git for Windows and Go")
        cls.git_directory = str(Path(git).resolve().parent)
        cls.fixture_temp = tempfile.TemporaryDirectory(prefix="loopforge native cli ")
        cls.addClassCleanup(cls.fixture_temp.cleanup)
        source = Path(cls.fixture_temp.name) / "native-cli.go"
        source.write_text(NATIVE_CLI_SOURCE, encoding="utf-8")
        cls.native_cli = source.with_suffix(".exe")
        reusable = os.environ.get("LOOPFORGE_TEST_NATIVE_CLI_BINARY")
        if reusable:
            cached = Path(reusable).resolve()
            if not cached.is_file() or not cached.read_bytes().startswith(b"MZ") or cached.with_suffix(".go").read_text(encoding="utf-8") != NATIVE_CLI_SOURCE:
                raise RuntimeError("reusable native CLI fixture must match the current fixture source exactly")
            shutil.copy2(cached, cls.native_cli)
        else:
            build_env = os.environ.copy()
            build_env.update({"GOTOOLCHAIN": "local", "CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off"})
            build_env.pop("GOOS", None)
            build_env.pop("GOARCH", None)
            build = subprocess.run([go, "build", "-o", str(cls.native_cli), str(source)],
                                   env=build_env, capture_output=True, text=True, timeout=90)
            if build.returncode:
                raise RuntimeError("native CLI fixture did not compile: " + build.stdout + build.stderr)
        # CTRL_BREAK_EVENT targets the new process group. A headless local
        # shell may have no console; allocate a hidden test console in that case.
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = (ctypes.c_uint32, ctypes.c_int, ctypes.c_uint32)
        kernel.OpenProcess.restype = ctypes.c_void_p
        kernel.WaitForSingleObject.argtypes = (ctypes.c_void_p, ctypes.c_uint32)
        kernel.TerminateProcess.argtypes = (ctypes.c_void_p, ctypes.c_uint32)
        kernel.CloseHandle.argtypes = (ctypes.c_void_p,)
        kernel.QueryFullProcessImageNameW.argtypes = (ctypes.c_void_p, ctypes.c_uint32,
                                                      ctypes.c_wchar_p, ctypes.POINTER(ctypes.c_uint32))
        cls.kernel = kernel
        if not kernel.GetConsoleCP():
            if not kernel.AllocConsole():
                raise ctypes.WinError(ctypes.get_last_error())
            kernel.GetConsoleWindow.restype = ctypes.c_void_p
            user = ctypes.WinDLL("user32")
            user.ShowWindow.argtypes = (ctypes.c_void_p, ctypes.c_int)
            user.ShowWindow(kernel.GetConsoleWindow(), 0)
            cls.addClassCleanup(kernel.FreeConsole)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="loopforge real runner ")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.binary = self.root / "runner bundle" / "agentflow-runner.exe"
        self.binary.parent.mkdir()
        shutil.copy2(self.source_binary, self.binary)
        self.state = self.root / "runner state" / "state.json"
        self.home = self.root / "isolated home"
        self.home.mkdir()
        self.tools = self.root / "isolated tools"
        self.tools.mkdir()
        shutil.copy2(self.native_cli, self.tools / "codex.exe")
        shutil.copy2(self.node_binary, self.tools / "node.exe")
        # A batch shim must not take precedence over the supported native CLI.
        (self.tools / "codex.cmd").write_text("@exit /b 89\n", encoding="ascii")
        self.workspace = self.root / "workspace" / "runs" / "windows-real-tasks"
        self.workspace.mkdir(parents=True)
        self.credentials = {}
        for relative in (".codex/auth.json", ".claude.json", ".docker/config.json"):
            path = self.home / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps({"fixtureCredential": CREDENTIAL_CANARY}), encoding="utf-8")
            self.credentials[path] = path.read_bytes()
        self.server = FakePlatform()
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.addCleanup(self.close_server)
        self.processes = []
        self.child_handles = []
        self.addCleanup(self.kill_processes)

    def close_server(self):
        with self.server.condition:
            connections = list(self.server.sockets)
        for connection in connections:
            try:
                connection.shutdown(socket.SHUT_RDWR)
                connection.close()
            except OSError:
                pass
        self.server.shutdown()
        self.server.server_close()
        self.worker.join(timeout=5)

    def kill_processes(self):
        errors = []
        for process, log in self.processes:
            try:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=10)
            except Exception as error:
                errors.append(str(error))
            finally:
                log.close()
        # Retain kernel handles when observing the descendants. Cleanup cannot
        # accidentally terminate another process after a PID has been reused.
        for handle in self.child_handles:
            try:
                if self.kernel.WaitForSingleObject(handle, 0) == 258:
                    if not self.kernel.TerminateProcess(handle, 1):
                        errors.append("could not terminate an owned descendant")
                    if self.kernel.WaitForSingleObject(handle, 5000) != 0:
                        errors.append("owned descendant did not exit during cleanup")
            finally:
                self.kernel.CloseHandle(handle)
        self.assertEqual(errors, [], "native process cleanup failed")

    def isolated_environment(self):
        # Keep only OS prerequisites, Git and the isolated native executables.
        # Provider keys, actual CLI login directories and Docker are absent.
        environment = {key: value for key, value in os.environ.items()
                       if key.upper() in {"SYSTEMROOT", "WINDIR", "TEMP", "TMP", "COMSPEC",
                                          "SYSTEMDRIVE", "PROGRAMFILES", "PROGRAMFILES(X86)", "PATHEXT",
                                          "PROCESSOR_ARCHITECTURE", "PROCESSOR_ARCHITEW6432"}}
        environment.update({
            "HOME": str(self.home), "USERPROFILE": str(self.home),
            "APPDATA": str(self.home / "AppData/Roaming"),
            "LOCALAPPDATA": str(self.home / "AppData/Local"),
            "CODEX_HOME": str(self.home / ".codex"),
            "PATH": os.pathsep.join((str(self.tools), self.git_directory,
                                      str(Path(os.environ["SYSTEMROOT"]) / "System32"))),
            "AGENTFLOW_BROWSER": "off",
            "AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN": BIND_CODE,
        })
        return environment

    def start_runner(self, number):
        environment = self.isolated_environment()
        log = (self.root / ("runner-%d.log" % number)).open("wb")
        process = subprocess.Popen([
            str(self.binary), "-server", "http://127.0.0.1:%d/api" % self.server.server_port,
            "-state", str(self.state), "-workspace", str(self.root / "workspace"),
            "-name", "Windows lifecycle fixture", "-heartbeat", "200ms",
            "-long-poll", "1s", "-auto-update=false",
        ], cwd=self.root, env=environment, stdin=subprocess.DEVNULL,
            stdout=log, stderr=subprocess.STDOUT, creationflags=subprocess.CREATE_NEW_PROCESS_GROUP)
        self.processes.append((process, log))
        return process

    def diagnostics(self):
        paths = list(self.root.glob("runner-*.log")) + list(self.root.rglob("stderr.log"))
        return "\n".join(path.read_text(errors="replace") for path in paths)

    def await_condition(self, predicate, process, message):
        deadline = time.monotonic() + 20
        with self.server.condition:
            while not predicate():
                if (process is not None and process.poll() is not None) or time.monotonic() >= deadline:
                    self.fail(message + "\n" + self.diagnostics())
                self.server.condition.wait(timeout=0.1)

    def stop_runner(self, process):
        process.send_signal(signal.CTRL_BREAK_EVENT)
        try:
            code = process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            self.fail("Runner did not stop gracefully\n" + self.diagnostics())
        self.assertEqual(code, 0, self.diagnostics())

    def shell_task(self, task_id, javascript, outputs=(), inputs=()):
        quote = lambda value: "'" + str(value).replace("'", "'\"'\"'") + "'"
        return {
            "id": task_id, "runnerId": RUNNER_ID, "provider": "terminal",
            "workspace": "runs/windows-real-tasks", "runId": "windows-real-tasks",
            "attempt": 1, "maxAttempts": 1, "leaseUntil": self.server.expiry,
            "command": quote((self.tools / "node.exe").as_posix()) + " -e " + quote(javascript),
            "invocation": {
                "reportOutputs": True, "outputFiles": list(outputs), "inputs": list(inputs),
                "artifacts": [{"name": path, "path": path, "required": True} for path in outputs],
            },
        }

    def native_task(self, resumed=False):
        output = "native-resume.json" if resumed else "native-first.json"
        return {
            "id": "native-resume" if resumed else "native-first",
            "runnerId": RUNNER_ID, "provider": "codex", "attempt": 1,
            "workspace": "runs/windows-real-tasks", "runId": "windows-real-tasks",
            "leaseUntil": self.server.expiry,
            "prompt": "NATIVE_FIXTURE_RESUME" if resumed else "NATIVE_FIXTURE_FIRST",
            "invocation": {
                "runId": "windows-real-tasks", "nodeId": "native-node",
                "invocationId": "native-resume" if resumed else "native-first",
                "resumeSessionId": "fixture-session" if resumed else "",
                "executor": {"kind": "codex", "binary": "codex.exe", "args": ["exec", "--json"]},
                "agent": {"id": "fixture-agent", "name": "Native fixture"},
                "credentialSource": "local", "reportOutputs": True, "outputFiles": [output],
                "artifacts": [{"name": output, "path": output, "required": True}],
            },
        }

    def run_task(self, process, task):
        self.server.enqueue(task)
        task_id = task["id"]
        self.await_condition(lambda: task_id in self.server.completed, process,
                             "Runner did not complete " + task_id)
        self.assertIn(task_id, self.server.started, self.diagnostics())
        result = self.server.completed[task_id]
        self.assertTrue(self.server.events.get(task_id), "task output was not streamed")
        return result

    def assert_native_receipt(self, result, resumed):
        self.assertEqual(result["status"], "success", result)
        output = "native-resume.json" if resumed else "native-first.json"
        report = json.loads(result["outputs"]["files"][output])
        self.assertEqual(Path(report["home"]), self.home)
        self.assertEqual(Path(report["codexHome"]), self.home / ".codex")
        self.assertTrue(report["loginVerified"])
        self.assertEqual(report["resumed"], resumed)
        self.assertEqual(result["artifacts"][output], (self.workspace / output).read_text())

    def execute_real_work(self, process):
        expected = "Windows real task: upstream fixture"
        success = self.shell_task("shell-success", r'''
const fs = require("fs");
const upstream = fs.readFileSync(".agentflow/inputs/upstream.txt", "utf8");
fs.writeFileSync("result.txt", "Windows real task: " + upstream);
console.log("NODE_TASK_EXECUTED");
''', ["result.txt"], [{"path": ".agentflow/inputs/upstream.txt", "content": "upstream fixture"}])
        result = self.run_task(process, success)
        self.assertEqual(result["status"], "success", result)
        self.assertEqual(result["exitCode"], 0)
        self.assertIn("NODE_TASK_EXECUTED", result["result"])
        self.assertEqual(result["outputs"]["files"]["result.txt"], expected)
        self.assertEqual(result["artifacts"], {"result.txt": expected})
        self.assertEqual((self.workspace / "result.txt").read_text(), expected)

        failure = self.shell_task("shell-failure", r'''
require("fs").writeFileSync("failure.txt", "diagnostic retained after exit 7");
console.error("EXPECTED_NATIVE_FAILURE"); process.exit(7);
''', ["failure.txt"])
        result = self.run_task(process, failure)
        self.assertEqual(result["status"], "failed", result)
        self.assertEqual(result["exitCode"], 7)
        self.assertEqual(result["outputs"]["files"]["failure.txt"], "diagnostic retained after exit 7")
        self.assertTrue(any("EXPECTED_NATIVE_FAILURE" in event["data"]
                            for event in self.server.events["shell-failure"]))

        self.assert_native_receipt(self.run_task(process, self.native_task()), False)

        handles = self.start_process_tree(process, "cancel-tree")
        with self.server.condition:
            self.server.desired_cancel.add("cancel-tree")
        self.await_condition(lambda: "cancel-tree" in self.server.completed, process,
                             "platform cancellation did not finish the task")
        self.assertEqual(self.server.completed["cancel-tree"]["status"], "canceled")
        self.assert_process_tree_stopped(handles)

    def start_process_tree(self, process, task_id):
        tree = self.workspace / "process-tree.js"
        tree.write_text(NODE_PROCESS_TREE, encoding="utf-8")
        receipts = [self.workspace / (role + ".pid.json") for role in ("parent", "child", "grandchild")]
        for receipt in receipts:
            receipt.unlink(missing_ok=True)
        self.server.enqueue(self.shell_task(task_id, 'require("./process-tree.js")'))
        self.await_condition(lambda: all(path.exists() for path in receipts), process,
                             "Node did not start the real child/grandchild tree")
        handles = []
        for receipt in receipts:
            pid = json.loads(receipt.read_text())["pid"]
            handle = self.kernel.OpenProcess(0x100000 | 0x1000 | 0x0001, False, pid)
            self.assertTrue(handle, "could not retain exact descendant process handle")
            self.child_handles.append(handle)
            handles.append(handle)
            self.assertEqual(self.kernel.WaitForSingleObject(handle, 0), 258)
        return handles

    def assert_process_tree_stopped(self, handles, expect_no_markers=True):
        for handle in handles:
            self.assertEqual(self.kernel.WaitForSingleObject(handle, 5000), 0,
                             "stopping the task or Runner left a Node descendant alive")
        if expect_no_markers:
            self.assertFalse(list(self.workspace.glob("orphan-*")), "orphan process wrote after cancellation")

    def retain_installed_process(self, install):
        record = json.loads((install / "process.json").read_text(encoding="utf-8-sig"))
        binary = Path(record["binary"]).resolve()
        self.assertTrue(binary.is_relative_to(install.resolve()))
        handle = self.kernel.OpenProcess(0x100000 | 0x1000 | 0x0001, False, record["pid"])
        self.assertTrue(handle, "installed Runner process did not start")
        self.child_handles.append(handle)
        buffer = ctypes.create_unicode_buffer(32768)
        length = ctypes.c_uint32(len(buffer))
        self.assertTrue(self.kernel.QueryFullProcessImageNameW(handle, 0, buffer, ctypes.byref(length)))
        self.assertEqual(Path(buffer.value).resolve(), binary,
                         "saved process record does not identify the actual installed executable")
        return handle, record

    def cleanup_installed_process(self, install):
        # Also cover a subprocess timeout before the test could retain its
        # handle. Only the exact executable in this test installation is owned.
        record_path = install / "process.json"
        if not record_path.exists():
            return
        record = json.loads(record_path.read_text(encoding="utf-8-sig"))
        binary = Path(record["binary"]).resolve()
        if not binary.is_relative_to(install.resolve()):
            return
        handle = self.kernel.OpenProcess(0x100000 | 0x1000 | 0x0001, False, record["pid"])
        if not handle:
            return
        try:
            buffer = ctypes.create_unicode_buffer(32768)
            length = ctypes.c_uint32(len(buffer))
            if self.kernel.QueryFullProcessImageNameW(handle, 0, buffer, ctypes.byref(length)):
                if Path(buffer.value).resolve() == binary and self.kernel.WaitForSingleObject(handle, 0) == 258:
                    self.assertTrue(self.kernel.TerminateProcess(handle, 1))
                    self.assertEqual(self.kernel.WaitForSingleObject(handle, 5000), 0)
        finally:
            self.kernel.CloseHandle(handle)

    def run_installer(self, powershell, install, *arguments, success=True, environment=None):
        # Windows PowerShell can let detached children inherit extra handles.
        # A regular log file avoids waiting for pipe EOF until the Runner stops.
        log_path = self.root / "installer-command.log"
        with log_path.open("wb") as log:
            process_environment = self.isolated_environment()
            process_environment.update(environment or {})
            result = subprocess.run([
                powershell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
                "-File", str(Path(__file__).resolve().parents[1] / "deploy/bind-cli.ps1"),
                "-InstallDirectory", str(install), *arguments,
            ], env=process_environment, cwd=self.root, stdin=subprocess.DEVNULL,
                stdout=log, stderr=subprocess.STDOUT, timeout=60)
        output = log_path.read_text(errors="replace")
        if success:
            self.assertEqual(result.returncode, 0, output + self.diagnostics())
        else:
            self.assertNotEqual(result.returncode, 0, "installer accepted failed connection: " + output)
        self.assertNotIn(BIND_CODE, output)
        self.assertNotIn(CREDENTIAL_CANARY, output)
        return output

    def exercise_actual_installer(self, shell):
        powershell = shutil.which(shell)
        self.assertTrue(powershell, shell + " is required for the complete Windows lifecycle")
        install = self.root / "actual installation"
        self.addCleanup(self.cleanup_installed_process, install)
        self.server.artifacts = {
            ("windows", "agentflow-runner"): self.source_binary.read_bytes(),
            ("windows", "agentflow-gate-mcp"): self.source_gate.read_bytes(),
            ("linux", "agentflow-gate-mcp"): self.source_linux_gate.read_bytes(),
        }
        sentinel = self.workspace / "user-work.txt"
        sentinel.write_text("retain the user's workspace", encoding="utf-8")
        self.run_installer(powershell, install,
                           "-ServerUrl", "http://127.0.0.1:%d/api" % self.server.server_port,
                           "-BindCode", BIND_CODE, "-WorkspaceRoot", str(self.root / "workspace"),
                           "-CliDirectory", str(self.tools), "-Name", "Real installed Windows Runner")
        original_handle, original_process = self.retain_installed_process(install)
        self.assertEqual(self.kernel.WaitForSingleObject(original_handle, 0), 258)
        self.await_condition(lambda: self.server.count("/api/runners/heartbeat") >= 1,
                             None, "installer did not connect the real Runner")
        original_state = (install / "state.json").read_bytes()
        self.assertEqual(json.loads(original_state)["runnerId"], RUNNER_ID)
        original_config = json.loads((install / "config.json").read_text(encoding="utf-8-sig"))
        linux_companion = Path(original_config["GateBinary"]).with_name("agentflow-gate-mcp-linux-" + self.arch)
        self.assertEqual(linux_companion.read_bytes(), self.server.artifacts[("linux", "agentflow-gate-mcp")])

        # Exercise the installed Gate binary through a real dispatched shell
        # task. No Go toolchain or paid CLI is available in the Runner's PATH.
        gate = self.shell_task("installed-gate", r'''
const fs = require("fs"), cp = require("child_process");
const call = {jsonrpc:"2.0",id:1,method:"tools/call",params:{name:"submit_gate_result",arguments:{decision:"pass",reason:"actual installed Windows Gate executed"}}};
const result = cp.spawnSync(process.env.AGENTFLOW_GATE_MCP_BINARY, [], {
  input:JSON.stringify(call)+"\n",encoding:"utf8",
  env:{...process.env,AGENTFLOW_GATE_RESULT_PATH:"gate-result.json"},windowsHide:true
});
if (result.error || result.status !== 0) throw Error("installed Gate failed: " + result.stderr);
if (JSON.parse(fs.readFileSync("gate-result.json","utf8")).decision !== "pass") throw Error("Gate did not submit its result");
console.log("INSTALLED_GATE_EXECUTED");
''', ["gate-result.json"])
        result = self.run_task(None, gate)
        self.assertEqual(result["status"], "success", result)
        self.assertEqual(json.loads(result["artifacts"]["gate-result.json"])["decision"], "pass")
        update_descendants = self.start_process_tree(None, "installer-update-tree")
        before_heartbeat = self.server.count("/api/runners/heartbeat")
        # PE overlays are ignored by the Windows loader. Both candidates remain
        # real runnable binaries while their checksums and version directory change.
        with self.server.condition:
            self.server.artifacts = {name: binary + b"\nLoopForge acceptance candidate v2\n"
                                     for name, binary in self.server.artifacts.items()}
        self.run_installer(powershell, install, "-Update")
        self.assertEqual(self.kernel.WaitForSingleObject(original_handle, 5000), 0)
        # Download validation intentionally leaves the old task running until
        # the replacement is ready; its timer may fire before the stop occurs.
        self.assert_process_tree_stopped(update_descendants, expect_no_markers=False)
        updated_handle, updated_process = self.retain_installed_process(install)
        self.assertNotEqual(updated_process["pid"], original_process["pid"])
        updated_config = json.loads((install / "config.json").read_text(encoding="utf-8-sig"))
        for name in ("RunnerBinary", "GateBinary"):
            self.assertNotEqual(updated_config[name], original_config[name])
            artifact = ("windows", "agentflow-runner" if name == "RunnerBinary" else "agentflow-gate-mcp")
            self.assertEqual(Path(updated_config[name]).read_bytes(), self.server.artifacts[artifact])
        updated_linux = Path(updated_config["GateBinary"]).with_name("agentflow-gate-mcp-linux-" + self.arch)
        self.assertNotEqual(updated_linux, linux_companion)
        self.assertEqual(updated_linux.read_bytes(), self.server.artifacts[("linux", "agentflow-gate-mcp")])
        self.await_condition(lambda: self.server.count("/api/runners/heartbeat") > before_heartbeat,
                             None, "updated real Runner did not reconnect")
        self.assertEqual((install / "state.json").read_bytes(), original_state)
        self.assertEqual(self.server.count("/api/runners/register"), 1)
        self.assertEqual(sentinel.read_text(), "retain the user's workspace")
        continuation = self.shell_task("installed-resume", r'''
const fs = require("fs");
if (JSON.parse(fs.readFileSync("gate-result.json","utf8")).decision !== "pass") throw Error("update lost task workspace");
fs.writeFileSync("installed-resume.txt","real task continued after installer update");
console.log("INSTALLED_UPDATE_RESUMED");
''', ["installed-resume.txt"])
        result = self.run_task(None, continuation)
        self.assertEqual(result["status"], "success", result)
        self.assertEqual(result["artifacts"]["installed-resume.txt"], "real task continued after installer update")
        uninstall_descendants = self.start_process_tree(None, "installer-uninstall-tree")
        self.run_installer(powershell, install, "-Uninstall")
        self.assertEqual(self.kernel.WaitForSingleObject(updated_handle, 5000), 0)
        self.assert_process_tree_stopped(uninstall_descendants, expect_no_markers=False)
        self.assertEqual(self.server.count("/api/runners/deregister"), 1)
        for name in ("config.json", "state.json", "process.json", "install.ps1", "stdout.log", "stderr.log"):
            self.assertFalse((install / name).exists(), "uninstall retained owned file " + name)
        self.assertFalse(list((install / "bin").rglob("*.exe")))
        self.assertFalse(list((install / "bin").rglob("agentflow-gate-mcp-linux-*")))
        self.assertEqual(sentinel.read_text(), "retain the user's workspace")
        self.assertTrue((self.workspace / "installed-resume.txt").is_file())
        self.assertEqual(self.server.errors, [])
        for _, _, headers, raw in self.server.requests:
            self.assertNotIn(CREDENTIAL_CANARY.encode(), raw)
            self.assertNotIn(CREDENTIAL_CANARY, json.dumps(headers))
        for path, original in self.credentials.items():
            self.assertEqual(path.read_bytes(), original)

    def test_actual_installer_connect_update_uninstall_powershell_51(self):
        self.exercise_actual_installer("powershell.exe")

    def test_actual_installer_connect_update_uninstall_powershell_7(self):
        self.exercise_actual_installer("pwsh.exe")

    def exercise_actual_rebind(self, shell):
        powershell = shutil.which(shell)
        self.assertTrue(powershell, shell + " is required")
        install = self.root / "rebind installation"
        self.addCleanup(self.cleanup_installed_process, install)
        self.server.artifacts = {
            ("windows", "agentflow-runner"): self.source_binary.read_bytes(),
            ("windows", "agentflow-gate-mcp"): self.source_gate.read_bytes(),
            ("linux", "agentflow-gate-mcp"): self.source_linux_gate.read_bytes(),
        }
        sentinel = self.workspace / "retained-on-rebind.txt"
        sentinel.write_text("identity and workspace survive session renewal", encoding="utf-8")
        self.run_installer(
            powershell, install,
            "-ServerUrl", "http://127.0.0.1:%d/api" % self.server.server_port,
            "-BindCode", BIND_CODE, "-WorkspaceRoot", str(self.root / "workspace"),
            "-CliDirectory", str(self.tools), "-Concurrency", "2",
            environment={"AGENTFLOW_RUNNER_CONCURRENCY": "32"},
        )
        state_path, config_path = install / "state.json", install / "config.json"
        original_config = json.loads(config_path.read_text(encoding="utf-8-sig"))
        original_handle, _ = self.retain_installed_process(install)
        registration = next(json.loads(raw) for _, path, _, raw in self.server.requests
                            if path == "/api/runners/register")
        self.assertEqual(registration["maxConcurrency"], 2,
                         "inherited concurrency overrode the saved installer setting")
        self.run_installer(powershell, install, "-Rebind", "-BindCode", BIND_CODE + "-renewed", success=False)
        self.assertEqual(self.kernel.WaitForSingleObject(original_handle, 0), 258)
        self.run_installer(powershell, install, "-Stop")
        self.assertEqual(self.kernel.WaitForSingleObject(original_handle, 5000), 0)

        # An expired local session forces registration with the fresh one-time
        # code while retaining the original RunnerID and workspace.
        expired = json.loads(state_path.read_bytes())
        expired["expiresAt"] = "2000-01-01T00:00:00Z"
        state_path.write_text(json.dumps(expired), encoding="utf-8")
        self.server.bind_code = BIND_CODE + "-renewed"
        self.server.next_session = SESSION + "-renewed"
        self.run_installer(powershell, install, "-Rebind", "-BindCode", self.server.bind_code)
        renewed = json.loads(state_path.read_bytes())
        self.assertEqual(renewed["runnerId"], RUNNER_ID)
        self.assertEqual(renewed["sessionToken"], SESSION + "-renewed")
        self.run_installer(powershell, install, "-Stop")

        # Server-side revocation must be recoverable even when expiresAt is
        # still in the future. Ordinary restart must not falsely report ready.
        before = state_path.read_bytes()
        self.server.session = "revoked-fixture-session"
        self.server.expected_rejections = True
        output = self.run_installer(powershell, install, success=False)
        self.assertIn("session was rejected", output)
        self.assertEqual(state_path.read_bytes(), before)
        self.assertFalse((install / "process.json").exists())
        self.server.bind_code = BIND_CODE + "-revocation-recovery"
        self.server.next_session = SESSION + "-recovered"
        self.run_installer(powershell, install, "-Rebind", "-BindCode", self.server.bind_code)
        recovered = json.loads(state_path.read_bytes())
        self.assertEqual(recovered["runnerId"], RUNNER_ID)
        self.assertEqual(recovered["sessionToken"], SESSION + "-recovered")
        self.run_installer(powershell, install, "-Stop")

        # A rejected new code must restore the old local state byte-for-byte;
        # the server did not rotate the old valid session in this case.
        state_before, config_before = state_path.read_bytes(), config_path.read_bytes()
        self.run_installer(powershell, install, "-Rebind", "-BindCode", BIND_CODE + "-invalid", success=False)
        self.assertEqual(state_path.read_bytes(), state_before)
        self.assertEqual(config_path.read_bytes(), config_before)
        self.assertFalse((install / "process.json").exists())
        self.run_installer(powershell, install)
        continued = self.shell_task("rebound-shell", r'''
const fs=require("fs");
if(fs.readFileSync("retained-on-rebind.txt","utf8")!=="identity and workspace survive session renewal")throw Error("rebind lost workspace");
fs.writeFileSync("rebound-result.txt","native work continued after session recovery");
console.log("REBOUND_TASK_EXECUTED");
''', ["rebound-result.txt"])
        self.assertEqual(self.run_task(None, continued)["status"], "success")
        final_config = json.loads(config_path.read_text(encoding="utf-8-sig"))
        for field in ("WorkspaceRoot", "CliDirectory", "PathEntries", "Name", "Concurrency"):
            self.assertEqual(final_config[field], original_config[field])
        self.assertEqual(sentinel.read_text(), "identity and workspace survive session renewal")
        registrations = [json.loads(raw) for _, path, _, raw in self.server.requests
                         if path == "/api/runners/register"]
        for registration in registrations[1:]:
            self.assertEqual(registration["runnerId"], RUNNER_ID)
        self.run_installer(powershell, install, "-Uninstall")
        self.assertFalse(config_path.exists())
        self.assertEqual(sentinel.read_text(), "identity and workspace survive session renewal")
        for path, original in self.credentials.items():
            self.assertEqual(path.read_bytes(), original)
        self.assertEqual(self.server.errors, [])

    def test_actual_installer_rebind_powershell_51(self):
        self.exercise_actual_rebind("powershell.exe")

    def test_actual_installer_rebind_powershell_7(self):
        self.exercise_actual_rebind("pwsh.exe")

    def test_actual_installer_waits_for_delayed_registration_failure(self):
        powershell = shutil.which("powershell.exe")
        install = self.root / "delayed rejected installation"
        self.addCleanup(self.cleanup_installed_process, install)
        self.server.artifacts = {
            ("windows", "agentflow-runner"): self.source_binary.read_bytes(),
            ("windows", "agentflow-gate-mcp"): self.source_gate.read_bytes(),
            ("linux", "agentflow-gate-mcp"): self.source_linux_gate.read_bytes(),
        }
        self.server.registration_delay = 6
        self.server.registration_status = 401
        self.server.expected_rejections = True
        output = self.run_installer(
            powershell, install, "-ServerUrl", "http://127.0.0.1:%d/api" % self.server.server_port,
            "-BindCode", BIND_CODE, "-CliDirectory", str(self.tools), success=False,
        )
        self.assertIn("exited during startup", output)
        self.assertFalse((install / "state.json").exists())
        self.assertFalse((install / "process.json").exists())

    def test_actual_rebind_keeps_issued_session_when_config_commit_fails(self):
        powershell = shutil.which("powershell.exe")
        install = self.root / "rebind commit failure"
        self.addCleanup(self.cleanup_installed_process, install)
        self.server.artifacts = {
            ("windows", "agentflow-runner"): self.source_binary.read_bytes(),
            ("windows", "agentflow-gate-mcp"): self.source_gate.read_bytes(),
            ("linux", "agentflow-gate-mcp"): self.source_linux_gate.read_bytes(),
        }
        self.run_installer(
            powershell, install, "-ServerUrl", "http://127.0.0.1:%d/api" % self.server.server_port,
            "-BindCode", BIND_CODE, "-CliDirectory", str(self.tools),
        )
        self.run_installer(powershell, install, "-Stop")
        config_path, state_path = install / "config.json", install / "state.json"
        before_config = config_path.read_bytes()
        before_state = json.loads(state_path.read_bytes())
        self.server.bind_code = BIND_CODE + "-commit-recovery"
        self.server.next_session = SESSION + "-after-commit-failure"
        held_files = []

        def hold_config_after_registration():
            if self.server.session == self.server.next_session and not held_files:
                # Windows Python file handles do not share deletion. Hold only
                # this fixture's config after authenticated readiness succeeds.
                held_files.append(config_path.open("rb"))

        self.server.on_preflight = hold_config_after_registration
        try:
            output = self.run_installer(powershell, install, "-Rebind", "-BindCode", self.server.bind_code, success=False)
            self.assertIn("after the platform issued a replacement session", output)
        finally:
            self.server.on_preflight = None
            for handle in held_files:
                handle.close()
        self.assertTrue(held_files, "fixture did not lock config after registration")
        self.assertEqual(config_path.read_bytes(), before_config)
        replacement = json.loads(state_path.read_bytes())
        self.assertEqual(replacement["runnerId"], before_state["runnerId"])
        self.assertNotEqual(replacement["sessionToken"], before_state["sessionToken"])
        self.assertEqual(replacement["sessionToken"], self.server.session)
        self.assertFalse((install / "process.json").exists())
        registrations = self.server.count("/api/runners/register")
        self.run_installer(powershell, install)
        self.assertEqual(self.server.count("/api/runners/register"), registrations,
                         "recovery consumed another binding code instead of the issued session")
        self.run_installer(powershell, install, "-Uninstall")
        self.assertFalse(config_path.exists())
        self.assertEqual(self.server.errors, [])

    def test_real_tasks_cancel_and_resume_after_authenticated_reconnect(self):
        first = self.start_runner(1)
        self.await_condition(lambda: self.server.count("/api/runners/heartbeat") >= 1
                             and self.server.count("/api/runners/tasks/claim") >= 1
                             and len(self.server.hellos) >= 2,
                             first, "Runner did not register, heartbeat, poll and reconnect its control channel")
        self.assertTrue(self.state.is_file(), self.diagnostics())
        persisted = self.state.read_bytes()
        state = json.loads(persisted)
        self.assertEqual(set(state), {"runnerId", "sessionToken", "expiresAt"})
        self.assertEqual(state["runnerId"], RUNNER_ID)
        self.assertEqual(state["sessionToken"], SESSION)
        self.execute_real_work(first)
        self.stop_runner(first)
        self.assertEqual(self.server.count("/api/runners/disconnect"), 1)
        self.assertEqual(self.state.read_bytes(), persisted)

        heartbeats = self.server.count("/api/runners/heartbeat")
        claims = self.server.count("/api/runners/tasks/claim")
        hellos = len(self.server.hellos)
        second = self.start_runner(2)
        self.await_condition(lambda: self.server.count("/api/runners/heartbeat") > heartbeats
                             and self.server.count("/api/runners/tasks/claim") > claims
                             and len(self.server.hellos) > hellos,
                             second, "Runner did not reconnect using its saved session")
        resume = self.shell_task("shell-resume", r'''
const fs = require("fs");
if (fs.readFileSync("result.txt", "utf8") !== "Windows real task: upstream fixture") throw Error("workspace output was lost");
if (fs.readFileSync(".agentflow/inputs/upstream.txt", "utf8") !== "upstream fixture") throw Error("upstream input was lost");
fs.writeFileSync("resumed.txt", "continued after runner restart"); console.log("WORKSPACE_RESUMED");
''', ["resumed.txt"])
        result = self.run_task(second, resume)
        self.assertEqual(result["status"], "success", result)
        self.assertEqual(result["artifacts"]["resumed.txt"], "continued after runner restart")
        self.assert_native_receipt(self.run_task(second, self.native_task(resumed=True)), True)
        forced_handles = self.start_process_tree(second, "force-stop-tree")
        # Stop-Process in Update/Uninstall uses TerminateProcess as Popen.kill
        # does. Job ownership must reap descendants even without Go defers.
        second.kill()
        second.wait(timeout=10)
        self.assert_process_tree_stopped(forced_handles)
        self.assertEqual(self.server.count("/api/runners/disconnect"), 1)
        third = self.start_runner(3)
        continued = self.shell_task("after-force-resume", r'''
const fs = require("fs");
if (fs.readFileSync("resumed.txt", "utf8") !== "continued after runner restart") throw Error("forced stop lost workspace");
fs.writeFileSync("after-force.txt", "continued after forced Runner stop"); console.log("FORCED_STOP_RESUMED");
''', ["after-force.txt"])
        result = self.run_task(third, continued)
        self.assertEqual(result["status"], "success", result)
        self.assertEqual(result["artifacts"]["after-force.txt"], "continued after forced Runner stop")
        self.stop_runner(third)
        self.assertEqual(self.server.count("/api/runners/disconnect"), 2)
        self.assertEqual(self.server.count("/api/runners/register"), 1,
                         "restart consumed the one-time binding code instead of reusing its session")
        self.assertEqual(self.state.read_bytes(), persisted)
        self.assertEqual(self.server.errors, [], self.diagnostics())

        for method, path, headers, raw in self.server.requests:
            self.assertNotIn(CREDENTIAL_CANARY.encode(), raw)
            self.assertNotIn(CREDENTIAL_CANARY, json.dumps(headers))
            self.assertNotIn("Cookie", headers)
            if path == "/api/runners/register":
                self.assertEqual(method, "POST")
                self.assertNotIn("Authorization", headers)
                registration = json.loads(raw)
                self.assertEqual(registration["os"], "windows")
                self.assertEqual(registration["name"], "Windows lifecycle fixture")
                self.assertEqual([runtime["provider"] for runtime in registration["runtimes"]], ["mock", "terminal", "codex"])
                native = registration["runtimes"][2]
                self.assertEqual(Path(native["binaryPath"]), self.tools / "codex.exe")
                self.assertEqual(native["version"], "codex-fixture 1.0.0")
            else:
                self.assertEqual(headers.get("Authorization"), "Bearer " + SESSION)
                self.assertNotIn("X-Bootstrap-Token", headers)
            if path == "/api/runners/heartbeat":
                heartbeat = json.loads(raw)
                self.assertIn(heartbeat["status"], {"idle", "busy"})
                self.assertIn(heartbeat["runningTasks"], {0, 1})
                self.assertEqual([runtime["provider"] for runtime in heartbeat["runtimes"]], ["mock", "terminal", "codex"])
            if path == "/api/runners/tasks/claim":
                self.assertEqual(json.loads(raw)["availableRuntimes"], ["mock", "terminal", "codex"])
        for hello in self.server.hellos:
            self.assertEqual(hello["type"], "hello")
            self.assertFalse(hello.get("activeTaskIds"))
        for path, original in self.credentials.items():
            self.assertEqual(path.read_bytes(), original, "local login state changed")
        self.assertNotIn(BIND_CODE, self.diagnostics())
        self.assertNotIn(CREDENTIAL_CANARY, self.diagnostics())


if __name__ == "__main__":
    unittest.main()
