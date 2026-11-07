import { test } from "vitest";
import assert from "node:assert/strict";
import {
  definitionTarget,
  projectEntryTarget,
  reconcileOperation,
  visibleSidebarArtifact,
  actionName,
} from "../src/platform.js";
import { nodeConversationItems } from "../src/node-conversation.js";

test("saved clone opens the new workflow, while unconfirmed drafts never link to the source", () => {
  const artifact = {
    kind: "sop",
    draft: {
      tool: "workflows_clone",
      path: { id: "source" },
      body: { id: "copy", projectId: "项目" },
      saved: true,
      savedResult: { id: "copy" },
    },
  };
  assert.equal(
    definitionTarget(artifact).href,
    "/?projectId=%E9%A1%B9%E7%9B%AE#/flow/copy",
  );
  assert.equal(
    definitionTarget({
      ...artifact,
      draft: { ...artifact.draft, saved: false },
    }),
    null,
  );
  assert.equal(
    definitionTarget({ kind: "role", asset: { id: "role / one" } }, "project")
      .href,
    "/?projectId=project#/agents/role%20%2F%20one",
  );
});
test("only the matching approved node resolves a historical approval draft", () => {
  const artifact = {
    draft: {
      tool: "runs_approve",
      path: { id: "run-1" },
      body: { nodeId: "review" },
    },
  };
  assert.equal(
    reconcileOperation(artifact, [
      { id: "other", nodes: { review: { status: "succeeded" } } },
    ]),
    artifact,
  );
  assert.equal(
    reconcileOperation(artifact, [
      {
        id: "run-1",
        nodes: {
          other: { status: "succeeded" },
          review: { status: "waiting_approval" },
        },
      },
    ]),
    artifact,
  );
  const resolved = reconcileOperation(artifact, [
    { id: "run-1", nodes: { review: { status: "succeeded" } } },
  ]);
  assert.equal(resolved.draft.resolved, true);
  assert.equal(visibleSidebarArtifact(resolved), false);
  assert.equal(artifact.draft.resolved, undefined);
  assert.equal(actionName("runs_approve"), "批准节点");
});
test("files stay at their own publication position and same-named node outputs never cross", () => {
  const files = [
    { id: "one", nodeId: "a", name: "result", attempt: 1 },
    { id: "two", nodeId: "b", name: "result", attempt: 1 },
  ];
  const node = { nodeId: "a", output: "完成" };
  const events = [
    { sequence: 1, category: "output", message: "完成" },
    {
      sequence: 2,
      category: "artifact",
      name: "artifact.published",
      attributes: { artifactId: "one" },
    },
    { sequence: 3, category: "tool", message: "验证后继续" },
  ];
  const items = nodeConversationItems(events, files, node);
  assert.deepEqual(
    items.map((x) => x.type),
    ["reply", "artifact", "activity"],
  );
  assert.equal(items[1].artifact.id, "one");
  assert.equal(items.filter((x) => x.type === "reply").length, 1);
  assert.equal(
    nodeConversationItems([], files, node, { hasMore: true }).length,
    0,
  );
});
test("tool start/finish coalesce, mirrored hooks disappear and legacy files remain readable", () => {
  const events = [
    {
      sequence: 1,
      category: "tool",
      source: { adapter: "anthropic-stream-v1" },
      tool: { name: "Read", callId: "one" },
      message: "Read /tmp/report.md",
      phase: "start",
    },
    {
      sequence: 2,
      category: "tool",
      source: { adapter: "agent-hooks-v1" },
      tool: { name: "Read", callId: "hook-copy" },
      message: "Read",
    },
    {
      sequence: 3,
      category: "tool",
      source: { adapter: "anthropic-stream-v1" },
      tool: { name: "Read", callId: "one" },
      message: "Read 完成",
      phase: "finish",
    },
  ];
  const items = nodeConversationItems(
    events,
    [{ id: "legacy", nodeId: "a", name: "result" }],
    { nodeId: "a", output: "完成" },
  );
  assert.deepEqual(
    items.map((x) => x.type),
    ["activity", "reply", "artifact"],
  );
  assert.equal(items[0].event.phase, "finish");
  assert.equal(items[0].event.request, "Read /tmp/report.md");
  assert.equal(items[0].event.message, "Read 完成");
});

test("internal resume prompts render as acknowledgements only with a matching saved receipt", async () => {
  const { confirmationCaption } = await import("../src/api.js");
  const message = {
    role: "user",
    content:
      "已确认并执行 agents_save。请核对这条草稿的保存回执，继续完成原始请求；不要重复刚才的写入，下一项需要确认时提交草稿，全部完成时总结实际结果。",
  };
  const previous = {
    role: "assistant",
    content: JSON.stringify({
      message: "",
      drafts: [{ tool: "agents_save", saved: true }],
    }),
  };
  assert.equal(
    confirmationCaption(message, previous),
    "已确认保存角色，继续处理",
  );
  assert.equal(
    confirmationCaption(message, {
      ...previous,
      content: JSON.stringify({
        message: "",
        drafts: [{ tool: "agents_save", saved: false }],
      }),
    }),
    "",
  );
  assert.equal(confirmationCaption(message, null), "");
  assert.equal(
    confirmationCaption(
      { ...message, content: message.content + "用户补充" },
      previous,
    ),
    "",
  );
});
test("a project created from another project's chat links to the new project", () => {
  const draft = {
    tool: "projects_save",
    body: { id: "okr-system", name: "OKR系统" },
    saved: true,
    savedResult: { id: "okr-system", name: "OKR系统" },
  };
  assert.deepEqual(projectEntryTarget(draft, "skillhub"), {
    href: "/?projectId=okr-system#/collaboration",
    label: "进入项目「OKR系统」继续对话",
  });
  assert.equal(projectEntryTarget({ ...draft, saved: false }, "skillhub"), null);
  assert.equal(projectEntryTarget(draft, "okr-system"), null);
  assert.equal(
    projectEntryTarget({ tool: "agents_save", body: { id: "a" }, saved: true }),
    null,
  );
  assert.equal(
    projectEntryTarget({ tool: "projects_save", body: { id: "a b" }, saved: true, savedResult: { data: { id: "a b" } } }).href,
    "/?projectId=a%20b#/collaboration",
  );
});
