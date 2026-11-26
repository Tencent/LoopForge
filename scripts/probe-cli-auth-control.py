#!/usr/bin/env python3
"""Opt-in, offline official-CLI auth callback experiment (macOS only).

Uses fake tokens, private temporary homes, a local TLS issuer/model stub, and
sandbox-exec to deny real credential access and non-loopback networking. No
provider credentials are read and no real model is called. This is a protocol
experiment, not a production auth implementation or provider compatibility claim.
"""

import argparse
import base64
import contextlib
import http.server
import json
import os
from pathlib import Path
import queue
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request


def encoded(value):
    return base64.urlsafe_b64encode(json.dumps(value).encode()).rstrip(b"=").decode()


def access_token(kind, generation):
    if kind == "claude":
        return "sk-ant-" + "oat01-probe-not-a-real-token-" + str(generation)
    return encoded({"alg": "none"}) + "." + encoded({
        "sub": "fake-user-a", "email": "owner@example.test", "jti": str(generation),
        "exp": int(time.time()) + 3600,
        "https://api.openai.com/auth": {
            "chatgpt_account_id": "fake-account-a", "chatgpt_user_id": "fake-user-a",
            "chatgpt_plan_type": "plus",
        },
    }) + ".fake-signature"


def certificates(root):
    ca_config = root / "ca.cnf"
    ca_config.write_text("[req]\nprompt=no\ndistinguished_name=dn\nx509_extensions=ca\n"
                         "[dn]\nCN=LoopForge isolated fake CA\n[ca]\n"
                         "basicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\n")
    extensions = root / "leaf.ext"
    extensions.write_text("basicConstraints=critical,CA:FALSE\n"
                          "subjectAltName=IP:127.0.0.1,DNS:localhost,DNS:api.anthropic.com,"
                          "DNS:console.anthropic.com,DNS:platform.claude.com,DNS:claude.ai\n"
                          "keyUsage=critical,digitalSignature,keyEncipherment\n"
                          "extendedKeyUsage=serverAuth\n")
    commands = [
        ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", "ca.key", "-out", "ca.pem",
         "-days", "1", "-config", str(ca_config)],
        ["req", "-newkey", "rsa:2048", "-nodes", "-keyout", "leaf.key", "-out", "leaf.csr",
         "-subj", "/CN=localhost"],
        ["x509", "-req", "-in", "leaf.csr", "-CA", "ca.pem", "-CAkey", "ca.key", "-CAcreateserial",
         "-out", "leaf.pem", "-days", "1", "-extfile", str(extensions)],
    ]
    for command in commands:
        subprocess.run(["/usr/bin/openssl", *command], cwd=root, check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for path in root.iterdir():
        if path.is_file():
            path.chmod(0o600)


class Issuer:
    def __init__(self, kind):
        self.kind = kind
        self.lock = threading.Lock()
        self.refresh_calls = 0
        self.refused_requests = 0
        self.model_requests = 0
        self.successful_model_requests = 0
        self.blocked_egress = 0
        self.refresh_token = "runner-owned-fake-refresh-0"

    def handler(self):
        issuer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def reply(self, code, data):
                body = json.dumps(data).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_CONNECT(self):
                with issuer.lock:
                    issuer.blocked_egress += 1
                self.reply(502, {"error": "offline probe blocks external destinations"})

            def do_GET(self):
                if "/accounts/check" in self.path:
                    if self.headers.get("Authorization") != "Bearer " + access_token("codex", 1):
                        # The fake JWT contains an expiry timestamp, so compare its generation.
                        bearer = self.headers.get("Authorization", "").removeprefix("Bearer ")
                        try:
                            claims = json.loads(base64.urlsafe_b64decode(bearer.split(".")[1] + "=="))
                            valid = claims.get("jti") == "1"
                        except (ValueError, IndexError):
                            valid = False
                        if not valid:
                            with issuer.lock:
                                issuer.refused_requests += 1
                            self.reply(401, {"error": {"type": "authentication_error", "code": "token_expired"}})
                            return
                    self.reply(200, {"accounts": [{
                        "id": "fake-account-a", "plan_type": "plus",
                        "workspace_backend_origin": "https://" + self.headers["Host"],
                        "account_routing_override": "NO_CONSTRAINT", "name": "Fake isolated account",
                    }]})
                else:
                    self.reply(200, {"models": [], "data": [], "settings": {}})

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
                if self.path in {"/probe-token", "/v1/oauth/token"}:
                    request = json.loads(body)
                    with issuer.lock:
                        if request.get("refresh_token") != issuer.refresh_token:
                            self.reply(400, {"error": "invalid_grant"})
                            return
                        issuer.refresh_calls += 1
                        issuer.refresh_token = "runner-owned-fake-refresh-" + str(issuer.refresh_calls)
                        token = access_token(issuer.kind, issuer.refresh_calls)
                        self.reply(200, {"access_token": token, "refresh_token": issuer.refresh_token, "expires_in": 3600})
                    return
                if self.path.split("?")[0].endswith("/messages/count_tokens"):
                    self.reply(200, {"input_tokens": 1})
                    return
                if self.path.split("?")[0].endswith("/messages"):
                    with issuer.lock:
                        issuer.model_requests += 1
                    if self.headers.get("Authorization") != "Bearer " + access_token("claude", 1):
                        with issuer.lock:
                            issuer.refused_requests += 1
                        self.reply(401, {"type": "error", "error": {
                            "type": "authentication_error", "message": "OAuth token has expired. Please obtain a new token or refresh your existing token.",
                        }})
                        return
                    with issuer.lock:
                        issuer.successful_model_requests += 1
                    events = [
                        ("message_start", {"type": "message_start", "message": {
                            "id": "msg_fake_probe", "type": "message", "role": "assistant", "content": [],
                            "model": "claude-sonnet-4-6", "stop_reason": None, "stop_sequence": None,
                            "usage": {"input_tokens": 1, "output_tokens": 0},
                        }}),
                        ("content_block_start", {"type": "content_block_start", "index": 0,
                                                 "content_block": {"type": "text", "text": ""}}),
                        ("content_block_delta", {"type": "content_block_delta", "index": 0,
                                                 "delta": {"type": "text_delta", "text": "OFFLINE_AUTH_OK"}}),
                        ("content_block_stop", {"type": "content_block_stop", "index": 0}),
                        ("message_delta", {"type": "message_delta", "delta": {
                            "stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 1}}),
                        ("message_stop", {"type": "message_stop"}),
                    ]
                    payload = "".join("event: " + event + "\ndata: " + json.dumps(data) + "\n\n"
                                      for event, data in events).encode()
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.send_header("Content-Length", str(len(payload)))
                    self.end_headers()
                    self.wfile.write(payload)
                    return
                self.reply(200, {})

        return Handler

    def proxy_handler(self, tls):
        issuer = self

        class Proxy(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_CONNECT(self):
                # Emulate official hostnames locally, so the CLI exercises its
                # first-party OAuth branch. Never forward to an external host.
                if self.path not in {host + ":443" for host in [
                        "api.anthropic.com", "console.anthropic.com", "platform.claude.com", "claude.ai"]}:
                    with issuer.lock:
                        issuer.blocked_egress += 1
                    self.send_error(502, "offline probe blocks this destination")
                    return
                self.send_response(200, "Connection established")
                self.end_headers()
                self.wfile.flush()
                try:
                    secure = tls.wrap_socket(self.connection, server_side=True)
                    issuer.handler()(secure, self.client_address, self.server)
                except (ConnectionError, ssl.SSLError):
                    pass
                finally:
                    self.close_connection = True

        return Proxy


class Broker:
    """Test-only single writer: all fake refresh tokens stay in this process."""
    def __init__(self, root, issuer, url, ca):
        self.root, self.issuer, self.url = root, issuer, url
        self.lock = threading.Lock()
        self.generation = 0
        self.callbacks = 0
        self.token = "runner-owned-fake-refresh-0"
        self.access = access_token(issuer.kind, 0)
        self.http = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(ca))),
        )

    def fresh(self, stale_generation):
        with self.lock:
            self.callbacks += 1
            if stale_generation < self.generation:
                return self.access, self.generation
            request = urllib.request.Request(self.url + "/probe-token", method="POST",
                                             headers={"Content-Type": "application/json"},
                                             data=json.dumps({"refresh_token": self.token}).encode())
            with self.http.open(request, timeout=5) as response:
                canonical = json.load(response)
            path = self.root / "runner-private.json"
            pending = self.root / "runner-private.pending"
            with pending.open("w", encoding="utf-8") as file:
                os.chmod(pending, 0o600)
                json.dump(canonical, file)
                file.flush()
                os.fsync(file.fileno())
            os.replace(pending, path)
            directory = os.open(self.root, os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
            self.token, self.access = canonical["refresh_token"], canonical["access_token"]
            self.generation += 1
            return self.access, self.generation


class Child:
    def __init__(self, root, binary, kind, url, ca, proxy_url, claude_entrypoint):
        self.root, self.kind = root, kind
        self.generation = 0
        self.events = queue.Queue()
        self.home = root / "home"
        self.home.mkdir(parents=True, mode=0o700)
        config = self.home / (".codex" if kind == "codex" else ".claude")
        config.mkdir(mode=0o700)
        real_home = Path.home()
        profile = '(version 1) (allow default) (deny network*) '
        profile += '(allow network-outbound (remote ip "localhost:*")) '
        for path in [real_home / ".codex", real_home / ".claude", real_home / ".gemini", real_home / "Library/Keychains"]:
            profile += '(deny file-read* file-write* (subpath ' + json.dumps(str(path)) + ')) '
        profile += '(deny file-write* (subpath ' + json.dumps(str(real_home)) + ')) '
        env = {
            "PATH": "/usr/bin:/bin", "HOME": str(self.home), "TMPDIR": str(root), "SHELL": "/bin/sh",
            "XDG_CONFIG_HOME": str(self.home), "XDG_DATA_HOME": str(self.home), "XDG_CACHE_HOME": str(self.home),
            "HTTP_PROXY": proxy_url, "HTTPS_PROXY": proxy_url, "ALL_PROXY": proxy_url, "NO_PROXY": "127.0.0.1,localhost",
            "SSL_CERT_FILE": str(ca), "NODE_EXTRA_CA_CERTS": str(ca), "DO_NOT_TRACK": "1", "NO_COLOR": "1",
        }
        if kind == "codex":
            env.update({"CODEX_HOME": str(config), "CODEX_CA_CERTIFICATE": str(ca), "RUST_LOG": "off",
                        "CODEX_APP_SERVER_CHATGPT_BASE_URL": url + "/backend-api/", "OPENAI_BASE_URL": url + "/v1/"})
            (config / "config.toml").write_text('cli_auth_credentials_store = "file"\nchatgpt_base_url = '
                                               + json.dumps(url + "/backend-api/") + '\n')
            args = ["app-server"]
        else:
            env.update({"CLAUDE_CONFIG_DIR": str(config),
                        "CLAUDE_CODE_OAUTH_TOKEN": access_token("claude", 0),
                        "CLAUDE_CODE_ENTRYPOINT": claude_entrypoint, "CLAUDE_AGENT_SDK_VERSION": "0.3.326",
                        "CLAUDE_CODE_SDK_HAS_OAUTH_REFRESH": "1",
                        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1"})
            (self.home / ".claude.json").write_text(json.dumps({"hasCompletedOnboarding": True}))
            args = ["--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
                    "--model", "claude-sonnet-4-6", "--max-turns", "1", "--tools", "", "--strict-mcp-config",
                    "--setting-sources", ""]
        self.stderr_path = root / "stderr.log"
        self.stderr = self.stderr_path.open("wb")
        self.process = subprocess.Popen(["/usr/bin/sandbox-exec", "-p", profile, binary, *args],
                                        cwd=root, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.stderr, text=True)
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()

    def read(self):
        try:
            for line in self.process.stdout:
                try:
                    self.events.put(json.loads(line))
                except ValueError:
                    pass
        finally:
            self.events.put(None)

    def send(self, message):
        self.process.stdin.write(json.dumps(message) + "\n")
        self.process.stdin.flush()

    def next(self, broker):
        while True:
            event = self.events.get(timeout=45)
            if event is None:
                raise RuntimeError("isolated CLI exited before completing the probe")
            if self.kind == "codex" and event.get("method") == "account/chatgptAuthTokens/refresh":
                if event.get("params", {}).get("previousAccountId") != "fake-account-a":
                    raise RuntimeError("refresh callback changed account")
                token, self.generation = broker.fresh(self.generation)
                self.send({"id": event["id"], "result": {
                    "accessToken": token, "chatgptAccountId": "fake-account-a", "chatgptPlanType": "plus",
                }})
            elif self.kind == "claude" and event.get("type") == "control_request":
                subtype = event.get("request", {}).get("subtype")
                if subtype != "oauth_token_refresh":
                    raise RuntimeError("unexpected control request: " + str(subtype))
                token, self.generation = broker.fresh(self.generation)
                self.send({"type": "control_response", "response": {
                    "subtype": "success", "request_id": event["request_id"], "response": {"accessToken": token},
                }})
            else:
                return event

    def rpc(self, method, params, request_id, broker):
        self.send({"id": request_id, "method": method, "params": params})
        while True:
            event = self.next(broker)
            if event.get("id") == request_id:
                if "error" in event:
                    raise RuntimeError("isolated RPC failed: " + json.dumps(event["error"]))
                return event.get("result")

    def exercise(self, broker):
        if self.kind == "codex":
            self.rpc("initialize", {"clientInfo": {"name": "loopforge-external-auth-probe", "version": "0.1.0"},
                                    "capabilities": {"experimentalApi": True}}, 1, broker)
            self.send({"method": "initialized"})
            self.rpc("account/login/start", {"type": "chatgptAuthTokens", "accessToken": access_token("codex", 0),
                                              "chatgptAccountId": "fake-account-a", "chatgptPlanType": "plus"}, 2, broker)
            result = self.rpc("account/read", {"refreshToken": False}, 3, broker)
            if not result.get("account") or self.generation != 1:
                raise RuntimeError("external login did not recover through a refresh callback")
        else:
            self.send({"type": "control_request", "request_id": "probe-init", "request": {"subtype": "initialize"}})
            while True:
                event = self.next(broker)
                if event.get("type") == "control_response" and event.get("response", {}).get("request_id") == "probe-init":
                    if event["response"].get("subtype") != "success":
                        raise RuntimeError("Claude initialization failed")
                    break
            self.send({"type": "user", "session_id": "", "message": {"role": "user", "content": "Reply OFFLINE_AUTH_OK."}})
            while True:
                event = self.next(broker)
                if event.get("type") == "result":
                    if event.get("is_error") or "OFFLINE_AUTH_OK" not in event.get("result", "") or self.generation != 1:
                        raise RuntimeError("Claude did not recover through host refresh: " + json.dumps({
                            "result": event.get("result"), "errors": event.get("errors"),
                            "generation": self.generation})[:500])
                    break
        for path in self.home.rglob("*"):
            if path.is_file() and "runner-owned-fake-refresh" in path.read_text(errors="ignore"):
                raise RuntimeError("a sandbox received the Runner-owned fake refresh token")
        return {"passed": True, "refreshGeneration": self.generation}

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
        try:
            self.process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait()
        self.stderr.close()
        self.process.stdin.close()
        self.process.stdout.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=["codex", "claude"])
    parser.add_argument("--binary", help="absolute official CLI binary path")
    parser.add_argument("--serve-only", action="store_true", help="serve an offline fixture for Go integration tests until stdin closes")
    parser.add_argument("--clients", type=int, default=3, choices=[1, 3])
    parser.add_argument("--claude-entrypoint", default="sdk-ts", choices=["sdk-ts", "local-agent"],
                        help="local-agent probes a restricted internal CLI path; NOT a supported public SDK contract")
    parser.add_argument("--expect-unsupported-sdk", action="store_true",
                        help="negative control: require ordinary sdk-ts to refuse auth without asking the host")
    options = parser.parse_args()
    if not options.serve_only and (not options.binary or not Path(options.binary).is_absolute() or not Path("/usr/bin/sandbox-exec").exists()):
        raise RuntimeError("an absolute binary and macOS sandbox-exec are required")
    with tempfile.TemporaryDirectory(prefix="lf-auth-control-") as directory:
        root = Path(directory)
        certificates(root)
        issuer = Issuer(options.kind)
        class QuietServer(http.server.ThreadingHTTPServer):
            def handle_error(self, *_):
                if not issubclass(sys.exc_info()[0], (ConnectionResetError, BrokenPipeError, ssl.SSLError)):
                    super().handle_error(*_)
        server = QuietServer(("127.0.0.1", 0), issuer.handler())
        server.daemon_threads = True
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(str(root / "leaf.pem"), str(root / "leaf.key"))
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        proxy = QuietServer(("127.0.0.1", 0), issuer.proxy_handler(tls))
        proxy.daemon_threads = True
        threading.Thread(target=proxy.serve_forever, daemon=True).start()
        url = "https://127.0.0.1:" + str(server.server_port)
        if options.serve_only:
            try:
                print(json.dumps({"url": url, "proxy": "http://127.0.0.1:" + str(proxy.server_port), "ca": str(root / "ca.pem")}), flush=True)
                sys.stdin.readline()
                print(json.dumps({"refreshes": issuer.refresh_calls, "refusals": issuer.refused_requests,
                                  "successes": issuer.successful_model_requests}), flush=True)
            finally:
                server.shutdown()
                server.server_close()
                proxy.shutdown()
                proxy.server_close()
            return
        broker = Broker(root, issuer, url, root / "ca.pem")
        children, failures, results = [], [], []
        try:
            for index in range(options.clients):
                children.append(Child(root / ("client-" + str(index)), options.binary, options.kind, url,
                                      root / "ca.pem", "http://127.0.0.1:" + str(proxy.server_port), options.claude_entrypoint))
            def exercise(child):
                try:
                    results.append(child.exercise(broker))
                except Exception as error:
                    failures.append(str(error) or type(error).__name__)
            threads = [threading.Thread(target=exercise, args=(child,)) for child in children]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join(timeout=55)
            if any(thread.is_alive() for thread in threads):
                failures.append("client did not complete within the probe deadline")
            passed = not failures and len(results) == options.clients and issuer.refresh_calls == 1
            expected_unavailable = False
            if options.expect_unsupported_sdk:
                expected_unavailable = (options.kind == "claude" and options.claude_entrypoint == "sdk-ts"
                                        and len(failures) == options.clients and not results
                                        and all(item.startswith("Claude did not recover through host refresh:") for item in failures)
                                        and broker.callbacks == 0 and issuer.refresh_calls == 0
                                        and issuer.refused_requests >= options.clients)
                passed = expected_unavailable
            print(json.dumps({"kind": options.kind, "clients": options.clients, "passed": passed,
                              "claudeEntrypoint": options.claude_entrypoint if options.kind == "claude" else None,
                              "expectedCallbackUnavailable": expected_unavailable,
                              "callbacks": broker.callbacks, "issuerRefreshCalls": issuer.refresh_calls,
                              "refusedRequests": issuer.refused_requests, "modelRequests": issuer.model_requests,
                              "successfulFakeModelRequests": issuer.successful_model_requests,
                              "blockedEgress": issuer.blocked_egress, "failures": failures}, ensure_ascii=False))
            if not passed:
                # Only fake credentials can occur here; keep diagnostics short.
                for child in children:
                    child.close()
                    text = child.stderr_path.read_text(errors="replace")
                    if text:
                        print("isolated CLI diagnostics:", text[-1600:])
                raise SystemExit(1)
        finally:
            for child in children:
                with contextlib.suppress(Exception):
                    child.close()
            server.shutdown()
            server.server_close()
            proxy.shutdown()
            proxy.server_close()


if __name__ == "__main__":
    main()
