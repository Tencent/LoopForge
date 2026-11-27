import { test } from "vitest";
import assert from "node:assert/strict";
import { branchCompareUrl, codeupRepoPath, githubRepoPath, gongfengProjectPath } from "../src/changes.js";
import { isConversationWorkspaceRun } from "../src/runs.js";

// 自由协作 runs and chat workspaces are not LOOPs; listing them showed
// "等待审批 · 0/0 节点", including for a conversation that is such a run.
test("conversation workspaces are not listed as runs", () => {
  assert.equal(isConversationWorkspaceRun({ id: "run-1", workflowId: "free-collaboration" }), true);
  assert.equal(isConversationWorkspaceRun({ id: "run-2", workflowId: "conversation-workspace" }), true);
  assert.equal(isConversationWorkspaceRun({ id: "run-3", kind: "workspace" }), true);
});

test("LOOP runs stay listed", () => {
  assert.equal(isConversationWorkspaceRun({ id: "run-3", workflowId: "generic-backend-delivery" }), false);
  assert.equal(isConversationWorkspaceRun(null), false);
});

test("branchCompareUrl points at the work branch on GitHub", () => {
  const repository = {
    cloneUrl: "https://github.com/example/agent-flow.git",
    baseBranch: "main",
    workBranch: "feature/fix-1",
  };
  assert.equal(
    branchCompareUrl(repository),
    "https://github.com/example/agent-flow/compare/main...feature/fix-1",
  );
  assert.equal(
    branchCompareUrl({ ...repository, cloneUrl: "git@github.com:example/agent-flow.git", baseBranch: "" }),
    "https://github.com/example/agent-flow/tree/feature/fix-1",
  );
  assert.equal(githubRepoPath("https://github.com/o/r"), "o/r");
  assert.equal(githubRepoPath("https://github.com/o/r/tree/main"), "");
  assert.equal(githubRepoPath("https://gitlab.com/o/r.git"), "");
});

test("branchCompareUrl points at the work branch on Codeup", () => {
  const repository = {
    cloneUrl: "https://codeup.aliyun.com/org123/ai/loopforge.git",
    baseBranch: "main",
    workBranch: "feature/fix-1",
  };
  // Codeup links to the work branch tree; the compare page URL is not stable.
  assert.equal(
    branchCompareUrl(repository),
    "https://codeup.aliyun.com/org123/ai/loopforge/tree/feature/fix-1",
  );
  assert.equal(
    branchCompareUrl({ ...repository, cloneUrl: "git@codeup.aliyun.com:org123/ai/loopforge.git", baseBranch: "" }),
    "https://codeup.aliyun.com/org123/ai/loopforge/tree/feature/fix-1",
  );
  assert.equal(codeupRepoPath("https://codeup.aliyun.com/org123/ai/loopforge"), "org123/ai/loopforge");
  assert.equal(codeupRepoPath("git@codeup.aliyun.com:org123/ai/loopforge.git"), "org123/ai/loopforge");
  assert.equal(codeupRepoPath("https://codeup.aliyun.com/org123"), "");
  assert.equal(codeupRepoPath("https://github.com/o/r.git"), "");
});

test("branchCompareUrl points at the work branch on a legacy host", () => {
  const repository = {
    cloneUrl: "git@git.example.com:example/agent-flow.git",
    baseBranch: "master",
    workBranch: "feature/fix-20261102-1912",
  };
  assert.equal(
    branchCompareUrl(repository),
    "https://git.example.com/example/agent-flow/-/compare/master...feature/fix-20261102-1912",
  );
  assert.equal(
    branchCompareUrl({ ...repository, cloneUrl: repository.cloneUrl.replace("git@git.example.com:", "https://git.example.com/") }),
    "https://git.example.com/example/agent-flow/-/compare/master...feature/fix-20261102-1912",
  );
});

test("branchCompareUrl falls back to the branch tree", () => {
  assert.equal(
    branchCompareUrl({
      cloneUrl: "https://git.example.com/example/agent-flow",
      workBranch: "feature/fix-1",
    }),
    "https://git.example.com/example/agent-flow/-/tree/feature/fix-1",
  );
  assert.equal(
    branchCompareUrl({
      cloneUrl: "https://git.example.com/example/agent-flow",
      baseBranch: "master",
      workBranch: "master",
    }),
    "https://git.example.com/example/agent-flow/-/tree/master",
  );
});

test("branchCompareUrl ignores unknown remotes and missing branches", () => {
  assert.equal(branchCompareUrl(null), "");
  assert.equal(branchCompareUrl({ workBranch: "feature/x" }), "");
  assert.equal(
    branchCompareUrl({ cloneUrl: "git@gitlab.com:o/r.git", workBranch: "feature/x" }),
    "",
  );
  assert.equal(
    branchCompareUrl({
      cloneUrl: "https://git.example.com/example/agent-flow",
      baseBranch: "master",
    }),
    "",
  );
  assert.equal(gongfengProjectPath("git@git.example.com:example/agent-flow.git"), "example/agent-flow");
});
