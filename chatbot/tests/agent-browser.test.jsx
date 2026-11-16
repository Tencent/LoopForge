import React from "react";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import AgentBrowser from "../src/AgentBrowser.jsx";
import ArtifactPanel from "../src/ArtifactPanel.jsx";
import { RichContent } from "../src/RichContent.jsx";
import { AgentBrowserLinks, agentBrowserTarget, devServerAddress, devServerTarget } from "../src/agent-browser-links.js";

class FakeSocket {
  static OPEN = 1;
  static instances = [];
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.sent = [];
    FakeSocket.instances.push(this);
  }
  send(data) {
    this.sent.push(JSON.parse(data));
  }
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
  open() {
    this.readyState = FakeSocket.OPEN;
    this.onopen?.();
  }
  state(state) {
    this.onmessage?.({ data: JSON.stringify({ type: "state", state }) });
  }
  error(error) {
    this.onmessage?.({ data: JSON.stringify({ type: "error", error }) });
  }
}

const live = {
  available: true,
  kind: "docker",
  url: "file:///workspace/demo.html",
  canGoBack: true,
  tabs: [
    { id: "t1", title: "Demo", url: "file:///workspace/demo.html", active: true },
    { id: "t2", title: "Vite", url: "http://localhost:5173/" },
  ],
};
const lastSocket = () => FakeSocket.instances.at(-1);
let fetchMock;

