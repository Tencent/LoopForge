import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import ChangesPanel from "../src/ChangesPanel.jsx";
import { diffTotals, mergeChanges, parseUnifiedDiff } from "../src/changes.js";

const PATCH = [
  "diff --git a/web/app/approvals.js b/web/app/approvals.js",
  "--- a/web/app/approvals.js",
  "+++ b/web/app/approvals.js",
  "@@ -10,4 +10,5 @@ render()",
  " const state = load();",
  "-render(state);",
  "+render(state, { batch: true });",
  "+track(state);",
  " return state;",
].join("\n");

function readyChanges(overrides = {}) {
  const files = mergeChanges(
    [
      { path: "web/app/approvals.js", index: " ", worktree: "M" },
      { path: "web/app/new.js", index: "?", worktree: "?" },
    ],
    parseUnifiedDiff(PATCH),
  );
  return {
    status: "ready",
    files,
    repositories: [],
    additions: diffTotals(files).additions,
    deletions: diffTotals(files).deletions,
    truncated: false,
    warning: "",
    reason: "",
    refresh: vi.fn(),
    loadFile: vi.fn(async () => ({ binary: false, hunks: [], additions: 0, deletions: 0 })),
    ...overrides,
  };
}

test("lists changed files with status and line counts", () => {
  const changes = readyChanges();
  render(<ChangesPanel changes={changes} />);
  expect(screen.getByText("approvals.js")).toBeInTheDocument();
  expect(screen.getAllByText("web/app")).toHaveLength(2);
  expect(screen.getByText("个文件")).toBeInTheDocument();
  expect(screen.getAllByText("+2").length).toBeGreaterThan(0);
  expect(screen.getAllByText("−1").length).toBeGreaterThan(0);
  expect(screen.getByText("未跟踪")).toBeInTheDocument();
});

test("opens a file diff and returns to the list", () => {
  const changes = readyChanges();
  render(<ChangesPanel changes={changes} />);
  fireEvent.click(screen.getByText("approvals.js"));
  expect(screen.getByText("返回改动列表")).toBeInTheDocument();
  expect(screen.getByText("@@ -10,4 +10,5 @@ render()")).toBeInTheDocument();
  expect(screen.getByText("render(state, { batch: true });")).toBeInTheDocument();
  expect(screen.getByText("render(state);")).toBeInTheDocument();
  fireEvent.click(screen.getByText("返回改动列表"));
  expect(screen.getByText("new.js")).toBeInTheDocument();
});

test("loads untracked file content on demand", async () => {
  const changes = readyChanges({
    loadFile: vi.fn(async () => ({
      binary: false,
      additions: 1,
      deletions: 0,
      hunks: [
        {
          header: "@@ -0,0 +1,1 @@",
          section: "",
          lines: [{ kind: "add", text: "export const ready = true;", oldNo: 0, newNo: 1 }],
        },
      ],
    })),
  });
  render(<ChangesPanel changes={changes} />);
  fireEvent.click(screen.getByText("new.js"));
  expect(await screen.findByText("该文件尚未纳入 git，下面是它的完整内容。")).toBeInTheDocument();
  expect(changes.loadFile).toHaveBeenCalledTimes(1);
});

test("shows the empty state and the workspace reason", () => {
  const { rerender } = render(
    <ChangesPanel changes={readyChanges({ files: [], additions: 0, deletions: 0 })} />,
  );
  expect(screen.getByText("工作区没有未提交改动。")).toBeInTheDocument();
  expect(screen.getByText("已提交或已推送的改动不计入这里。")).toBeInTheDocument();
  rerender(
    <ChangesPanel
      changes={readyChanges({
        status: "unavailable",
        files: [],
        additions: 0,
        deletions: 0,
        reason: "工作区已回收；有保存的快照时可按需恢复",
      })}
    />,
  );
  expect(
    screen.getByText("工作区已回收；有保存的快照时可按需恢复"),
  ).toBeInTheDocument();
});

// Committed work never reaches the panel, so the empty state names the branch
// and links to the code host instead of leaving 「改动 0」looking like a broken panel.
test("empty state points at the work branch on the code host", () => {
  render(
    <ChangesPanel
      changes={readyChanges({
        files: [],
        additions: 0,
        deletions: 0,
        repositories: [
          {
            name: "agent-flow",
            cloneUrl: "git@github.com:example/agent-flow.git",
            baseBranch: "main",
            workBranch: "feature/fix-20261102-1912",
          },
        ],
      })}
    />,
  );
  expect(screen.getByText("feature/fix-20261102-1912")).toBeInTheDocument();
  expect(screen.getByText("main")).toBeInTheDocument();
  const link = screen.getByRole("link", { name: /在代码仓库查看这个分支的改动/ });
  expect(link).toHaveAttribute(
    "href",
    "https://github.com/example/agent-flow/compare/main...feature/fix-20261102-1912",
  );
});

test("git failures surface above the empty state", () => {
  render(
    <ChangesPanel
      changes={readyChanges({
        files: [],
        additions: 0,
        deletions: 0,
        warning: "Git 状态读取失败",
      })}
    />,
  );
  expect(screen.getByRole("alert")).toHaveTextContent("Git 状态读取失败");
});

// A reclaimed workspace is the usual state once the work is pushed, so the
// branch entry stays available there too.
test("a reclaimed workspace still points at the work branch on the code host", () => {
  render(
    <ChangesPanel
      changes={readyChanges({
        status: "unavailable",
        files: [],
        reason: "工作区已回收；有保存的快照时可按需恢复",
        repositories: [
          {
            name: "agent-flow",
            cloneUrl: "https://github.com/example/agent-flow.git",
            baseBranch: "main",
            workBranch: "feature/fix-1",
          },
        ],
      })}
    />,
  );
  expect(screen.getByText("工作区已回收；有保存的快照时可按需恢复")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /在代码仓库查看这个分支的改动/ })).toHaveAttribute(
    "href",
    "https://github.com/example/agent-flow/compare/main...feature/fix-1",
  );
});
