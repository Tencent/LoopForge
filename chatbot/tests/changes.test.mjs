import { test } from "vitest";
import assert from "node:assert/strict";
import {
  addedFileDiff,
  changeState,
  diffTotals,
  findPatch,
  mergeChanges,
  parseUnifiedDiff,
  pathParts,
  statusLabel,
} from "../src/changes.js";

const PATCH = [
  "diff --git a/web/app/approvals.js b/web/app/approvals.js",
  "index 1111111..2222222 100644",
  "--- a/web/app/approvals.js",
  "+++ b/web/app/approvals.js",
  "@@ -10,6 +10,8 @@ function render() {",
  " const state = load();",
  "-render(state);",
  "+render(state, { batch: true });",
  "+track(state);",
  " return state;",
  "@@ -40,3 +42,3 @@ function track() {",
  "-  log(state);",
  "+  log(state, 'batch');",
  " }",
  "diff --git a/web/app/new.js b/web/app/new.js",
  "new file mode 100644",
  "--- /dev/null",
  "+++ b/web/app/new.js",
  "@@ -0,0 +1,2 @@",
  "+export const ready = true;",
  "+export const count = 2;",
].join("\n");

test("parseUnifiedDiff splits files, hunks and line numbers", () => {
  const files = parseUnifiedDiff(PATCH);
  assert.equal(files.length, 2);
  const [changed, created] = files;
  assert.equal(changed.path, "web/app/approvals.js");
  assert.equal(changed.status, "M");
  assert.equal(changed.hunks.length, 2);
  assert.equal(changed.additions, 3);
  assert.equal(changed.deletions, 2);
  assert.deepEqual(
    changed.hunks[0].lines.map((line) => [line.kind, line.oldNo, line.newNo]),
    [
      ["ctx", 10, 10],
      ["del", 11, 0],
      ["add", 0, 11],
      ["add", 0, 12],
      ["ctx", 12, 13],
    ],
  );
  assert.equal(changed.hunks[0].section, "function render() {");
  assert.equal(created.status, "A");
  assert.equal(created.additions, 2);
  assert.equal(created.deletions, 0);
});

test("parseUnifiedDiff marks deleted and binary files", () => {
  const files = parseUnifiedDiff(
    [
      "diff --git a/logo.png b/logo.png",
      "Binary files a/logo.png and b/logo.png differ",
      "diff --git a/old.md b/old.md",
      "deleted file mode 100644",
      "--- a/old.md",
      "+++ /dev/null",
      "@@ -1,2 +0,0 @@",
      "-a",
      "-b",
    ].join("\n"),
  );
  assert.equal(files[0].binary, true);
  assert.equal(files[1].status, "D");
  assert.equal(files[1].deletions, 2);
});

test("parseUnifiedDiff tolerates truncated text", () => {
  const files = parseUnifiedDiff(
    "diff --git a/a.js b/a.js\n@@ -1,2 +1,2 @@\n-old\n+new",
  );
  assert.equal(files.length, 1);
  assert.equal(files[0].additions, 1);
  assert.equal(files[0].deletions, 1);
});

test("changeState reads porcelain index and worktree columns", () => {
  assert.equal(changeState({ index: "?", worktree: "?" }).code, "A");
  assert.equal(changeState({ index: " ", worktree: "M" }).code, "M");
  assert.equal(changeState({ index: "A", worktree: " " }).code, "A");
  assert.equal(changeState({ index: " ", worktree: "D" }).code, "D");
  assert.equal(changeState({ index: "R", worktree: " " }).code, "R");
  assert.equal(changeState({ index: "U", worktree: "U" }).code, "U");
  assert.equal(statusLabel("A"), "新增");
  assert.equal(statusLabel("Z"), "修改");
});

test("pathParts splits the directory from the file name", () => {
  assert.deepEqual(pathParts("web/app/approvals.js"), {
    dir: "web/app",
    name: "approvals.js",
  });
  assert.deepEqual(pathParts("README.md"), { dir: "", name: "README.md" });
});

test("mergeChanges keeps untracked files and attaches patch stats", () => {
  const patch = parseUnifiedDiff(PATCH);
  const files = mergeChanges(
    [
      { path: "web/app/approvals.js", index: " ", worktree: "M" },
      { path: "web/app/new.js", index: "?", worktree: "?" },
      { path: "docs/notes.md", index: "?", worktree: "?" },
    ],
    patch,
  );
  assert.deepEqual(
    files.map((file) => [file.path, file.status, file.additions, file.deletions]),
    [
      ["docs/notes.md", "A", 0, 0],
      ["web/app/approvals.js", "M", 3, 2],
      ["web/app/new.js", "A", 2, 0],
    ],
  );
  assert.equal(files[0].untracked, true, "a file with no patch stays untracked");
  assert.equal(files[2].untracked, false);
  assert.deepEqual(diffTotals(files), { additions: 5, deletions: 2 });
});

test("findPatch matches repository-prefixed workspace paths", () => {
  const patch = parseUnifiedDiff(PATCH);
  assert.equal(findPatch(patch, "repo-a/web/app/approvals.js")?.path, "web/app/approvals.js");
  assert.equal(findPatch(patch, "web/app/new.js")?.status, "A");
  assert.equal(findPatch(patch, "missing.js"), null);
});

test("addedFileDiff renders untracked content as additions", () => {
  const patch = addedFileDiff("notes.md", "a\nb\n");
  assert.equal(patch.status, "A");
  assert.equal(patch.additions, 2);
  assert.deepEqual(
    patch.hunks[0].lines.map((line) => [line.kind, line.newNo, line.text]),
    [
      ["add", 1, "a"],
      ["add", 2, "b"],
    ],
  );
  assert.equal(addedFileDiff("empty.md", "").hunks.length, 0);
});
