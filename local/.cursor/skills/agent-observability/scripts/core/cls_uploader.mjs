import { createRequire } from 'node:module';

const MAX_FIELD_LENGTH = 32 * 1024;

function truncate(value) {
  if (value.length <= MAX_FIELD_LENGTH) {
    return value;
  }
  return `${value.slice(0, MAX_FIELD_LENGTH)}...[truncated]`;
}

function stringifyValue(value) {
  if (value === null || value === undefined) {
    return '';
  }
  if (typeof value === 'string') {
    return truncate(value);
  }
  if (typeof value === 'number' || typeof value === 'boolean') {
    return String(value);
  }
  try {
    return truncate(JSON.stringify(value));
  } catch {
    return truncate(String(value));
  }
}

function timestampSeconds(record) {
  const raw = Number(record?.ts);
  if (!Number.isFinite(raw) || raw <= 0) {
    return Math.floor(Date.now() / 1000);
  }
  return raw > 1_000_000_000_000 ? Math.floor(raw / 1000) : Math.floor(raw);
}

async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString('utf8');
}

async function main() {
  const sdkPath = process.argv[2];
  if (!sdkPath) {
    process.exitCode = 2;
    process.stderr.write('missing sdk path\n');
    return;
  }

  const require = createRequire(import.meta.url);
  const { AsyncClient, LogItem, Content, LogGroup, PutLogsRequest } = require(sdkPath);

  const raw = await readStdin();
  const payload = JSON.parse(raw || '{}');
  const records = Array.isArray(payload.records) ? payload.records : [];
  if (!payload.endpoint || !payload.topicId || !payload.secretId || !payload.secretKey || records.length === 0) {
    process.exitCode = 0;
    return;
  }

  const client = new AsyncClient({
    endpoint: payload.endpoint,
    secretId: payload.secretId,
    secretKey: payload.secretKey,
    secretToken: payload.secretToken || '',
    sourceIp: '127.0.0.1',
    retry_times: 3,
  });

  const logGroup = new LogGroup();
  logGroup.setFilename(payload.serviceName || 'agent-observability');

  for (const record of records) {
    const item = new LogItem();
    const merged = {
      service_name: payload.serviceName || 'agent-observability',
      ...record,
    };
    for (const [key, value] of Object.entries(merged)) {
      item.pushBack(new Content(key, stringifyValue(value)));
    }
    item.setTime(timestampSeconds(record));
    logGroup.addLogs(item);
  }

  const request = new PutLogsRequest(payload.topicId, logGroup);
  await client.PutLogs(request);
}

main().catch((error) => {
  process.exitCode = 1;
  process.stderr.write(`${error?.message || String(error)}\n`);
});
