// @vitest-environment node
import { afterEach, test } from "vitest";
import assert from "node:assert/strict";
import {
  browserEnvironment,
  loadPlatformCommit,
  messageTraceId,
  platformCommit,
  resetPlatformCommit,
  traceDiagnostics,
} from "../src/trace-diagnostics.js";
import { NOT_REPORTED } from "../src/message-info.js";

afterEach(resetPlatformCommit);

const turn = [
  { role: "user", content: "第一问", traceId: "af1", createdAt: "2026-11-12T06:00:00Z" },
  {
    role: "assistant",
    traceId: "af1",
    model: "gpt-5.5",
    speakerName: "",
    runIds: ["run-1"],
    startedAt: "2026-11-12T06:00:01Z",
    createdAt: "2026-11-12T06:00:09Z",
  },
];

test("a question and its reply copy the same trace ID", () => {
  assert.equal(messageTraceId(turn, 0), "af1");
  assert.equal(messageTraceId(turn, 1), "af1");
  assert.equal(messageTraceId(turn, 5), "");
});

test("legacy questions borrow the trace ID of their stored reply", () => {
  const legacy = [{ role: "user", content: "旧问题" }, { ...turn[1], traceId: "af9" }];
  assert.equal(messageTraceId(legacy, 0), "af9");
  assert.equal(messageTraceId([{ role: "assistant" }], 0), "");
});

test("a question still on screen uses the generation it just started", () => {
  const sent = [...turn, { role: "user", content: "第二问" }];
  const generation = { id: "af2", messageCount: 3, status: "running" };
  assert.equal(messageTraceId(sent, 2, generation), "af2");
  // A generation for another turn never labels this question.
  assert.equal(messageTraceId(sent, 2, { id: "af2", messageCount: 5 }), "");
  assert.equal(messageTraceId(sent, 0, generation), "af1");
});

test("diagnostics list trace, conversation, agent, model and environment", () => {
  const text = traceDiagnostics({
    messages: turn,
    index: 1,
    session: { id: "s1", executor: "codex", model: "gpt-5.5", projectId: "p1" },
    environment: {
      page: "https://lf.example/chatbot/",
      network: "在线 · 4g",
      browser: "UA/1",
      locale: "Asia/Shanghai · zh-CN",
      version: "60fc455",
    },
  });
  const lines = text.split("\n");
  assert.equal(lines[0], "LoopForge 排障信息");
  for (const want of [
    "Trace ID: af1",
    "会话 ID: s1",
    "消息: 第 2 条 · 回复",
    "Agent: 主 Bot",
    "执行器: codex",
    "模型: gpt-5.5",
    "项目: p1",
    "关联运行: run-1",
    "平台版本: 60fc455",
    "页面: https://lf.example/chatbot/",
    "网络: 在线 · 4g",
    "浏览器: UA/1",
    "时区/语言: Asia/Shanghai · zh-CN",
  ])
    assert.ok(lines.includes(want), `missing ${want} in\n${text}`);
});

test("missing values read 「未上报」 instead of disappearing", () => {
  const text = traceDiagnostics({
    messages: [{ role: "user", content: "刚发出" }],
    index: 0,
    session: null,
    environment: {},
  });
  for (const key of ["Trace ID", "会话 ID", "执行器", "模型", "平台版本", "网络"])
    assert.ok(text.includes(`${key}: ${NOT_REPORTED}`), `${key} in\n${text}`);
  assert.ok(text.includes("消息: 第 1 条 · 提问"));
});

test("the environment keeps the page path but drops its query string", () => {
  const env = browserEnvironment({
    navigator: {
      onLine: true,
      userAgent: " UA/2 ",
      language: "zh-CN",
      connection: { effectiveType: "4g", rtt: 50, downlink: 10 },
    },
    location: {
      origin: "https://lf.example",
      pathname: "/chatbot/",
      search: "?token=secret",
    },
  });
  assert.equal(env.page, "https://lf.example/chatbot/");
  assert.equal(env.network, "在线 · 4g · RTT 50ms · 下行 10Mbps");
  assert.equal(env.browser, "UA/2");
  assert.match(env.locale, /zh-CN$/);
  assert.equal(browserEnvironment({ navigator: { onLine: false } }).network, "离线");
  assert.equal(browserEnvironment({}).network, NOT_REPORTED);
});

test("the platform build is read once and a failure can be retried", async () => {
  let calls = 0;
  const read = async (path) => {
    calls++;
    assert.equal(path, "/api/health");
    return { status: "ok", commit: "60fc455" };
  };
  assert.equal(await loadPlatformCommit(read), "60fc455");
  assert.equal(await loadPlatformCommit(read), "60fc455");
  assert.equal(calls, 1);
  assert.equal(platformCommit(), "60fc455");
  assert.equal(browserEnvironment({}).version, "60fc455");

  resetPlatformCommit();
  assert.equal(await loadPlatformCommit(async () => { throw new Error("offline"); }), "");
  assert.equal(await loadPlatformCommit(read), "60fc455");
});