beforeEach(() => {
  FakeSocket.instances = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  fetchMock = vi.fn(async () => ({ ok: true, status: 200, json: async () => live }));
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

describe("devServerTarget", () => {
  test("maps plain-HTTP execution machine addresses to a port and path", () => {
    expect(devServerTarget("http://localhost:5173/a/b?c=1#d")).toEqual({ port: 5173, path: "/a/b?c=1#d" });
    expect(devServerTarget("http://127.0.0.1:3000")).toEqual({ port: 3000, path: "/" });
    expect(devServerTarget("http://localhost/")).toEqual({ port: 80, path: "/" });
    for (const value of ["https://localhost:5173/", "http://example.com:5173/", "localhost:5173", "file:///", "file://host/a.html"])
      expect(devServerTarget(value)).toBeNull();
    expect(devServerAddress("run-1", 5173, { pathname: "/devpreview/run-1/5173/x", search: "?q", hash: "" })).toBe("http://localhost:5173/x?q");
    expect(devServerAddress("run-1", 5173, { pathname: "/x", search: "", hash: "" })).toBe("http://localhost:5173/x");
  });

  test("maps workspace files to the file channel", () => {
    expect(devServerTarget("file:///workspace/demo.html#top")).toEqual({ port: "file", path: "/workspace/demo.html#top" });
    expect(devServerTarget("file:///workspace/页面.html")).toEqual({ port: "file", path: "/workspace/%E9%A1%B5%E9%9D%A2.html" });
    expect(devServerAddress("run-1", "file", { pathname: "/devpreview/run-1/file/workspace/site/", search: "", hash: "" })).toBe(
      "file:///workspace/site/",
    );
  });
});

describe("agentBrowserTarget", () => {
  test("accepts only addresses on the execution machine", () => {
    expect(agentBrowserTarget("http://localhost:5173/sidebar-demo-2.html")).toBe("http://localhost:5173/sidebar-demo-2.html");
    expect(agentBrowserTarget("localhost:5173")).toBe("http://localhost:5173/");
    expect(agentBrowserTarget("http://127.0.0.1:8080/a?b=1")).toBe("http://127.0.0.1:8080/a?b=1");
    expect(agentBrowserTarget("http://[::1]:3000/")).toBe("http://[::1]:3000/");
    expect(agentBrowserTarget("/workspace/sidebar-demo-2.html")).toBe("file:///workspace/sidebar-demo-2.html");
    expect(agentBrowserTarget("file:///workspace/out/chart.svg")).toBe("file:///workspace/out/chart.svg");
    for (const value of [
      "https://example.com/a.html",
      "http://localhost.evil.com/",
      "/workspace/agentflow/main.go",
      "/workspace/...",
      "file:///etc/passwd",
      "file://host/workspace/a.html",
      "javascript:alert(1)",
      "go test ./...",
      "",
    ])
      expect(agentBrowserTarget(value)).toBe("");
  });
});

describe("AgentBrowser", () => {
  test("streams the workspace browser and opens requested links", async () => {
    const handled = vi.fn();
    const { rerender, container } = render(
      <AgentBrowser runId="run-1" request={{ id: "r1", url: "https://example.org/demo.html" }} onRequestHandled={handled} />,
    );
    const socket = lastSocket();
    expect(socket.url).toBe(`ws://${location.host}/api/runs/run-1/browser/stream`);
    // The socket is not open yet, so the first link starts the browser over HTTP.
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/runs/run-1/browser");
    expect(JSON.parse(init.body)).toMatchObject({ operation: "navigate", url: "https://example.org/demo.html", ensure: true });
    expect(handled).toHaveBeenCalledWith("r1");

    act(() => {
      socket.open();
      socket.state(live);
    });
    expect(screen.getByLabelText("网址")).toHaveValue("file:///workspace/demo.html");
    expect(screen.getByLabelText("后退")).toBeEnabled();
    expect(screen.getByLabelText("前进")).toBeDisabled();
    expect(screen.getByRole("tablist", { name: "浏览器标签页" })).toHaveTextContent("Vite");
    expect(screen.getByText("正在等待第一帧画面…")).toBeInTheDocument();

    const img = container.querySelector(".agent-browser-screen");
    act(() => socket.onmessage({ data: new Blob(["jpeg"]) }));
    expect(img).toHaveAttribute("src", "blob:test");
    act(() => fireEvent.load(img));
    expect(screen.queryByText("正在等待第一帧画面…")).not.toBeInTheDocument();
    expect(screen.getByText("沙箱浏览器 · 实时")).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText("刷新"));
    expect(socket.sent).toContainEqual({ operation: "reload" });
    fireEvent.click(screen.getByText("Vite"));
    expect(socket.sent).toContainEqual({ operation: "activate", targetId: "t2" });

    rerender(
      <AgentBrowser runId="run-1" request={{ id: "r2", url: "https://example.com/" }} onRequestHandled={handled} />,
    );
    expect(socket.sent).toContainEqual({ operation: "navigate", url: "https://example.com/" });
    expect(handled).toHaveBeenCalledWith("r2");
    rerender(
      <AgentBrowser runId="run-1" request={{ id: "r2", url: "https://example.com/" }} onRequestHandled={handled} />,
    );
    expect(socket.sent.filter((m) => m.operation === "navigate")).toHaveLength(1);

    const ime = container.querySelector(".agent-browser-ime");
    fireEvent.keyDown(ime, { key: "a", code: "KeyA", keyCode: 65 });
    expect(socket.sent.at(-1)).toMatchObject({ operation: "input", events: [{ type: "key", action: "down", key: "a", text: "a" }] });
    // Escape goes to the page; the side panel only closes on unhandled Escape.
    expect(fireEvent.keyDown(ime, { key: "Escape", code: "Escape", keyCode: 27 })).toBe(false);
  });

  test("opens dev server addresses natively and switches back to the screencast", async () => {
    const handled = vi.fn();
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    const { container } = render(
      <AgentBrowser runId="run-5" request={{ id: "n1", url: "http://localhost:5173/admin?tab=2" }} onRequestHandled={handled} />,
    );
    expect(handled).toHaveBeenCalledWith("n1");
    const frame = container.querySelector("iframe.agent-browser-frame");
    expect(frame).toHaveAttribute("src", "/devpreview/run-5/5173/admin?tab=2");
    expect(screen.getByLabelText("网址")).toHaveValue("http://localhost:5173/admin?tab=2");
    // No screencast while the page renders natively.
    expect(FakeSocket.instances).toHaveLength(0);
    fireEvent.click(screen.getByLabelText("在新窗口打开"));
    expect(open).toHaveBeenCalledWith(expect.stringContaining("/devpreview/run-5/5173/"), "_blank", "noopener");
    fireEvent.click(screen.getByLabelText("切换到 Agent 浏览器画面"));
    expect(container.querySelector("iframe.agent-browser-frame")).toBeNull();
    expect(lastSocket().url).toContain("/api/runs/run-5/browser/stream");
  });

  test("opens workspace files natively", async () => {
    const handled = vi.fn();
    const { container } = render(
      <AgentBrowser runId="run-6" request={{ id: "f1", url: "file:///workspace/demo.html" }} onRequestHandled={handled} />,
    );
    expect(handled).toHaveBeenCalledWith("f1");
    expect(container.querySelector("iframe.agent-browser-frame")).toHaveAttribute("src", "/devpreview/run-6/file/workspace/demo.html");
    expect(screen.getByLabelText("网址")).toHaveValue("file:///workspace/demo.html");
    expect(screen.getByText("原生预览 · 工作区文件")).toBeInTheDocument();
    expect(FakeSocket.instances).toHaveLength(0);
  });

  test("the panel in use takes the shared viewport back and never slides the frame", () => {
    const { container } = render(<AgentBrowser runId="run-5" />);
    const socket = lastSocket();
    const stage = container.querySelector(".agent-browser-stage");
    const img = container.querySelector(".agent-browser-screen");
    // A tall panel; another viewer last sized the browser to 1024×866.
    stage.getBoundingClientRect = () => ({ left: 0, top: 0, width: 600, height: 1200, right: 600, bottom: 1200, x: 0, y: 0 });
    img.getBoundingClientRect = () => ({ left: 0, top: 0, width: 600, height: 507, right: 600, bottom: 507, x: 0, y: 0 });
    act(() => {
      socket.open();
      socket.state({ ...live, width: 1024, height: 866 });
    });
    const resizes = () => socket.sent.filter((m) => m.operation === "resize");
    fireEvent.wheel(stage, { deltaY: 120 });
    expect(resizes()).toEqual([expect.objectContaining({ width: 1024, height: 2000 })]);
    expect(img.style.transform).toBe("");
    // Not again while the resize is on its way.
    fireEvent.wheel(stage, { deltaY: 120 });
    expect(resizes()).toHaveLength(1);
  });

  test("a panel whose size already fits leaves the viewport alone", () => {
    const { container } = render(<AgentBrowser runId="run-6" />);
    const socket = lastSocket();
    const stage = container.querySelector(".agent-browser-stage");
    stage.getBoundingClientRect = () => ({ left: 0, top: 0, width: 1024, height: 866, right: 1024, bottom: 866, x: 0, y: 0 });
    act(() => {
      socket.open();
      // Frames count the page's 15px scrollbar the resize left out.
      socket.state({ ...live, width: 1039, height: 866 });
    });
    fireEvent.wheel(stage, { deltaY: 120 });
    expect(socket.sent.filter((m) => m.operation === "resize")).toHaveLength(0);
  });

  test("project viewers watch the browser without driving it", async () => {
    const { container } = render(<AgentBrowser runId="run-2" />);
    const socket = lastSocket();
    act(() => {
      socket.open();
      socket.state(live);
      socket.error("无权操作该任务的浏览器");
    });
    expect(screen.getByText("你没有该项目的编辑权限，只能查看画面")).toBeInTheDocument();
    expect(screen.getByLabelText("刷新")).toBeDisabled();
    const before = socket.sent.length;
    fireEvent.keyDown(container.querySelector(".agent-browser-ime"), { key: "a", code: "KeyA", keyCode: 65 });
    expect(socket.sent).toHaveLength(before);
  });

  test("offers to start a browser that is not running", async () => {
    const user = userEvent.setup();
    render(<AgentBrowser runId="run-3" />);
    const socket = lastSocket();
    act(() => {
      socket.open();
      socket.state({ available: false, reason: "Agent 浏览器尚未启动" });
    });
    await user.click(screen.getByRole("button", { name: "启动浏览器" }));
    expect(socket.sent).toContainEqual({ operation: "start", ensure: true });
    expect(screen.getByText("正在启动 Agent 浏览器…")).toBeInTheDocument();
  });

  test("stops streaming once unmounted", () => {
    const { unmount } = render(<AgentBrowser runId="run-4" />);
    const socket = lastSocket();
    act(() => socket.open());
    unmount();
    expect(socket.readyState).toBe(3);
    expect(FakeSocket.instances).toHaveLength(1);
  });
});

describe("workspace links in replies", () => {
  const reply = [
    "预览：`http://localhost:5173/sidebar-demo-2.html`，文件在 `/workspace/sidebar-demo-2.html`。",
    "也可以 [打开 demo](file:///workspace/demo.html) 或直接 file:///workspace/plain.html。",
    "源码 `/workspace/agentflow/main.go`，文档 [官网](https://example.com/docs)。",
  ].join("\n\n");

  test("open in the Agent browser when the conversation has a workspace", async () => {
    const user = userEvent.setup();
    const open = vi.fn();
    render(
      <AgentBrowserLinks.Provider value={open}>
        <RichContent message={reply} />
      </AgentBrowserLinks.Provider>,
    );
    const links = screen.getAllByTitle(/^在 Agent 浏览器中打开/);
    expect(links.map((b) => b.title.replace("在 Agent 浏览器中打开 ", ""))).toEqual([
      "http://localhost:5173/sidebar-demo-2.html",
      "file:///workspace/sidebar-demo-2.html",
      "file:///workspace/demo.html",
      "file:///workspace/plain.html",
    ]);
    await user.click(links[0]);
    expect(open).toHaveBeenCalledWith("http://localhost:5173/sidebar-demo-2.html");
    await user.click(screen.getByText("打开 demo"));
    expect(open).toHaveBeenLastCalledWith("file:///workspace/demo.html");
    expect(screen.getByText("/workspace/agentflow/main.go").closest("button")).toBeNull();
    expect(screen.getByRole("link", { name: "官网" })).toHaveAttribute("href", "https://example.com/docs");
  });

  // Models often put the address in a fenced block of its own; its trailing
  // newline must not push the link icon onto a line of its own.
  test("open an address written as a fenced block", async () => {
    const user = userEvent.setup();
    const open = vi.fn();
    render(
      <AgentBrowserLinks.Provider value={open}>
        <RichContent message={"文件地址：\n\n```\nfile:///workspace/repo/demo.html\n```\n"} />
      </AgentBrowserLinks.Provider>,
    );
    const link = screen.getByTitle("在 Agent 浏览器中打开 file:///workspace/repo/demo.html");
    expect(link.querySelector("code").textContent).toBe("file:///workspace/repo/demo.html");
    await user.click(link);
    expect(open).toHaveBeenCalledWith("file:///workspace/repo/demo.html");
  });

  test("stay plain without a workspace and never render file links", () => {
    render(<RichContent message={reply} />);
    expect(screen.queryAllByTitle(/^在 Agent 浏览器中打开/)).toHaveLength(0);
    expect(screen.getByText("打开 demo").closest("a")).toBeNull();
    expect(document.querySelector('a[href^="file:"]')).toBeNull();
    expect(screen.getByText("http://localhost:5173/sidebar-demo-2.html").tagName).toBe("CODE");
  });
});

describe("side panel browser tab", () => {
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
    expect(screen.getByRole("tab", { name: /浏览器/ })).toBeDisabled();
    expect(FakeSocket.instances).toHaveLength(0);
  });

  test("a compact panel goes back to the chat after adding an element", () => {
    // A narrow window: the panel covers the conversation.
    const measure = vi
      .spyOn(HTMLElement.prototype, "getBoundingClientRect")
      .mockReturnValue({ left: 0, top: 0, width: 600, height: 800, right: 600, bottom: 800, x: 0, y: 0 });
    const onAddElement = vi.fn();
    const onClose = vi.fn();
    const props = {
      view: "browser",
      workspaceRunId: "run-9",
      browserRequest: { id: "b1", url: "http://localhost:5173/" },
      onBrowserRequestHandled: vi.fn(),
      onAddElement,
      onClose,
    };
    try {
      const { container } = render(panel(props));
      expect(screen.getByRole("button", { name: "返回聊天" })).toBeInTheDocument();
      const frame = container.querySelector("iframe.agent-browser-frame");
      const doc = frame.contentDocument;
      doc.open();
      doc.write(`<!doctype html><html><body><button>提交</button></body></html>`);
      doc.close();
      act(() => {
        fireEvent.load(frame);
      });
      fireEvent.click(screen.getByRole("button", { name: "标记元素" }));
      act(() => {
        doc.querySelector("button").dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      });
      fireEvent.click(screen.getByText("加入对话框"));
      expect(onAddElement).toHaveBeenCalledWith(expect.objectContaining({ type: "element" }));
      expect(onClose).toHaveBeenCalled();
    } finally {
      measure.mockRestore();
    }
  });

  test("shows the workspace browser", async () => {
    const onViewChange = vi.fn();
    const { rerender } = render(panel({ view: "artifacts", workspaceRunId: "run-9", onViewChange }));
    fireEvent.click(screen.getByRole("tab", { name: /浏览器/ }));
    expect(onViewChange).toHaveBeenCalledWith("browser");
    rerender(panel({ view: "browser", workspaceRunId: "run-9", onViewChange }));
    expect(screen.getByRole("tab", { name: /浏览器/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("complementary", { name: "Agent 浏览器" })).toBeInTheDocument();
    expect(lastSocket().url).toContain("/api/runs/run-9/browser/stream");
    rerender(panel({ view: "artifacts", workspaceRunId: "run-9", onViewChange }));
    expect(lastSocket().readyState).toBe(3);
  });
});
