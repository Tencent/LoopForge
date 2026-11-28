import React from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test, vi } from "vitest";

const { apiMock, writeMock } = vi.hoisted(() => ({
  apiMock: vi.fn(async (path) => {
    if (path.includes("/telemetry")) return { events: [], nextAfter: 0, hasMore: false };
    if (path.startsWith("/api/workflows?"))
      return [{ id: "wf1", name: "流程一", projectId: "p1", nodes: [] }];
    if (path.startsWith("/api/runs?"))
      return [{
        id: "r1", status: "succeeded", workflowId: "wf1", input: "完成任务",
        workflowSnapshot: { name: "交付流程", nodes: [{ id: "n1", name: "实现" }] },
        nodes: { n1: { status: "succeeded", output: "完成" } },
        artifacts: [{ id: "out", name: "out.md", mediaType: "text/markdown" }],
      }];
    if (path.includes("/references"))
      return { items: [{ id: "ISSUE-1", name: "需求一" }], nextOffset: 0 };
    if (path.startsWith("/api/issues/"))
      return { id: "ISSUE-1", title: "需求一" };
    if (path.startsWith("/api/runs/"))
      return {
        id: "r1", status: "succeeded", input: "完成任务",
        workflowSnapshot: { name: "交付流程", nodes: [{ id: "n1", name: "实现" }] },
        nodes: { n1: { status: "succeeded", output: "完成" } },
        artifacts: [{ id: "out", name: "out.md", mediaType: "text/markdown" }],
      };
    return [];
  }),
  writeMock: vi.fn(async (_path, body) => ({ id: "created", ...body })),
}));
vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  api: apiMock,
  write: writeMock,
}));
vi.mock("mermaid", () => ({
  default: {
    initialize: vi.fn(),
    render: vi.fn(async () => ({ svg: "<svg><text>diagram</text></svg>" })),
  },
}));
vi.mock("../src/presentation.js", async (original) => ({
  ...(await original()),
  exportPresentation: vi.fn(async () => {}),
}));

import ArtifactPanel from "../src/ArtifactPanel.jsx";
import IssueContext from "../src/IssueContext.jsx";
import {
  ArtifactPreview,
  Attachment,
  CodeBlock,
  CopyButton,
  Diagram,
  ImageLightbox,
  RichContent,
  Slides,
  ZoomableImage,
  safeSVG,
} from "../src/RichContent.jsx";
import RunAlerts from "../src/RunAlerts.jsx";
import SessionContextMenu from "../src/SessionContextMenu.jsx";
import SidePanel, { DraftCard } from "../src/SidePanel.jsx";
import SopRunPanel from "../src/SopRunPanel.jsx";
import {
  CredentialAction,
  Dialog,
  IconButton,
  Menu,
  SelectMenu,
} from "../src/ui.jsx";
import { finalReply, mergeActivity, savedActivity } from "../src/activity.js";

const chat = {
  user: { id: 1 },
  projects: [{ id: "p1", name: "项目一" }],
  projectId: "p1",
  issueId: "",
  session: { id: "s1", projectId: "p1", messages: [] },
  busy: false,
  readOnly: false,
  linkIssue: vi.fn(async () => true),
  applyDraft: vi.fn(async () => ({})),
};
const run = {
  id: "r1",
  status: "failed",
  workflowSnapshot: {
    name: "交付流程",
    nodes: [{ id: "n1", name: "实现", type: "agent" }],
  },
  nodes: {
    n1: {
      nodeId: "n1",
      status: "failed",
      error: "未配置 CodeBuddy API Key",
      output: "失败",
    },
  },
  artifacts: [
    { id: "a1", nodeId: "n1", path: "result.md", mediaType: "text/markdown", size: 12, attempt: 1 },
  ],
};

