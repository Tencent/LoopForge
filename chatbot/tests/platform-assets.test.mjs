import { test } from "vitest";
import assert from "node:assert/strict";
import { collectArtifacts, mergeEvents } from "../src/api.js";
test("automatic platform writes become artifacts; confirmation receipts merge instead of duplicating the saved asset", () => {
  const role = {
    kind: "role",
    id: "p-role",
    name: "分析员",
    tool: "agents_save",
  };
  const session = {
    messages: [
      {
        role: "assistant",
        assets: [
          role,
          { kind: "sop", id: "p-sop", name: "流程", status: "published" },
          { kind: "run", id: "run-1" },
        ],
        content: '{"message":"保存成功","drafts":[]}',
      },
      {
        role: "assistant",
        content: JSON.stringify({
          message: "确认成功",
          drafts: [
            {
              tool: "agents_save",
              saved: true,
              body: { name: "分析员" },
              savedResult: { id: "p-role", name: "分析员" },
            },
          ],
        }),
      },
    ],
  };
  const artifacts = collectArtifacts(session);
  assert.equal(artifacts.length, 2);
  assert.equal(artifacts.find((a) => a.kind === "role").draft.saved, true);
  assert.equal(
    artifacts.find((a) => a.kind === "sop").asset.status,
    "published",
  );
});
test("a corrected presentation replaces its preliminary stream rather than appending two decks", () => {
  const state = mergeEvents({}, [
    { data: { kind: "answer", text: "four pages" } },
  ]);
  const corrected = mergeEvents(state, [
    { data: { kind: "answerReset" } },
    { data: { kind: "answer", text: "three pages" } },
  ]);
  assert.equal(corrected.answer, "three pages");
});

test("a merge request submitted in the conversation becomes a sidebar artifact", () => {
  const mr =
    "https://git.example.com/example/agent-flow/merge_requests/631";
  const session = {
    messages: [
      {
        role: "assistant",
        content: JSON.stringify({
          message: `MR 已提交到 preview 分支：\n\n${mr}\n\n- 源分支: feature/x → preview`,
          drafts: [],
        }),
      },
      { role: "assistant", content: `同一个 MR 又提了一次：${mr}` },
      { role: "user", content: `看下这个 ${mr}` },
    ],
  };
  const artifacts = collectArtifacts(session);
  assert.equal(artifacts.length, 1);
  const [artifact] = artifacts;
  assert.equal(artifact.kind, "merge-request");
  assert.equal(artifact.name, "agent-flow !631");
  assert.equal(artifact.url, mr);
  assert.equal(artifact.external, true);
  assert.equal(artifact.source, "MR · example/agent-flow");
});

test("a GitHub pull request submitted in the conversation becomes a sidebar artifact", () => {
  const pr = "https://github.com/example/agent-flow/pull/12";
  const artifacts = collectArtifacts({
    messages: [
      { role: "assistant", content: `PR 已提交：${pr}` },
      { role: "assistant", content: `同一个 PR：${pr}` },
    ],
  });
  assert.equal(artifacts.length, 1);
  assert.equal(artifacts[0].kind, "merge-request");
  assert.equal(artifacts[0].name, "agent-flow #12");
  assert.equal(artifacts[0].url, pr);
  assert.equal(artifacts[0].source, "PR · example/agent-flow");
});

test("a /-/ merge request link names the repository, not the dash segment", () => {
  const dashed =
    "https://git.example.com/example/agent-flow/-/merge_requests/642";
  const plain =
    "https://git.example.com/example/agent-flow/merge_requests/642";
  const artifacts = collectArtifacts({
    messages: [
      { role: "assistant", content: `MR：${dashed}` },
      { role: "assistant", content: `同一个：${plain}` },
    ],
  });
  assert.equal(artifacts.length, 1);
  assert.equal(artifacts[0].name, "agent-flow !642");
  assert.equal(artifacts[0].url, dashed);
  assert.equal(artifacts[0].source, "MR · example/agent-flow");
});
