import ctypes
import http.client
import json
import os
import pathlib
import re
import signal
import stat
import subprocess
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


# The execd command owns the CLI's stdout and stderr. Its file API carries
# stdin and tool responses, so the sandbox needs no route back to the worker.
state, cwd, mailbox = map(pathlib.Path, sys.argv[1:4])
uid, gid, port = map(int, sys.argv[4:7])
request_path = mailbox / "command.json"
config = json.loads(request_path.read_bytes())
request_path.unlink()
token, guards = config["token"], config["guards"]
home = state / "home"
mode_before = {}


def protect(path, mode):
    original = path.stat(follow_symlinks=False)
    if not stat.S_ISDIR(original.st_mode) and not stat.S_ISREG(original.st_mode):
        raise ValueError("invalid conversation guard")
    mode_before.setdefault(path, (original.st_uid, original.st_gid, stat.S_IMODE(original.st_mode)))
    os.chown(path, 0, gid, follow_symlinks=False)
    os.chmod(path, mode)


def prepare_home():
    # Make the two imported trees reachable by the CLI uid, including when
    # the corresponding worker paths happen to be under a private HOME.
    for path in {*state.parents, *cwd.parents} - {pathlib.Path("/")}:
        current = stat.S_IMODE(path.stat().st_mode)
        if not current & 0o001:
            protect(path, current | 0o001)
    protect(state, 0o750)
    for entry in state.iterdir():
        if entry == home:
            continue
        paths = [entry]
        if entry.is_dir() and not entry.is_symlink():
            paths.extend(entry.rglob("*"))
        for path in paths:
            if not path.is_symlink():
                mode = stat.S_IMODE(path.stat().st_mode)
                protect(path, (mode & 0o700) | (0o050 if path.is_dir() or mode & 0o100 else 0o040))
    # Writable sticky directories let CLIs maintain their journals while
    # preventing replacement of root-owned hooks or their directory anchors.
    protect(home, 0o1770)
    tmp = home / "tmp"
    tmp.mkdir(mode=0o700, exist_ok=True)
    os.chown(tmp, uid, gid, follow_symlinks=False)
    for relative in guards:
        path = state / relative
        if not path.exists():
            continue
        for parent in path.parents:
            if parent == home:
                break
            protect(parent, 0o1770)
        protect(path, 0o440)


class Relay(BaseHTTPRequestHandler):
    def setup(self):
        super().setup()
        self.connection.settimeout(12)

    def log_message(self, *_args):
        pass

    def do_POST(self):
        if self.path != "/internal/agent-tools/" + token + "/mcp":
            self.send_error(404)
            return
        if not self.server.calls.acquire(blocking=False):
            self.send_error(429)
            return
        try:
            self.relay()
        except (ConnectionError, TimeoutError):
            pass
        finally:
            self.server.calls.release()

    def relay(self):
        size = self.headers.get("Content-Length", "")
        if not size.isdigit() or not 0 < int(size) <= 4 << 20:
            self.send_error(400)
            return
        body = self.rfile.read(int(size))
        try:
            rpc = json.loads(body)
        except ValueError:
            self.send_error(400)
            return
        if not isinstance(rpc, dict):
            self.send_error(400)
            return
        ident = uuid.uuid4().hex
        request = mailbox / (ident + ".request")
        response = mailbox / (ident + ".response")
        request.write_bytes(body)
        # One short write keeps control messages separate even from a CLI
        # that emits stderr without a trailing newline.
        os.write(2, f"\n{mailbox.name} {ident}\n".encode())
        streaming = (rpc.get("method") == "tools/call" and rpc.get("id") is not None
                     and "text/event-stream" in self.headers.get("Accept", ""))
        deadline = time.monotonic() + 570
        next_heartbeat = time.monotonic()
        try:
            if streaming:
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()
            while not response.exists() and time.monotonic() < deadline:
                if streaming and time.monotonic() >= next_heartbeat:
                    self.wfile.write(b": awaiting tool result\n\n")
                    self.wfile.flush()
                    next_heartbeat = time.monotonic() + 10
                time.sleep(0.05)
            reply = response.read_bytes() if response.exists() else b"502\n"
            status, data = reply.split(b"\n", 1)
            if streaming:
                if status != b"200":
                    data = json.dumps({"jsonrpc": "2.0", "id": rpc["id"], "error": {
                        "code": -32603, "message": "gateway unavailable"}}).encode()
                self.wfile.write(b"event: message\ndata: " + data + b"\n\n")
            else:
                self.send_response(int(status))
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(data)
        finally:
            response.unlink(missing_ok=True)
            request.unlink(missing_ok=True)


def copy_input(cli):
    try:
        index = 0
        while cli.poll() is None:
            path = mailbox / f"stdin-{index}"
            if not path.exists():
                time.sleep(0.025)
                continue
            data = path.read_bytes()
            path.unlink()
            if not data:
                break
            cli.stdin.write(data)
            cli.stdin.flush()
            index += 1
    except BrokenPipeError:
        pass
    finally:
        cli.stdin.close()


def stop(_signum, _frame):
    raise SystemExit(130)


def main():
    for signum in (signal.SIGTERM, signal.SIGINT):
        signal.signal(signum, stop)
    # No approval guard is useful if the workload can reach an unauthenticated
    # execd API to run a root command. Check before starting any user code.
    probe = http.client.HTTPConnection("127.0.0.1", 44772, timeout=3)
    try:
        probe.request("GET", "/files/info?path=/")
        if probe.getresponse().status not in (401, 403):
            raise RuntimeError("OpenSandbox execd must enforce access tokens")
    finally:
        probe.close()
    if ctypes.CDLL(None, use_errno=True).prctl(38, 1, 0, 0, 0) != 0:  # PR_SET_NO_NEW_PRIVS
        raise OSError(ctypes.get_errno(), "cannot restrict CLI privileges")
    server = None
    cli = None
    try:
        prepare_home()
        if token:
            server = ThreadingHTTPServer(("127.0.0.1", port), Relay)
            server.calls = threading.BoundedSemaphore(16)
        env = {name: value for name, value in (os.environ | config["env"]).items()
               if not name.startswith("EXECD_")}
        cli = subprocess.Popen(config["argv"], cwd=cwd, env=env, stdin=subprocess.PIPE,
                               user=uid, group=gid, extra_groups=[], start_new_session=True)
        if server is not None:
            threading.Thread(target=server.serve_forever, daemon=True).start()
        threading.Thread(target=copy_input, args=(cli,), daemon=True).start()
        code = cli.wait()
        return code if code >= 0 else 128 - code
    finally:
        for signum in (signal.SIGTERM, signal.SIGINT):
            signal.signal(signum, signal.SIG_IGN)
        if cli is not None:
            try:
                os.killpg(cli.pid, signal.SIGTERM)
                cli.wait(timeout=1)
                os.killpg(cli.pid, signal.SIGKILL)
            except subprocess.TimeoutExpired:
                os.killpg(cli.pid, signal.SIGKILL)
                cli.wait()
            except ProcessLookupError:
                pass
        if server is not None:
            server.server_close()
        for path, (owner, group, mode) in reversed(mode_before.items()):
            os.chown(path, owner, group, follow_symlinks=False)
            os.chmod(path, mode)


sys.exit(main())
