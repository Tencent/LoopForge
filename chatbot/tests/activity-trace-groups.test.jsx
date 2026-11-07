import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";
import ActivityTrace, { ReplyTimeline } from "../src/ActivityTrace.jsx";

const start = "2026-01-01T00:00:00Z";
const finish = "2026-01-01T00:01:05Z";
const tool = (id, detail, extra = {}) => ({
  id,
  kind: "tool",
  tool: "Bash",
  detail,
  startedAt: start,
  finishedAt: finish,
  ...extra,
});

describe("Codex-style activity trace", () => {
  test("the running round lists every step and folds once it finishes", async () => {
    const steps = [
      { id: "t1", kind: "text", text: "先查澄清面板的前端代码。" },
      tool("a", "grep -rn ModelPicker src"),
      tool("b", "npm test", { finishedAt: null }),
    ];
    const view = render(
      <ReplyTimeline steps={steps} startedAt={start} finishedAt={finish} live />,
    );
    // The progress note sits in the conversation, not inside a fold.
    expect(screen.getByText("先查澄清面板的前端代码。")).toBeVisible();
    // Like Codex, the running round has no title; its steps scroll in a box
    // that follows the newest one.
    expect(screen.queryByText("正在运行命令")).toBeNull();
    expect(screen.getByText("grep -rn ModelPicker src")).toBeInTheDocument();
    expect(screen.getByText("正在运行")).toBeInTheDocument();
    expect(
      view.container.querySelector('.execution-scroll[data-follow="true"]'),
    ).not.toBeNull();
    // A finished command that took a while says so in its row.
    expect(screen.getByText("已在1分钟5秒内运行")).toBeInTheDocument();

    const done = [
      ...steps.slice(0, 2),
      tool("b", "npm test", { failed: true }),
      { id: "t2", kind: "text", text: "结论" },
    ];
    view.rerender(
      <ReplyTimeline steps={done} startedAt={start} finishedAt={finish} />,
    );
    // A finished reply folds the whole process into its elapsed time.
    expect(screen.getByText(/已处理 /)).toBeInTheDocument();
    expect(screen.queryByText("先查澄清面板的前端代码。")).toBeNull();
    expect(screen.queryByText("grep -rn ModelPicker src")).toBeNull();

    // Opening it shows every note and step, each round expanded.
    const user = userEvent.setup();
    await user.click(screen.getByText(/已处理 /));
    expect(screen.getByText("先查澄清面板的前端代码。")).toBeInTheDocument();
    expect(screen.getByText("已运行命令")).toBeInTheDocument();
    expect(screen.getByText("grep -rn ModelPicker src")).toBeInTheDocument();
    expect(screen.getByText("npm test")).toBeInTheDocument();
    // An opened round scrolls inside a fixed height, without following.
    expect(
      view.container.querySelector(".execution-group-body .execution-scroll"),
    ).not.toBeNull();

    // Folding a round keeps its failures visible.
    await user.click(screen.getByText("已运行命令"));
    expect(screen.queryByText("grep -rn ModelPicker src")).toBeNull();
    expect(screen.getByText("1 步失败")).toBeInTheDocument();
    expect(screen.getByText("npm test")).toBeInTheDocument();
  });

  test("a new paragraph closes the previous round while still streaming", () => {
    const steps = [
      { id: "t1", kind: "text", text: "第一步" },
      tool("a", "pwd"),
      tool("b", "ls"),
      { id: "t2", kind: "text", text: "第二步" },
      tool("c", "cat a.js", { finishedAt: null }),
    ];
    render(<ReplyTimeline steps={steps} startedAt={start} live />);
    expect(screen.getByText("已运行命令")).toBeInTheDocument();
    expect(screen.queryByText("pwd")).toBeNull();
    // A round with one step is shown as that step.
    expect(screen.getByText("cat a.js")).toBeInTheDocument();
    expect(screen.getByText(/正在处理/)).toBeInTheDocument();
  });

  test("the elapsed time and its line head the reply, running or finished", () => {
    const steps = [
      { id: "t1", kind: "text", text: "第一步" },
      tool("a", "pwd", { finishedAt: null }),
    ];
    const order = (container) =>
      [...container.querySelectorAll(".execution-summary-row, .reply-divider, .execution-body")]
        .map((el) => el.className.split(" ")[0]);
    const view = render(<ReplyTimeline steps={steps} startedAt={start} live />);
    expect(order(view.container)).toEqual([
      "execution-summary-row",
      "reply-divider",
      "execution-body",
    ]);
    view.rerender(
      <ReplyTimeline
        steps={[...steps.slice(0, 1), tool("a", "pwd"), { id: "t2", kind: "text", text: "结论" }]}
        startedAt={start}
        finishedAt={finish}
      />,
    );
    // The line stays visible under the folded elapsed time.
    const divider = view.container.querySelector("summary .reply-divider");
    expect(divider).not.toBeNull();
    expect(divider.previousElementSibling.className).toBe("execution-summary-row");
  });

  test("history without progress notes opens as one round", async () => {
    const steps = [
      { id: "th", kind: "thinking", text: "分析", startedAt: start, finishedAt: finish },
      tool("a", "pwd"),
      { id: "r", kind: "tool", tool: "Read", detail: "/tmp/a.js", startedAt: start, finishedAt: finish },
    ];
    render(<ActivityTrace steps={steps} startedAt={start} finishedAt={finish} />);
    expect(screen.queryByText("已运行命令、读取文件")).toBeNull();
    await userEvent.setup().click(screen.getByText(/已处理 /));
    expect(screen.getByText("已运行命令、读取文件")).toBeInTheDocument();
    expect(screen.getByText("pwd")).toBeInTheDocument();
  });

  test("a round that only thought folds into one line while running", async () => {
    const steps = [
      { id: "th", kind: "thinking", text: "The user picked option A", startedAt: start, finishedAt: finish },
      { id: "t1", kind: "text", text: "好，按 A 做。" },
      tool("a", "pwd", { finishedAt: null }),
    ];
    render(<ReplyTimeline steps={steps} startedAt={start} live />);
    expect(screen.getByText("已思考")).toBeInTheDocument();
    expect(screen.queryByText("The user picked option A")).toBeNull();
    await userEvent.setup().click(screen.getByText("已思考"));
    expect(screen.getByText("The user picked option A")).toBeInTheDocument();
  });
});
