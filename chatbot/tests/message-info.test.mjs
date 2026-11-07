import { test } from "vitest";
import assert from "node:assert/strict";
import {
  NOT_REPORTED,
  formatTokens,
  fullTime,
  messageInfo,
  messageSentAt,
  messageTimeLabel,
} from "../src/message-info.js";

const row = (info, key) => info.rows.find((r) => r.key === key);

test("new replies show model, elapsed time and token totals", () => {
  const info = messageInfo({
    startedAt: "2026-10-31T04:04:12",
    createdAt: "2026-10-31T04:05:39",
    model: "claude-opus-4-5",
    usage: {
      inputTokens: 4,
      outputTokens: 493,
      cacheReadTokens: 253736,
      cacheWriteTokens: 0,
      totalTokens: 497,
    },
  });
  assert.deepEqual(
    info.rows.map((r) => [r.label, r.value]),
    [
      ["时间", "2026-10-31 04:04"],
      ["Agent", "主 Bot"],
      ["编码工具", "CodeBuddy"],
      ["模型", "claude-opus-4-5"],
      ["用时", "1分钟27秒"],
    ],
  );
  assert.equal(info.total, 254233);
  assert.equal(info.totalText, "254,233");
  assert.deepEqual(
    info.tokens.map((p) => [p.label, p.text, p.zero]),
    [
      ["输入", "4", false],
      ["输出", "493", false],
      ["缓存读", "253,736", false],
      ["缓存写", "0", true],
    ],
  );
});

test("legacy replies fall back to 「未上报」 without token details", () => {
  const info = messageInfo({
    createdAt: "2026-10-31T03:52:48",
    speakerName: "前端顾问",
  });
  assert.equal(row(info, "time").value, "2026-10-31 03:52");
  assert.equal(row(info, "agent").value, "前端顾问");
  assert.equal(row(info, "model").value, NOT_REPORTED);
  assert.equal(row(info, "model").missing, true);
  assert.equal(row(info, "elapsed").value, NOT_REPORTED);
  assert.equal(info.total, null);
  assert.equal(info.totalText, NOT_REPORTED);
  assert.deepEqual(info.tokens, []);
});

test("short replies and malformed values stay readable", () => {
  const info = messageInfo({
    startedAt: "2026-10-31T03:52:48Z",
    createdAt: "2026-10-31T03:52:48.400Z",
    model: "  ",
    speakerName: " ",
    usage: { inputTokens: "12", outputTokens: -3 },
  });
  assert.equal(row(info, "elapsed").value, "不到1秒");
  assert.equal(row(info, "model").value, NOT_REPORTED);
  assert.equal(row(info, "agent").value, "主 Bot");
  assert.equal(info.total, 12);
  assert.equal(fullTime("not a date"), NOT_REPORTED);
  assert.equal(formatTokens(1234567), "1,234,567");
  assert.deepEqual(
    messageInfo()
      .rows.map((r) => r.value)
      .slice(1, 3),
    ["主 Bot", "CodeBuddy"],
  );
});

test("every message has a send time; legacy questions use their reply's start", () => {
  const messages = [
    { role: "user", content: "旧问题", createdAt: "0001-01-01T00:00:00Z" },
    { role: "assistant", startedAt: "2026-11-01T12:24:51", createdAt: "2026-11-01T12:25:36" },
    { role: "user", content: "新问题", createdAt: "2026-11-01T12:30:00" },
    { role: "assistant", createdAt: "2026-11-01T12:31:00" },
    { role: "user", content: "未回复" },
  ];
  assert.equal(fullTime(messageSentAt(messages, 0)), "2026-11-01 12:24");
  assert.equal(fullTime(messageSentAt(messages, 1)), "2026-11-01 12:25");
  assert.equal(fullTime(messageSentAt(messages, 2)), "2026-11-01 12:30");
  assert.equal(messageSentAt(messages, 4), null);
  assert.equal(messageSentAt(messages, 9), null);
  assert.equal(messageSentAt([{ role: "assistant", createdAt: "0001-01-01T00:00:00Z" }], 0), null);
});

test("time labels add the date only when it is not today", () => {
  const now = new Date(2026, 10, 1, 18, 0);
  assert.equal(messageTimeLabel(new Date(2026, 10, 1, 9, 5), now), "09:05");
  assert.equal(messageTimeLabel(new Date(2026, 9, 31, 23, 59), now), "10-31 23:59");
  assert.equal(messageTimeLabel(new Date(2025, 11, 1, 8, 0), now), "2025-12-01 08:00");
  assert.equal(messageTimeLabel(null, now), "");
});
