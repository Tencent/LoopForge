// Bridges the browser-level CDP websocket to newline-delimited JSON on stdio,
// so the control plane reaches Chromium through `docker exec` without ports.
const base = `http://127.0.0.1:${process.env.AGENTFLOW_BROWSER_CDP_PORT || 9222}`;
const waitOnly = process.argv.includes('--wait');

async function version(deadline) {
  for (;;) {
    try {
      const res = await fetch(base + '/json/version');
      if (res.ok) return await res.json();
    } catch {}
    if (Date.now() > deadline) throw new Error('chromium devtools endpoint not ready');
    await new Promise((resolve) => setTimeout(resolve, 200));
  }
}

const info = await version(Date.now() + (waitOnly ? 60000 : 15000));
if (waitOnly) process.exit(0);

const ws = new WebSocket(info.webSocketDebuggerUrl);
const queue = [];
let open = false;
let buffer = '';
ws.onopen = () => {
  open = true;
  for (const line of queue.splice(0)) ws.send(line);
  process.stdout.write('{"relay":"ready"}\n');
};
ws.onmessage = (event) => process.stdout.write(String(event.data).replace(/\n/g, '') + '\n');
ws.onclose = () => process.exit(0);
ws.onerror = () => process.exit(1);
process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk) => {
  buffer += chunk;
  let index;
  while ((index = buffer.indexOf('\n')) >= 0) {
    const line = buffer.slice(0, index);
    buffer = buffer.slice(index + 1);
    if (!line.trim()) continue;
    if (open) ws.send(line);
    else queue.push(line);
  }
});
process.stdin.on('end', () => ws.close());
