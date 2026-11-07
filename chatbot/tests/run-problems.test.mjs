import { test } from "vitest";
import assert from "node:assert/strict";
import { runProblems } from "../src/runs.js";
import { CREDENTIAL_GUIDES, credentialHint } from "../src/credentials.js";

const run = (status, nodes) => ({
  id: "run-1",
  status,
  workflowSnapshot: {
    name: "通用后端交付 SOP",
    nodes: [
      { id: "design", name: "方案设计", type: "agent" },
      { id: "mr", name: "提交 MR", type: "agent" },
      { id: "approve", name: "上线确认", type: "approval" },
    ],
  },
  nodes,
});

test("failed and waiting nodes surface with the reason in workflow order", () => {
  const problems = runProblems(
    run("failed", {
      approve: { status: "waiting_approval", attempt: 1 },
      mr: {
        status: "waiting_approval",
        attempt: 1,
        error: "agent requested human confirmation: 推送被拒绝",
        gateResult: {
          reason: "运行账号没有写权限",
          actionRequired: "加为 Developer",
        },
      },
      design: {
        status: "failed",
        attempt: 2,
        reworkCount: 0,
        error:
          "repository checkout: git clone https://git.example.com/a/b.git: exit status 128 (fatal: could not read Username for 'https://git.example.com': terminal prompts disabled)",
      },
    }),
  );
  assert.deepEqual(
    problems.map((p) => [p.nodeName, p.kind]),
    [
      ["方案设计", "failed"],
      ["提交 MR", "waiting_approval"],
      ["上线确认", "waiting_approval"],
    ],
  );
  assert.deepEqual(
    problems.map((p) => p.gate),
    [undefined, "agent", "approval"],
  );
  assert.equal(
    problems[1].reason,
    "运行账号没有写权限；需要处理：加为 Developer",
  );
  assert.equal(
    problems[2].reason,
    "SOP「通用后端交付 SOP」在这一步设置了人工门禁：「提交 MR」已完成，需要有权限的成员检查结果后确认，确认后运行结束。",
  );
  assert.deepEqual(problems[0].expected, {
    status: "failed",
    attempt: 2,
    reworkCount: 0,
  });
  const hint = credentialHint(problems[0].reason);
  assert.equal(hint?.field, "gitToken");
  assert.match(
    CREDENTIAL_GUIDES.gitToken.steps.join(""),
    /Contents 和 Pull requests/,
  );
});

test("finished runs and healthy nodes raise nothing", () => {
  assert.deepEqual(
    runProblems(run("canceled", { design: { status: "failed" } })),
    [],
  );
  assert.deepEqual(
    runProblems(run("running", { design: { status: "running" } })),
    [],
  );
  assert.deepEqual(runProblems(null), []);
});

test("engine credential messages map to the owner's setting", () => {
  for (const [text, field] of [
    [
      "运行责任人未配置 Git Token，无法拉取仓库。请责任人到「个人配置 → GitHub Git Token」配置",
      "gitToken",
    ],
    [
      "当前责任人未配置 CodeBuddy API Key：请先在个人配置中配置",
      "codeBuddyApiKey",
    ],
  ]) {
    const hint = credentialHint(text);
    assert.equal(hint?.field, field, text);
    assert.equal(hint.owner, true, text);
    assert.ok(CREDENTIAL_GUIDES[field].url, field);
  }
});

test("approval reason names upstream, downstream, rework and instructions", async () => {
  const { approvalWaitReason } = await import("../src/runs.js");
  const run = {
    workflowSnapshot: {
      name: "通用后端交付 SOP",
      nodes: [
        { id: "impl", name: "代码实现" },
        { id: "test", name: "测试" },
        {
          id: "gate",
          name: "上线确认",
          type: "approval",
          rejectTo: "impl",
          taskPrompt: "检查测试报告和 MR 改动范围",
        },
        { id: "release", name: "发布" },
      ],
      edges: [
        { source: "impl", target: "gate" },
        { source: "test", target: "gate" },
        { source: "gate", target: "release" },
      ],
    },
  };
  // Same expectation as TestApprovalWaitReason in internal/domain.
  assert.equal(
    approvalWaitReason(run, "gate"),
    "SOP「通用后端交付 SOP」在这一步设置了人工门禁：「代码实现」、「测试」已完成，需要有权限的成员检查结果后确认，确认后继续「发布」；不通过可打回到「代码实现」重做。确认要求：检查测试报告和 MR 改动范围",
  );
});
