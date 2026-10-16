"""Keep Classic's real asynchronous team alive in a terminal until completion.

A one-shot `-p` reply can mean 'the team is running', not 'the task is done'.
This opt-in harness checks the saved completion state before closing the TUI.
Terminal output is consumed but never printed or published.
"""
import json
import os
import select
import signal
import time


def run_classic(binary, project, env, model, prompt, timeout=900):
    import pty
    import fcntl
    import struct
    import termios

    pid, master = pty.fork()
    if pid == 0:
        os.chdir(project)
        os.execvpe(binary, [binary, '--model', model, '--permission-mode', 'bypassPermissions',
                           '--strict-mcp-config', '--mcp-config', '{"mcpServers":{}}',
                           '--setting-sources', 'project,local', prompt],
                   dict(env, TERM='xterm-256color', NO_COLOR='1'))
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack('HHHH', 48, 160, 0, 0))
    deadline = time.monotonic() + timeout
    completed = False
    trust_confirmed = False
    tail = b''
    exited = False
    try:
        while time.monotonic() < deadline:
            ready, _, _ = select.select([master], [], [], 0.5)
            if ready:
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    break
                if not chunk:
                    break
                if b'\x1b[6n' in chunk:
                    os.write(master, b'\x1b[1;1R')
                tail = (tail + chunk)[-24000:]
                # The fixture was created by this test. Confirm only the
                # workspace trust screen, never unrelated permission prompts.
                if not trust_confirmed and b'trust' in tail.lower() and b'folder' in tail.lower():
                    os.write(master, b'\r')
                    trust_confirmed = True
            for state_file in (project / 'artifacts').rglob('workflow-state.json'):
                try:
                    state = json.loads(state_file.read_text())
                except (ValueError, OSError):
                    continue
                if state.get('summary', {}).get('status') == 'completed':
                    completed = True
                    break
            if completed:
                break
            finished, _ = os.waitpid(pid, os.WNOHANG)
            if finished:
                exited = True
                break
    finally:
        if not exited:
            try:
                os.killpg(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            for _ in range(30):
                finished, _ = os.waitpid(pid, os.WNOHANG)
                if finished:
                    exited = True
                    break
                time.sleep(0.1)
            if not exited:
                os.killpg(pid, signal.SIGKILL)
                os.waitpid(pid, 0)
        os.close(master)
    # Use actual persisted CLI transcripts to check model usage, not TUI text.
    tokens = 0
    for transcript in (project / '.test-codebuddy-config').rglob('*.jsonl'):
        for line in transcript.read_text(errors='replace').splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            usage = event.get('usage') or event.get('message', {}).get('usage') or {}
            tokens += sum(usage.get(k, 0) or 0 for k in ('input_tokens', 'output_tokens', 'cache_read_input_tokens'))
    return {'type': 'result', 'is_error': not completed, 'usage': {'input_tokens': tokens},
            'result': 'Interactive team completion observed' if completed else 'Interactive team did not complete'}