describe("content and panels", () => {
  const indentedCode = "    first()\n    second()";

  test.each([
    ["one delta", [indentedCode]],
    ["split indentation", ["    ", "first()\n    second()"]],
  ])("renders a code-only reply from %s", (_, deltas) => {
    const steps = mergeActivity(
      [],
      deltas.map((text, sequence) => ({
        data: { kind: "answer", text },
        sequence,
      })),
    );
    expect(finalReply("", steps)).toBe(indentedCode);
    const answer = finalReply(indentedCode, savedActivity({ activity: steps }));
    expect(answer).toBe(indentedCode);
    const { container } = render(<RichContent message={answer} />);
    expect(container.querySelector("pre code")?.textContent).toBe(
      "first()\nsecond()",
    );
    expect(screen.getByRole("button", { name: "复制代码" })).toBeInTheDocument();
  });

  test("renders recovered indented code as the final answer after tool activity", () => {
    const intro = "Minimal example:";
    const code = indentedCode;
    const full = `${intro}\n\n${code}`;
    const steps = [
      { kind: "text", text: intro },
      { kind: "tool", id: "t1" },
      { kind: "text", text: code },
    ];
    const answer = finalReply(full, steps);
    expect(answer).toBe(code);
    const trimmedSteps = [...steps.slice(0, -1), { kind: "text", text: code.trimStart() }];
    expect(finalReply(full, trimmedSteps)).toBe(code);
    render(<RichContent message={answer} />);
    expect(screen.getByRole("button", { name: "复制代码" })).toBeInTheDocument();
  });

  test("renders rich content formats and preview controls", async () => {
    const user = userEvent.setup();
    expect(safeSVG("<svg><script>x</script><path /></svg>")).not.toContain("script");
    render(
      <>
        <RichContent message={"# 标题\n\n[链接](https://example.com)\n\n```js\nconst x = 1\n```"} />
        <CopyButton value="copy me" />
        <Diagram source="graph TD; A-->B" />
        <Slides source={"# 演示\n---\n## 第二页"} />
        <CodeBlock language="html" source="<h1>预览</h1>" />
        <Attachment attachment={{ id: "f1", name: "file.txt", size: 20 }} sessionId="s1" />
        <ZoomableImage src="/image.png" alt="示例图" />
        <ImageLightbox src="/large.png" alt="大图" onClose={vi.fn()} />
        <ArtifactPreview artifact={{ id: "x", name: "note.md", kind: "code", content: "hello", source: "测试" }} />
        <ArtifactPreview artifact={{ id: "svg", name: "shape.svg", kind: "svg", content: "<svg><circle /></svg>", source: "测试" }} />
        <ArtifactPreview artifact={{ id: "mmd", name: "flow.mmd", kind: "mermaid", content: "graph TD;A-->B", source: "测试" }} />
        <ArtifactPreview artifact={{ id: "html", name: "page.html", kind: "html", content: "<h1>网页</h1>", source: "测试" }} />
        <ArtifactPreview artifact={{ id: "slides", name: "deck.md", kind: "slides", content: "# Deck\n---\nPage", source: "测试" }} />
        <ArtifactPreview artifact={{ id: "img", name: "pic.png", kind: "attachment", mimeType: "image/png", url: "/pic.png", source: "测试" }} />
      </>,
    );
    expect(screen.getByText("标题")).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: "下一页" })[0]);
    await waitFor(() => expect(screen.getByText("diagram")).toBeInTheDocument());
    await user.click(screen.getAllByLabelText("复制")[0]);
    for (const toggle of screen.getAllByRole("button", { name: "查看源码" }).slice(0, 3))
      await user.click(toggle);
    fireEvent.doubleClick(screen.getByAltText("示例图"));
    expect(screen.getByRole("dialog", { name: /查看大图 示例图/ })).toBeInTheDocument();
    await user.click(screen.getAllByLabelText("关闭大图").at(-1));
    const sourceTabs = screen.queryAllByRole("tab", { name: "源码" });
    if (sourceTabs.length) await user.click(sourceTabs[0]);
    const downloads = screen.queryAllByLabelText("下载文件");
    if (downloads.length) await user.click(downloads[0]);
  });

  test("loads local and remote artifact contents", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (url) => ({
      ok: !String(url).includes("broken"),
      text: async () => "remote body",
    }));
    render(
      <>
        <ArtifactPreview artifact={{
          id: "local-attachment-1",
          name: "local.txt",
          mimeType: "text/plain",
          localDataUrl: `data:text/plain;base64,${btoa("local body")}`,
        }} />
        <ArtifactPreview artifact={{
          id: "remote",
          name: "remote.md",
          mimeType: "text/markdown",
          kind: "markdown",
          url: "/remote.md",
        }} />
        <ArtifactPreview artifact={{
          id: "binary",
          name: "archive.zip",
          mimeType: "application/zip",
          url: "/archive.zip",
        }} />
      </>,
    );
    expect(await screen.findByText("local body")).toBeInTheDocument();
    expect(await screen.findByText("remote body")).toBeInTheDocument();
    expect(screen.getByText("此类型文件可通过右上角下载查看。")).toBeInTheDocument();
    globalThis.fetch = originalFetch;
  });

  test("offers oversized text artifacts for download instead of loading them", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => ({ ok: true, text: async () => "body" }));
    render(
      <>
        <ArtifactPreview artifact={{
          id: "big",
          name: "big.log",
          mimeType: "text/plain",
          url: "/big.log",
          size: 6 * 1024 * 1024,
        }} />
        <ArtifactPreview artifact={{
          id: "small",
          name: "small.log",
          mimeType: "text/plain",
          url: "/small.log",
          size: 1024,
        }} />
      </>,
    );
    expect(await screen.findByText("body")).toBeInTheDocument();
    expect(screen.getByText("文件较大，不在页面内预览，可通过右上角下载查看。")).toBeInTheDocument();
    expect(globalThis.fetch).toHaveBeenCalledTimes(1);
    expect(globalThis.fetch).toHaveBeenCalledWith("/small.log");
    globalThis.fetch = originalFetch;
  });

  test("offers binary content declared as Markdown for download", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => ({ ok: true, text: async () => "PK\u0003\u0004\u0000\u0000garbage" }));
    render(
      <ArtifactPreview artifact={{
        id: "zip",
        name: "pkg.zip",
        mimeType: "text/markdown",
        url: "/pkg.zip",
        size: 2048,
      }} />,
    );
    expect(await screen.findByText("此类型文件可通过右上角下载查看。")).toBeInTheDocument();
    expect(screen.queryByText(/garbage/)).not.toBeInTheDocument();
    globalThis.fetch = originalFetch;
  });

  test("previews an attached HTML file in the script-only sandbox", async () => {
    const user = userEvent.setup();
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => ({ ok: true, text: async () => "<h1>Demo</h1>" }));
    render(
      <ArtifactPreview artifact={{
        id: "demo",
        name: "sidebar-demo.html",
        kind: "attachment",
        mimeType: "text/html",
        url: "/api/assistant/attachments/demo?sessionId=s1",
      }} />,
    );
    const frame = await screen.findByTitle("HTML 产物预览");
    expect(frame).toHaveAttribute("sandbox", "allow-scripts");
    expect(frame).toHaveAttribute("src", "/chatbot/preview.html");
    await user.click(screen.getByRole("tab", { name: "源码" }));
    expect(screen.getByText("<h1>Demo</h1>")).toBeInTheDocument();
    globalThis.fetch = originalFetch;
  });

  test("keeps quoted fields and omits trailing blank lines in a CSV preview", () => {
    render(<ArtifactPreview artifact={{
      id: "checks", name: "checks.csv", mimeType: "text/csv",
      content: '检查项,结果\r\n回归,"通过, 42 项"\r\n\r\n',
    }} />);
    expect(screen.getByRole("table", { name: "checks.csv" })).toBeVisible();
    expect(screen.getAllByRole("row")).toHaveLength(2);
    expect(screen.getByRole("cell", { name: "通过, 42 项" })).toBeVisible();
  });

  test("exercises common UI primitives", async () => {
    const user = userEvent.setup();
    const close = vi.fn();
    const anchor = document.createElement("button");
    document.body.append(anchor);
    render(
      <>
        <IconButton label="图标按钮" onClick={close}>x</IconButton>
        <CredentialAction text="未配置 CodeBuddy API Key" />
        <Menu label="菜单" onClose={close}>
          <button role="menuitem">第一项</button>
          <button role="menuitem">第二项</button>
        </Menu>
        <Dialog title="对话框" onClose={close} footer={<button>确定</button>}>
          <input aria-label="输入" />
        </Dialog>
        <SelectMenu
          label="选项"
          value=""
          options={[
            { id: "a", label: "甲", icon: "doc", section: "常用", description: "第一项" },
            { id: "b", label: "乙", section: "其他" },
          ]}
          onSelect={close}
          onClose={close}
          anchorRef={{ current: anchor }}
          searchable
          actions={[{ id: "new", label: "新建", icon: "plus", onSelect: close }]}
          open
        />
        <SessionContextMenu x={10} y={10} anchor={anchor} activeSessionId="s1" onClose={close}>
          <button role="menuitem">会话项</button>
        </SessionContextMenu>
      </>,
    );
    await user.click(screen.getByLabelText("图标按钮"));
    const combobox = screen.getByRole("combobox");
    await user.type(combobox, "甲");
    const select = document.querySelector(".select-menu");
    fireEvent.keyDown(select, { key: "ArrowDown" });
    fireEvent.keyDown(select, { key: "Home" });
    fireEvent.keyDown(select, { key: "Enter" });
    fireEvent.keyDown(document, { key: "Escape" });
    const menu = screen.getByRole("menu", { name: "菜单" });
    screen.getByRole("menuitem", { name: "第一项" }).focus();
    fireEvent.keyDown(menu, { key: "End" });
    fireEvent.keyDown(menu, { key: "Home" });
    fireEvent.keyDown(menu, { key: "ArrowUp" });
    fireEvent.pointerDown(document.body);
    fireEvent.mouseDown(document.querySelector(".dialog-backdrop"));
    expect(close).toHaveBeenCalled();
  });

  test("closes a controlled select and runs its action row", async () => {
    const user = userEvent.setup();
    const action = vi.fn();
    function ControlledSelect() {
      const [open, setOpen] = React.useState(true);
      const anchor = React.useRef(null);
      return (
        <div className="chat-shell">
          <button ref={(node) => {
            anchor.current = node;
            if (node)
              node.getBoundingClientRect = () => ({
                top: 200, bottom: 230, left: 200, right: 300,
                width: 100, height: 30,
              });
          }}>anchor</button>
          <SelectMenu
            label="受控选项"
            value="a"
            options={[]}
            actions={[{ id: "new", label: "新建项目", icon: "plus", onSelect: action }]}
            onSelect={vi.fn()}
            onClose={() => setOpen(false)}
            anchorRef={anchor}
            open={open}
            emptyText="没有选项"
          />
        </div>
      );
    }
    render(<ControlledSelect />);
    expect(screen.getByText("没有选项")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("option", { name: "新建项目" }));
    expect(action).toHaveBeenCalled();
  });

  test("links an issue through the selector", async () => {
    const user = userEvent.setup();
    render(<IssueContext chat={chat} open hideTrigger onClose={vi.fn()} />);
    expect(await screen.findByText("需求一")).toBeInTheDocument();
    await user.click(screen.getByText("需求一"));
    expect(chat.linkIssue).toHaveBeenCalledWith("ISSUE-1");
  });

  test("unlinks an existing issue", async () => {
    const user = userEvent.setup();
    const existingChat = { ...chat, issueId: "ISSUE-1", linkIssue: vi.fn(async () => true) };
    render(<IssueContext chat={existingChat} open hideTrigger onClose={vi.fn()} />);
    await user.click(await screen.findByText("解除关联"));
    expect(existingChat.linkIssue).toHaveBeenCalledWith("");
  });

  test("renders side panel tabs, draft card and artifact directory", async () => {
    const user = userEvent.setup();
    const setSelected = vi.fn();
    const draft = {
      id: "d1",
      name: "保存需求",
      kind: "operation",
      draft: { tool: "issues_save", body: { projectId: "p1", title: "需求" } },
    };
    const { rerender } = render(
      <>
        <SidePanel chat={chat} tab="sop" setTab={vi.fn()} selected={null} setSelected={setSelected} onClose={vi.fn()} />
        <DraftCard artifact={draft} onApply={vi.fn(async () => ({}))} onPreview={vi.fn()} context={chat} />
      </>,
    );
    expect(await screen.findByText("流程一")).toBeInTheDocument();
    await user.type(screen.getByPlaceholderText("新流程或这次要做的事"), "新任务");
    await user.click(screen.getByText("派发 LOOP"));
    await waitFor(() => expect(writeMock).toHaveBeenCalled());
    await user.type(screen.getByPlaceholderText("新流程或这次要做的事"), "复制流程");
    await user.click(screen.getByText("创建 LOOP"));
    const runRow = await screen.findByRole("button", { name: /交付流程/ });
    await user.click(runRow);
    expect(await screen.findByText("完成任务")).toBeInTheDocument();

    rerender(
      <div className="chat-shell">
        <header className="conversation-header" />
        <main className="conversation-main" />
        <ArtifactPanel
          artifacts={[
            { id: "f1", name: "readme.md", kind: "code", content: "text", source: "会话" },
            { id: "sop1", name: "流程", kind: "sop", asset: { id: "wf1", status: "published" } },
            draft,
          ]}
          runs={[run]}
          selected={null}
          setSelected={setSelected}
          chat={chat}
          onClose={vi.fn()}
          open
        />
      </div>,
    );
    await user.type(screen.getByLabelText("搜索产物"), "readme");
    await user.click(screen.getByText("readme.md"));
    expect(setSelected).toHaveBeenCalled();
    rerender(
      <div className="chat-shell">
        <header className="conversation-header" />
        <main className="conversation-main" />
        <ArtifactPanel
          artifacts={[{ id: "f1", name: "readme.md", kind: "code", content: "text", source: "会话" }]}
          runs={[run]}
          selected={{ kind: "run", id: "r1", runId: "r1", name: "交付流程" }}
          setSelected={setSelected}
          chat={chat}
          onClose={vi.fn()}
          open
        />
      </div>,
    );
    expect(screen.getByLabelText("LOOP 运行过程")).toBeInTheDocument();
  });

  test("renders file artifacts and validates LOOP actions without a project", async () => {
    const user = userEvent.setup();
    const select = vi.fn();
    const artifactChat = {
      ...chat,
      projectId: "",
      projects: [],
      session: {
        id: "s-files",
        projectId: "",
        messages: [{
          role: "assistant",
          content: JSON.stringify({
            message: "```mermaid\ngraph LR\nA-->B\n```",
            drafts: [{ tool: "workflows_save", body: { name: "文件流程" } }],
          }),
          attachments: [{ id: "file1", name: "report.txt", mimeType: "text/plain", size: 2048 }],
        }],
      },
    };
    const { rerender } = render(
      <SidePanel
        chat={artifactChat}
        tab="files"
        setTab={vi.fn()}
        selected={null}
        setSelected={select}
        onClose={vi.fn()}
      />,
    );
    await user.click(screen.getByText("report.txt"));
    expect(select).toHaveBeenCalled();
    rerender(
      <SidePanel
        chat={artifactChat}
        tab="sop"
        setTab={vi.fn()}
        selected={null}
        setSelected={select}
        onClose={vi.fn()}
      />,
    );
    await user.type(screen.getByPlaceholderText("新流程或这次要做的事"), "任务");
    await user.click(screen.getByText("派发 LOOP"));
    expect(screen.getByRole("alert")).toHaveTextContent("请先点右上角的项目名称选择或绑定项目");
  });

  test("renders selected draft and definition previews", async () => {
    const draft = {
      id: "d2",
      name: "角色草稿",
      kind: "role",
      draft: {
        tool: "agents_save",
        body: { id: "role1", name: "评审角色", description: "负责评审", prompt: "认真检查" },
      },
    };
    const { rerender } = render(
      <SidePanel
        chat={{ ...chat, session: { ...chat.session, messages: [{ role: "assistant", content: JSON.stringify({ message: "草稿", drafts: [draft.draft] }) }] } }}
        tab="files"
        setTab={vi.fn()}
        selected={draft}
        setSelected={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    expect(screen.getByText("评审角色")).toBeInTheDocument();
    rerender(
      <div className="chat-shell">
        <header className="conversation-header" />
        <main className="conversation-main" />
        <ArtifactPanel
          artifacts={[draft]}
          runs={[run]}
          selected={draft}
          setSelected={vi.fn()}
          chat={chat}
          onClose={vi.fn()}
          open
        />
      </div>,
    );
    expect(screen.getAllByText("评审角色").length).toBeGreaterThan(0);
  });

  test("loads saved definitions and previews executed operations", async () => {
    apiMock.mockResolvedValueOnce({
      id: "wf-saved",
      name: "已保存流程",
      description: "流程说明",
      status: "published",
      nodes: [{ id: "n1", name: "实现", type: "agent" }],
    });
    const { rerender } = render(
      <div className="chat-shell">
        <header className="conversation-header" />
        <main className="conversation-main" />
        <ArtifactPanel
          artifacts={[]}
          runs={[run]}
          selected={{ id: "saved-sop", kind: "sop", name: "流程", asset: { id: "wf-saved" } }}
          setSelected={vi.fn()}
          chat={chat}
          onClose={vi.fn()}
          open
        />
      </div>,
    );
    expect(await screen.findByText("已保存流程")).toBeInTheDocument();
    rerender(
      <div className="chat-shell">
        <header className="conversation-header" />
        <main className="conversation-main" />
        <ArtifactPanel
          artifacts={[]}
          runs={[run]}
          selected={{
            id: "operation1",
            kind: "operation",
            name: "执行结果",
            draft: { tool: "runs_start", saved: true, savedResult: { data: { runId: "r1" } }, body: {} },
          }}
          setSelected={vi.fn()}
          chat={chat}
          onClose={vi.fn()}
          open
        />
      </div>,
    );
    expect(screen.getByLabelText("会话产物")).toBeInTheDocument();
  });

  test("scopes artifact knowledge reads to the selected run", () => {
    const otherRunReads = [
      {
        id: "kr_other",
        serverId: "srv",
        tool: "search_knowledge",
        status: "hit",
        evidences: [{ id: "ev_other_1", title: "另一轮的文档.md", cited: true }],
      },
    ];
    const artifact = {
      id: "kb-art",
      name: "报告.md",
      kind: "code",
      mimeType: "text/markdown",
      content: "端口配置见 [[knowledge:ev_other_1]]。",
      runId: "r-target",
      nodeId: "n1",
    };
    const runs = [
      { id: "r-target", nodes: { n1: { status: "succeeded" } } },
      {
        id: "r-other",
        nodes: { n1: { status: "succeeded", knowledgeReads: otherRunReads } },
      },
    ];
    const panel = (selected) => (
      <div className="chat-shell">
        <header className="conversation-header" />
        <main className="conversation-main" />
        <ArtifactPanel
          artifacts={[artifact]}
          runs={runs}
          selected={selected}
          setSelected={vi.fn()}
          chat={chat}
          onClose={vi.fn()}
          open
        />
      </div>
    );
    const { container, rerender } = render(panel(artifact));
    expect(container.textContent).toContain("端口配置见");
    // The selected run has no reads for this node: never borrow another run's.
    expect(screen.queryByText("产物引用来源")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /引用 \[1\] 另一轮的文档\.md/ }),
    ).not.toBeInTheDocument();
    // Without a runId the nodeId fallback keeps working.
    rerender(panel({ ...artifact, runId: undefined }));
    expect(screen.getByText("产物引用来源")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /引用 \[1\] 另一轮的文档\.md/ }),
    ).toBeInTheDocument();
  });

  test("loads repository choices and applies an import draft", async () => {
    const user = userEvent.setup();
    apiMock.mockResolvedValueOnce([
      { id: "repo1", name: "仓库一", projectId: "p1" },
      { id: "repo2", name: "仓库二", projectId: "p1" },
    ]);
    const apply = vi.fn(async () => ({}));
    render(
      <DraftCard
        artifact={{
          id: "import1",
          messageIndex: 1,
          draftIndex: 0,
          name: "导入 TAPD 需求",
          kind: "operation",
          draft: {
            tool: "issues_import_tapd",
            body: { projectId: "p1", workspaceId: "tapd", repositoryIds: [] },
            warnings: ["导入前请检查"],
          },
        }}
        onApply={apply}
        onPreview={vi.fn()}
        context={{ projectId: "p1", projects: chat.projects, references: [] }}
      />,
    );
    await user.click(await screen.findByLabelText("仓库一"));
    const confirm = screen.queryByRole("button", { name: /^确认/ });
    if (confirm && !confirm.disabled) await user.click(confirm);
    expect(screen.getByText("导入前请检查")).toBeInTheDocument();
  });

  test("a LOOP proposal is declined or muted from its card", async () => {
    const user = userEvent.setup();
    const decide = vi.fn(async () => ({}));
    const artifact = (draft) => ({
      id: "sop2",
      messageIndex: 3,
      draftIndex: 0,
      name: "积分排行榜",
      kind: "operation",
      draft: {
        tool: "issues_save",
        body: { projectId: "p1", title: "积分排行榜", kind: "requirement", workflowId: "delivery" },
        proposal: { reason: "后续可以走 LOOP 完整交付", timing: "end" },
        ...draft,
      },
    });
    const context = { projectId: "p1", projects: chat.projects, references: [] };
    const { rerender } = render(
      <DraftCard artifact={artifact({})} onApply={vi.fn()} onDecide={decide} onPreview={vi.fn()} context={context} />,
    );
    expect(screen.getByText(/可选的后续工作：后续可以走 LOOP/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "不需要" }));
    expect(decide).toHaveBeenLastCalledWith(3, 0, "declined");
    await user.click(screen.getByRole("button", { name: "本会话不再建议" }));
    expect(decide).toHaveBeenLastCalledWith(3, 0, "muted");

    rerender(
      <DraftCard
        artifact={artifact({ proposal: { reason: "整件需求走 LOOP", timing: "start" } })}
        onApply={vi.fn()}
        onDecide={decide}
        onPreview={vi.fn()}
        context={context}
      />,
    );
    expect(screen.getByRole("button", { name: "让 AI 助手自己做" })).toBeInTheDocument();
    expect(screen.queryByRole("timer")).not.toBeInTheDocument();

    const expiresAt = new Date(Date.now() + 125_000).toISOString();
    rerender(
      <DraftCard
        artifact={artifact({ proposal: { reason: "整件需求走 LOOP", timing: "start", expiresAt } })}
        onApply={vi.fn()}
        onDecide={decide}
        onPreview={vi.fn()}
        context={context}
      />,
    );
    expect(screen.getByRole("timer")).toHaveTextContent(/^2:0[45] 后未确认，AI 助手将直接执行$/);

    rerender(
      <DraftCard
        artifact={artifact({
          proposal: { reason: "整件需求走 LOOP", timing: "start", expiresAt: new Date(Date.now() - 1000).toISOString() },
        })}
        onApply={vi.fn()}
        onDecide={decide}
        onPreview={vi.fn()}
        context={context}
      />,
    );
    expect(screen.getByRole("timer")).toHaveTextContent("已超时，AI 助手即将直接执行");

    rerender(
      <DraftCard
        artifact={artifact({ proposal: { reason: "整件需求走 LOOP", timing: "start", expiresAt }, decision: "expired" })}
        onApply={vi.fn()}
        onDecide={decide}
        onPreview={vi.fn()}
        context={context}
      />,
    );
    expect(screen.getByRole("button", { name: "已超时，AI 助手直接执行" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "建需求并走 LOOP" })).not.toBeInTheDocument();
    expect(screen.queryByRole("timer")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "让 AI 助手自己做" })).not.toBeInTheDocument();

    rerender(
      <DraftCard artifact={artifact({ decision: "declined" })} onApply={vi.fn()} onDecide={decide} onPreview={vi.fn()} context={context} />,
    );
    expect(screen.getByText(/未采纳/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "建需求并走 LOOP" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "本会话不再建议" })).not.toBeInTheDocument();

    rerender(
      <DraftCard
        artifact={artifact({ decision: "rejected", review: { model: "claude-code:claude-haiku-4-5", rationale: "缺少发布授权" } })}
        onApply={vi.fn()}
        onDecide={decide}
        onPreview={vi.fn()}
        context={context}
      />,
    );
    expect(screen.getByText(/AI 已拒绝/)).toBeInTheDocument();
    expect(screen.getByText(/缺少发布授权/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "建需求并走 LOOP" })).not.toBeInTheDocument();
  });

  test("a LOOP proposal card names the LOOP route", async () => {
    const user = userEvent.setup();
    const apply = vi.fn(async () => ({}));
    const decide = vi.fn(async () => ({}));
    render(
      <DraftCard
        artifact={{
          id: "sop1",
          messageIndex: 1,
          draftIndex: 0,
          name: "积分排行榜",
          kind: "operation",
          draft: {
            tool: "issues_save",
            body: { projectId: "p1", title: "积分排行榜", kind: "requirement", workflowId: "delivery" },
            proposal: { reason: "新功能，LOOP 用途对得上", timing: "start" },
          },
        }}
        onApply={apply}
        onDecide={decide}
        onPreview={vi.fn()}
        context={{ projectId: "p1", projects: [{ id: "p1", name: "项目一" }], references: [] }}
      />,
    );
    expect(screen.getByText(/建议整件事走 LOOP：新功能/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "建需求并走 LOOP" }));
    expect(apply).toHaveBeenCalledWith(1, 0, expect.anything());
    await user.click(screen.getByRole("button", { name: "让 AI 助手自己做" }));
    expect(decide).toHaveBeenLastCalledWith(1, 0, "declined");
  });

  test("opens LOOP node conversation and its artifacts", async () => {
    const user = userEvent.setup();
    render(<SopRunPanel runs={[run]} onClose={vi.fn()} />);
    await user.click(screen.getByText("实现"));
    expect(await screen.findByText("会话过程")).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: /产物/ }));
    await user.click(screen.getByText("result.md"));
    expect(screen.getByText("返回节点产物")).toBeInTheDocument();
  });

  test("shows the node composer under a finished script result", async () => {
    const originalFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async (url) => {
      const path = String(url);
      if (path.includes("/conversation?")) return new Response(null, { status: 204 });
      const list = /^\/api\/(projects|users|sandboxes)(\?|$)/.test(path);
      return new Response(list ? "[]" : "{}", { headers: { "Content-Type": "application/json" } });
    });
    try {
      render(<SopRunPanel runs={[{
        ...run, reviewAccess: { canDiscuss: true },
        workflowSnapshot: { ...run.workflowSnapshot, nodes: [{ id: "n1", name: "收集证据", type: "script" }] },
        nodes: { n1: { nodeId: "n1", status: "succeeded", output: "已收集检查证据" } },
        artifacts: [],
      }]} onClose={vi.fn()} />);
      await userEvent.setup().click(screen.getByText("收集证据"));
      await waitFor(() => expect(screen.getByRole("textbox", { name: "询问此节点" })).toBeEnabled());
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  test("lets a node discussion send when the default CLI is missing on the platform server", async () => {
    // A node discussion runs in the run's environment, like the run
    // conversation, so a CLI missing on the platform server must not
    // silently disable sending.
    const fallback = apiMock.getMockImplementation();
    apiMock.mockImplementation(async (path) => {
      if (path.includes("/conversation?")) return null;
      if (path === "/api/auth/current") return { user: { id: 7, loginName: "member" } };
      if (path.startsWith("/api/assistant/models")) return {
        models: [], preferences: { executor: "cursor" },
        engines: [{ id: "cursor", name: "Cursor", configured: true, serverMissing: true }],
      };
      return fallback(path);
    });
    try {
      render(<SopRunPanel runs={[{
        ...run, reviewAccess: { canDiscuss: true },
        workflowSnapshot: { ...run.workflowSnapshot, nodes: [{ id: "n1", name: "收集证据", type: "script" }] },
        nodes: { n1: { nodeId: "n1", status: "succeeded", output: "已收集检查证据" } },
        artifacts: [],
      }]} onClose={vi.fn()} />);
      const user = userEvent.setup();
      await user.click(screen.getByText("收集证据"));
      const box = await screen.findByRole("textbox", { name: "询问此节点" });
      await waitFor(() => expect(box).toBeEnabled());
      await user.type(box, "这个节点做了什么？");
      await waitFor(() => expect(screen.getByRole("button", { name: "发送节点消息" })).toBeEnabled());
    } finally {
      apiMock.mockImplementation(fallback);
    }
  });

  test.each([
    ["the failed reply carries the install action", true, 1],
    ["no message keeps the failure", false, 1],
  ])("shows one install button for a missing CLI when %s", async (_name, stored, count) => {
    const failure = {
      code: "CLI_NOT_INSTALLED",
      message: "Codex（codex）没有安装在当前服务器上。安装后即可在对话里使用。",
    };
    const fallback = apiMock.getMockImplementation();
    const generation = {
      id: "g1", status: "failed", error: failure.message, failure,
      startedAt: "2026-11-25T10:00:00Z", events: [],
    };
    apiMock.mockImplementation(async (path) => {
      if (path.includes("/conversation?")) return { id: "s1" };
      if (path.startsWith("/api/assistant/sessions/s1/generation")) return generation;
      if (path.startsWith("/api/assistant/sessions/s1/messages")) return {};
      if (path.startsWith("/api/assistant/sessions/s1"))
        return {
          id: "s1", runId: "r1", messages: [
            { role: "user", content: "这个节点做了什么？", authorId: 7, startedAt: "2026-11-25T10:00:00Z" },
            {
              role: "assistant", content: "", generationId: "g1", unfinished: failure.message,
              ...(stored ? { failure } : {}),
            },
          ],
        };
      if (path === "/api/auth/current") return { user: { id: 7, loginName: "member" } };
      if (path.startsWith("/api/assistant/models")) return {
        models: [], preferences: { executor: "codex" },
        engines: [{ id: "codex", name: "Codex", configured: true, serverInstallable: true }],
      };
      return fallback(path);
    });
    vi.stubGlobal("fetch", vi.fn(async () => ({
      ok: true, status: 200, json: async () => ({ engine: "codex", status: "missing" }),
    })));
    try {
      render(<SopRunPanel runs={[{
        ...run, reviewAccess: { canDiscuss: true },
        workflowSnapshot: { ...run.workflowSnapshot, nodes: [{ id: "n1", name: "收集证据", type: "script" }] },
        nodes: { n1: { nodeId: "n1", status: "succeeded", output: "已收集检查证据" } },
        artifacts: [],
      }]} onClose={vi.fn()} />);
      await userEvent.setup().click(screen.getByText("收集证据"));
      await waitFor(() => expect(screen.getAllByRole("button", { name: "安装 Codex" }).length).toBeGreaterThan(0));
      await waitFor(() => expect(screen.queryAllByRole("button", { name: "安装 Codex" })).toHaveLength(count));
      if (!stored) expect(screen.getByRole("alert")).toHaveTextContent(failure.message);
    } finally {
      apiMock.mockImplementation(fallback);
      vi.unstubAllGlobals();
    }
  });

  test("switches among runs and renders mixed node statuses", async () => {
    const user = userEvent.setup();
    const mixed = {
      id: "r2",
      status: "running",
      input: "执行混合流程",
      workflowSnapshot: {
        name: "混合流程",
        nodes: [
          { id: "done", name: "已完成节点", type: "agent" },
          { id: "active", name: "运行节点", type: "agent" },
          { id: "pending", name: "待执行节点", type: "agent" },
        ],
      },
      nodes: {
        done: { nodeId: "done", status: "succeeded", output: "完成输出" },
        active: { nodeId: "active", status: "running", activity: "处理中" },
      },
      artifacts: [],
    };
    render(<SopRunPanel runs={[run, mixed]} onClose={vi.fn()} embedded />);
    await user.click(screen.getByLabelText(/^选择会话关联运行：/));
    await user.click(screen.getByRole("option", { name: "混合流程" }));
    expect(screen.getAllByText("混合流程").length).toBeGreaterThan(0);
    expect(screen.getByText("待执行")).toBeInTheDocument();
    await user.click(screen.getByText("运行节点"));
    expect(await screen.findByText("执行混合流程")).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: /产物/ }));
    expect(screen.getByText("暂无产物")).toBeInTheDocument();
  });

  test("shows and operates a failed run alert", async () => {
    const user = userEvent.setup();
    const onAsk = vi.fn();
    render(
      <RunAlerts
        runs={[run]}
        user={{ id: 1 }}
        users={[]}
        readOnly={false}
        onOpenNode={vi.fn()}
        onAsk={onAsk}
        onChanged={vi.fn()}
      />,
    );
    expect(screen.getByText(/执行失败/)).toBeInTheDocument();
    await user.click(screen.getByText("重试节点"));
    await user.click(screen.getByText("让 AI 分析"));
    expect(onAsk).toHaveBeenCalled();
  });

  test("a CodeBuddy key alert links to the key page of the owner's network environment", () => {
    const { unmount } = render(
      <RunAlerts
        runs={[run]}
        user={{ id: 1 }}
        users={[{ id: 1, codeBuddyInternetEnvironment: "internal" }]}
        readOnly={false}
        onOpenNode={vi.fn()}
        onAsk={vi.fn()}
        onChanged={vi.fn()}
      />,
    );
    expect(screen.getByRole("link", { name: /国内站创建 Key/ })).toHaveAttribute(
      "href",
      "https://copilot.tencent.com/profile/",
    );
    unmount();
    render(
      <RunAlerts
        runs={[{ ...run, executionContext: { responsibleUserId: 2 } }]}
        user={{ id: 1, codeBuddyInternetEnvironment: "internal" }}
        users={[{ id: 2, loginName: "owner", codeBuddyInternetEnvironment: "ioa" }]}
        readOnly={false}
        onOpenNode={vi.fn()}
        onAsk={vi.fn()}
        onChanged={vi.fn()}
      />,
    );
    expect(screen.getByRole("link", { name: /iOA创建 Key/ })).toHaveAttribute(
      "href",
      "https://tencent.sso.copilot.tencent.com/profile/keys",
    );
  });

  test("opens the waiting approval in the chat sidebar", async () => {
    const onOpenNode = vi.fn();
    const waiting = { ...run, status: "waiting", nodes: { n1: { nodeId: "n1", status: "waiting_approval" } } };
    render(
      <RunAlerts
        runs={[waiting]}
        user={{ id: 1 }}
        users={[]}
        readOnly={false}
        onOpenNode={onOpenNode}
        onAsk={vi.fn()}
        onChanged={vi.fn()}
      />,
    );
    expect(screen.getByText("待审核")).toBeInTheDocument();
    expect(screen.queryByText("人工门禁")).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "去审核" }));
    expect(onOpenNode).toHaveBeenCalledWith(waiting, "n1");
  });

  test("lists a submitted merge request in the artifact panel and opens it on 工蜂", () => {
    const mr = {
      id: "mr-example/agent-flow/631",
      name: "agent-flow !631",
      kind: "merge-request",
      url: "https://git.example.com/example/agent-flow/merge_requests/631",
      external: true,
      source: "MR · example/agent-flow",
    };
    render(
      <div className="chat-shell">
        <header className="conversation-header" />
        <main className="conversation-main" />
        <ArtifactPanel
          artifacts={[mr]}
          runs={[]}
          selected={null}
          setSelected={vi.fn()}
          chat={chat}
          onClose={vi.fn()}
          open
        />
      </div>,
    );
    expect(screen.getByText("合并请求")).toBeInTheDocument();
    const link = screen.getByTitle("agent-flow !631 · 在代码仓库打开");
    expect(link.getAttribute("href")).toBe(mr.url);
    expect(link.getAttribute("target")).toBe("_blank");
  });

  test("lets failed agent retry with note and failed script pass after confirmation", async () => {
    const user = userEvent.setup();
    apiMock.mockClear();
    const failedRun = {
      ...run,
      id: "r-fail-controls",
      reviewAccess: { canOperate: true },
      workflowSnapshot: {
        name: "交付流程",
        nodes: [
          { id: "n1", name: "实现", type: "agent" },
          { id: "n2", name: "测试脚本", type: "script" },
        ],
      },
      nodes: {
        n1: { nodeId: "n1", status: "failed", error: "构建超时" },
        n2: { nodeId: "n2", status: "failed", error: "退出码 1" },
      },
      artifacts: [],
    };
    render(<SopRunPanel runs={[failedRun]} onClose={vi.fn()} />);
    await user.click(screen.getByText("实现"));
    await user.click(await screen.findByRole("button", { name: "补充信息或重跑" }));
    await user.type(screen.getByLabelText("重跑说明（选填）"), "跳过缓存重跑");
    await user.click(screen.getByRole("button", { name: "补充信息并重跑" }));
    expect(apiMock).toHaveBeenCalledWith(
      "/api/runs/r-fail-controls/retry",
      expect.objectContaining({
        method: "POST",
        body: expect.stringContaining("跳过缓存重跑"),
      }),
    );

    await user.click(screen.getByRole("button", { name: "全部流程节点" }));
    await user.click(screen.getByText("测试脚本"));
    await user.click(await screen.findByRole("button", { name: "手动通过，继续下游" }));
    await user.click(screen.getByRole("button", { name: "确认通过，跳过不重跑" }));
    expect(apiMock).toHaveBeenCalledWith(
      "/api/runs/r-fail-controls/pass",
      expect.objectContaining({
        method: "POST",
        body: expect.stringContaining('"nodeId":"n2"'),
      }),
    );
  });

  test("previews earlier node attempts from the run overview and its file navigation", async () => {
    const user = userEvent.setup();
    const reviewRun = {
      ...run,
      id: "r-replayed",
      status: "succeeded",
      nodes: { n1: { nodeId: "n1", status: "succeeded", attempt: 2 } },
      artifacts: [
        { id: "new", nodeId: "n1", path: "new.md", mediaType: "text/markdown", attempt: 2 },
        { id: "old", nodeId: "n1", path: "old.md", mediaType: "text/markdown", attempt: 1 },
      ],
    };
    const fileRead = vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => ({
      ok: true,
      text: async () => `Body of ${String(url).split("/").pop()}`,
    }));
    try {
      render(
        <ArtifactPanel
          artifacts={[]}
          runs={[reviewRun]}
          selected={{ kind: "run", id: "run-replayed", runId: reviewRun.id }}
          setSelected={vi.fn()}
          chat={chat}
          onClose={vi.fn()}
          open
        />,
      );
      await user.click(screen.getByText("new.md"));
      expect(await screen.findByText("Body of new")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "下一个产物" }));
      expect(await screen.findByText("Body of old")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "上一个产物" }));
      expect(await screen.findByText("Body of new")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "返回运行概览" }));
      await user.click(screen.getByText("old.md"));
      expect(await screen.findByText("Body of old")).toBeInTheDocument();
    } finally {
      fileRead.mockRestore();
    }
  });

  test("steps between node artifacts in a single back-toolbar and groups upstream review artifacts in filesOnly", async () => {
    const user = userEvent.setup();
    const reviewRun = {
      id: "r-art-nav",
      status: "waiting",
      workflowSnapshot: {
        name: "审阅流程",
        nodes: [
          { id: "n1", name: "方案", type: "agent" },
          { id: "n2", name: "人工门禁", type: "approval", after: ["n1"] },
        ],
      },
      nodes: {
        n1: { nodeId: "n1", status: "succeeded", output: "ok" },
        n2: { nodeId: "n2", status: "waiting_approval" },
      },
      artifacts: [
        { id: "a1", nodeId: "n1", path: "plan.md", mediaType: "text/markdown", content: "# 方案" },
        { id: "a2", nodeId: "n2", path: "check.md", mediaType: "text/markdown", content: "# 检查" },
        { id: "a3", nodeId: "n2", path: "summary.md", mediaType: "text/markdown", content: "# 总结" },
      ],
    };

    function StatefulArtifactDrawer() {
      const [selected, setSelected] = React.useState(null);
      return (
        <div className="chat-shell">
          <header className="conversation-header" />
          <main className="conversation-main" />
          <ArtifactPanel
            artifacts={[]}
            runs={[reviewRun]}
            selected={selected}
            setSelected={setSelected}
            chat={chat}
            onClose={vi.fn()}
            open
          />
        </div>
      );
    }

    const { unmount } = render(<StatefulArtifactDrawer />);
    await user.click(screen.getByText(/审阅流程/));
    await user.click(screen.getByText("人工门禁"));
    const nodeTabs = screen.getByRole("tablist", { name: "节点详情" });
    await user.click(within(nodeTabs).getByRole("tab", { name: /产物/ }));
    await user.click(screen.getByText("check.md"));
    expect(screen.getAllByRole("button", { name: "返回节点产物" })).toHaveLength(1);
    expect(screen.getByText("1 / 3")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "下一个产物" }));
    expect(screen.getByText("2 / 3")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "返回节点产物" }));
    expect(screen.getByText("check.md")).toBeInTheDocument();
    unmount();

    render(
      <ArtifactPanel
        artifacts={[
          { id: "cur", nodeId: "n2", path: "check.md", mediaType: "text/markdown" },
          { id: "up", nodeId: "n1", path: "plan.md", mediaType: "text/markdown", source: "上游待审 · 方案" },
        ]}
        selected={null}
        setSelected={vi.fn()}
        chat={chat}
        onClose={vi.fn()}
        filesOnly
        open
      />,
    );
    expect(screen.getByText("本节点产物")).toBeInTheDocument();
    expect(screen.getByText("上游待审产物")).toBeInTheDocument();
  });

  test("supports resuming paused runs, replaying succeeded nodes inline, and showing workflow names on multi-run alerts", async () => {
    const user = userEvent.setup();
    apiMock.mockClear();
    const pausedRun = {
      id: "r-paused",
      status: "waiting",
      pauseRequested: true,
      reviewAccess: { canOperate: true },
      workflowSnapshot: {
        name: "发布流程",
        nodes: [
          { id: "n1", name: "构想", type: "agent" },
          { id: "n2", name: "审核", type: "approval", after: ["n1"] },
        ],
      },
      nodes: {
        n1: { nodeId: "n1", status: "succeeded", output: "done" },
        n2: { nodeId: "n2", status: "waiting_approval" },
      },
      artifacts: [],
    };
    const { unmount } = render(<SopRunPanel runs={[pausedRun]} onClose={vi.fn()} />);
    await user.click(screen.getByText("审核"));
    await user.click(await screen.findByRole("button", { name: "继续运行" }));
    expect(apiMock).toHaveBeenCalledWith(
      "/api/runs/r-paused/resume",
      expect.objectContaining({
        method: "POST",
        body: expect.stringContaining('"nodeId":"n2"'),
      }),
    );
    unmount();

    const completedRun = {
      ...pausedRun,
      id: "r-done",
      status: "succeeded",
      pauseRequested: false,
      nodes: {
        n1: { nodeId: "n1", status: "succeeded", output: "done" },
        n2: { nodeId: "n2", status: "succeeded", output: "approved" },
      },
    };
    render(<SopRunPanel runs={[completedRun]} onClose={vi.fn()} />);
    await user.click(screen.getByText("构想"));
    await user.click(await screen.findByRole("button", { name: "从本节点重跑" }));
    await user.type(screen.getByLabelText("补充说明（选填）"), "换用简洁文案");
    await user.click(screen.getByRole("button", { name: "确认从本节点重跑" }));
    expect(apiMock).toHaveBeenCalledWith(
      "/api/runs/r-done/retry",
      expect.objectContaining({
        method: "POST",
        body: expect.stringContaining("换用简洁文案"),
      }),
    );

    const rA = {
      id: "ra",
      status: "waiting",
      workflowSnapshot: { name: "流程A", nodes: [{ id: "n1", name: "审批一" }] },
      nodes: { n1: { nodeId: "n1", status: "waiting_approval" } },
    };
    const rB = {
      id: "rb",
      status: "waiting",
      workflowSnapshot: { name: "流程B", nodes: [{ id: "n1", name: "审批二" }] },
      nodes: { n1: { nodeId: "n1", status: "waiting_approval" } },
    };
    render(
      <RunAlerts
        runs={[rA, rB]}
        user={{ id: 1 }}
        users={[]}
        readOnly={false}
        onOpenNode={vi.fn()}
        onAsk={vi.fn()}
        onChanged={vi.fn()}
      />,
    );
    expect(screen.getByText("流程A · 审批一")).toBeInTheDocument();
    expect(screen.getByText("流程B · 审批二")).toBeInTheDocument();
  });

});
