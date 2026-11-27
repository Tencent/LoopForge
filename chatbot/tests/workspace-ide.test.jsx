import React from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import ArtifactPanel from "../src/ArtifactPanel.jsx";
import WorkspaceIDE, { workspaceIDEURL } from "../src/WorkspaceIDE.jsx";

afterEach(() => vi.unstubAllGlobals());

describe("side panel IDE tab", () => {
  const chat = { session: { id: "s1", projectId: "p1" }, projectId: "p1", projects: [] };
  const panel = (props) => (
    <div className="chat-shell">
      <header className="conversation-header" />
      <main className="conversation-main" />
      <ArtifactPanel artifacts={[]} runs={[]} selected={null} setSelected={vi.fn()} chat={chat} onClose={vi.fn()} open {...props} />
    </div>
  );

  test("needs a conversation workspace", () => {
    render(panel({ view: "artifacts" }));
    expect(screen.getByRole("tab", { name: /IDE/ })).toBeDisabled();
  });

  test("offers both IDEs in the panel before loading the web editor", () => {
    const onViewChange = vi.fn();
    const { rerender } = render(panel({ view: "artifacts", workspaceRunId: "run-9", onViewChange }));
    fireEvent.click(screen.getByRole("tab", { name: /IDE/ }));
    expect(onViewChange).toHaveBeenCalledWith("ide");
    const props = { view: "ide", workspaceRunId: "run-9", onViewChange, workspaceChanges: { desktopIDE: { available: true } } };
    rerender(panel(props));
    const choices = within(screen.getByRole("tabpanel", { name: "IDE" }));
    const desktop = choices.getByRole("link", { name: "本地 Visual Studio Code" });
    const origin = Buffer.from(window.location.origin).toString("hex");
    expect(desktop).toHaveAttribute("href", `vscode://vscode-remote/ssh-remote+loopforge-${origin}-72756e2d39/workspace`);
    expect(screen.queryByLabelText("VS Code")).not.toBeInTheDocument();

    fireEvent.click(choices.getByRole("button", { name: "在线 IDE" }));
    const frame = screen.getByLabelText("VS Code");
    expect(frame).toHaveAttribute("src", "/vscode.html?run=run-9&embed=1&source=conversation");
    // A title would show a hover tip over the whole editor.
    expect(frame).not.toHaveAttribute("title");
    expect(choices.queryByRole("link", { name: "本地 Visual Studio Code" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "IDE" }));
    expect(frame).not.toBeVisible();
    fireEvent.click(choices.getByRole("button", { name: "在线 IDE" }));
    expect(screen.getByLabelText("VS Code")).toBe(frame);
    expect(frame).toBeVisible();

    rerender(panel({ ...props, workspaceRunId: "run-10" }));
    expect(frame).not.toBeInTheDocument();
    fireEvent.click(choices.getByRole("button", { name: "在线 IDE" }));
    expect(screen.getByLabelText("VS Code")).toHaveAttribute("src", "/vscode.html?run=run-10&embed=1&source=conversation");
  });

  test("explains why desktop connection is unavailable and keeps the online IDE accessible", () => {
    render(panel({
      view: "ide",
      workspaceRunId: "run-9",
      workspaceChanges: { desktopIDE: { available: false, reason: "桌面连接需要工作区编辑权限" } },
    }));
    const desktop = screen.getByRole("link", { name: "本地 Visual Studio Code" });
    expect(desktop).toHaveAttribute("aria-disabled", "true");
    expect(desktop).not.toHaveAttribute("href");
    expect(desktop).toHaveAccessibleDescription("桌面连接需要工作区编辑权限");
    expect(screen.getByRole("button", { name: "在线 IDE" })).toBeEnabled();
  });
});

describe("WorkspaceIDE", () => {
  const post = (data, origin = window.location.origin) =>
    window.dispatchEvent(new MessageEvent("message", { data, origin }));

  test("handles popout and maximize only for its own run", () => {
    const open = vi.fn(() => ({ focus() {} }));
    vi.stubGlobal("open", open);
    const onMaximize = vi.fn();
    render(<WorkspaceIDE runId="run-9" onMaximize={onMaximize} />);
    post({ type: "agentflow-vscode", runId: "other", action: "popout" });
    post({ type: "agentflow-vscode", runId: "run-9", action: "popout" }, "https://evil.example");
    expect(open).not.toHaveBeenCalled();
    post({ type: "agentflow-vscode", runId: "run-9", action: "popout" });
    expect(open).toHaveBeenCalledWith(workspaceIDEURL("run-9"), "agentflow-vscode-run-9", expect.any(String));
    post({ type: "agentflow-vscode", runId: "run-9", action: "maximize" });
    expect(onMaximize).toHaveBeenCalledTimes(1);
  });
});
