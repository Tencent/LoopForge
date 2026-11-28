import { test } from "vitest";
import assert from "node:assert/strict";
import {
  collectArtifacts,
  projectChange,
  visibleText,
  mergeEvents,
} from "../src/api.js";
test("real image attachment appears as an authenticated artifact independently of local Markdown paths", () => {
  const message = {
    role: "assistant",
    content: JSON.stringify({
      message:
        "画好了\n![hero])\n| 本地路径 | `/private/generated-images/hero.png` |",
      drafts: [],
    }),
    attachments: [{ id: "image-id", name: "hero.png", mimeType: "image/png" }],
  };
  const artifacts = collectArtifacts({ id: "chat-1", messages: [message] });
  assert.equal(artifacts.length, 1);
  assert.equal(
    artifacts[0].url,
    "/api/assistant/attachments/image-id?sessionId=chat-1",
  );
  assert(!visibleText(message).includes("/private"));
  assert(!visibleText(message).includes("![hero])"));
});
test("image, chart, code and LOOP drafts share one artifact list and remain stable after reload", () => {
  const session = {
    id: "chat-2",
    messages: [
      {
        role: "assistant",
        content: JSON.stringify({
          message: "```mermaid\ngraph LR\nA-->B\n```\n```svg\n<svg/>\n```",
          drafts: [{ tool: "workflows_save", body: { name: "评审 LOOP" } }],
        }),
      },
    ],
  };
  const before = collectArtifacts(session);
  assert.deepEqual(
    before.map((x) => x.kind),
    ["sop", "mermaid", "svg"],
  );
  assert.deepEqual(
    collectArtifacts(JSON.parse(JSON.stringify(session))),
    before,
  );
});
test("project binding allows personal history and preserves established project boundaries", () => {
  assert.equal(projectChange(null, "p1"), "draft");
  assert.equal(projectChange({ projectId: "" }, "p1"), "bind");
  assert.equal(projectChange({ workspaceRunId: "ws" }, "p1"), "new");
  assert.equal(projectChange({ ownWorkspaceRunIds: ["ws"] }, "p1"), "new");
  assert.equal(projectChange({ projectId: "p1" }, "p1"), "same");
  assert.equal(projectChange({ projectId: "p1" }, "p2"), "new");
  assert.equal(projectChange({ projectId: "p1" }, ""), "new");
});
test("streamed answers and tools are not duplicated by full events", () => {
  const state = mergeEvents({ answer: "", thinking: "", tools: [] }, [
    { data: { kind: "answer", text: "你好" } },
    { data: { kind: "answer", text: "你好", full: true } },
    { data: { kind: "tool", id: "one", tool: "search" } },
    { data: { kind: "tool", id: "one", tool: "search" } },
    { data: { kind: "toolDone", id: "one" } },
  ]);
  assert.equal(state.answer, "你好");
  assert.equal(state.tools.length, 1);
  assert.equal(state.tools[0].done, true);
});

test("reply duration uses recorded timestamps without inventing missing history timing", async () => {
  const { elapsedSeconds, formatElapsed } = await import("../src/api.js");
  assert.equal(
    elapsedSeconds("2026-10-27T08:00:00Z", "2026-10-27T08:53:09Z"),
    3189,
  );
  assert.equal(formatElapsed(3189), "53分钟9秒");
  assert.equal(formatElapsed(3609), "1小时9秒");
  assert.equal(formatElapsed(60), "1分钟");
  assert.equal(formatElapsed(0), "不到1秒");
  assert.equal(elapsedSeconds(undefined, "2026-10-27T08:53:09Z"), null);
  assert.equal(
    elapsedSeconds("0001-01-01T00:00:00Z", "2026-10-27T08:53:09Z"),
    null,
  );
  assert.equal(
    elapsedSeconds("2026-10-27T08:00:09Z", "2026-10-27T08:00:00Z"),
    null,
  );
});
test("a requirement draft artifact is listed under its title even when the draft carries name=<id>", () => {
  const session = {
    id: "chat-3",
    messages: [
      {
        role: "assistant",
        content: JSON.stringify({
          message: "已准备好",
          drafts: [
            {
              tool: "issues_save",
              body: {
                id: "issue-1795",
                name: "issue-1795",
                title: "format_currency 负数格式",
              },
            },
            { tool: "agents_save", body: { id: "a", name: "评审员" } },
          ],
        }),
      },
    ],
  };
  assert.deepEqual(
    collectArtifacts(session).map((x) => x.name),
    ["format_currency 负数格式", "评审员"],
  );
});

test("a recent tail names its artifacts by stored message index, like the full transcript", () => {
  const messages = Array.from({ length: 46 }, (_, index) => ({
    role: index % 2 ? "assistant" : "user",
    content: index === 39 || index === 45
      ? JSON.stringify({
        message: "```svg\n<svg/>\n```",
        drafts: [{ tool: "issues_save", body: { title: `DRAFT-${index}` } }],
      })
      : `MSG-${index}`,
  }));
  const full = collectArtifacts({ id: "long", messages });
  const tail = collectArtifacts({ id: "long", messagesPartial: true, messagesOffset: 6, messages: messages.slice(6) });
  const named = (items) => items.map(({ id, name, messageIndex, draftIndex }) => ({ id, name, messageIndex, draftIndex }));
  assert.deepEqual(named(tail), named(full));
  assert.deepEqual(
    full.filter((item) => item.draft).map((item) => [item.id, item.name]),
    [["draft-39-0", "DRAFT-39"], ["draft-45-0", "DRAFT-45"]],
  );
  // A complete transcript ignores a stale offset.
  assert.deepEqual(named(collectArtifacts({ id: "long", messagesOffset: 6, messages })), named(full));
});
