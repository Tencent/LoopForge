import React, { useRef, useState } from "react";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, test, vi } from "vitest";
import {
  buildKnowledgeSummary,
  extractCitedIdsInOrder,
  filterKnowledgeReadsForAttempt,
  formatTextForCopy,
  maskIncompleteStreamingCitation,
} from "../src/knowledge-citations.js";
import { ArtifactPreview, RichContent } from "../src/RichContent.jsx";
import KnowledgeSources from "../src/KnowledgeSources.jsx";
import { ReplyTimeline } from "../src/ActivityTrace.jsx";
import SopRunPanel from "../src/SopRunPanel.jsx";
import { activityFace, activityTarget } from "../src/activity.js";

const sampleReads = [
  {
    id: "kr_cat_1",
    serverId: "agentflow-knowledge",
    tool: "list_documents",
    status: "命中",
    evidences: [
      {
        id: "ev_cat_1",
        documentId: "doc-catalog-only",
        title: "目录里的无关文档.md",
      },
    ],
  },
  {
    id: "kr_search_1",
    serverId: "agentflow-knowledge",
    tool: "search_knowledge",
    query: "服务端口",
    status: "命中",
    cited: true,
    evidences: [
      {
        id: "ev_aaa_1",
        sourceId: "wk::kb1",
        sourceName: "研发规范库",
        documentId: "wk::doc1",
        title: "部署规范.md",
        locator: "#chunk-2",
        cited: true,
      },
      {
        id: "ev_aaa_2",
        sourceId: "wk::kb1",
        sourceName: "研发规范库",
        documentId: "wk::doc2",
        title: "监控告警手册.md",
        locator: "#chunk-0",
        cited: false,
      },
    ],
  },
  {
    id: "kr_read_1",
    serverId: "agentflow-knowledge",
    tool: "read_document",
    query: "wk::doc3",
    status: "命中",
    cited: false,
    evidences: [
      {
        id: "ev_bbb_1",
        sourceId: "wk::kb1",
        sourceName: "研发规范库",
        documentId: "wk::doc3",
        title: "发布流程.md",
        locator: "#chunk-0..#chunk-2",
        cited: false,
      },
    ],
  },
];

describe("knowledge citations & two-layer summary", () => {
  test("buildKnowledgeSummary separates strict cited docs from retrieved docs and excludes catalog tools", () => {
    const summary = buildKnowledgeSummary(sampleReads, [
      "服务端口为 8080 [[knowledge:ev_aaa_1]]。",
    ]);
    expect(summary.callCount).toBe(3);
    expect(summary.hasContentCalls).toBe(true);
    expect(summary.hasCatalogOnly).toBe(false);
    expect(summary.usedDocs).toHaveLength(1);
    expect(summary.usedDocs[0]).toMatchObject({
      citeIndex: 1,
      label: "部署规范.md",
      sourceName: "研发规范库",
      used: true,
    });
    expect(summary.referencedDocs.map((d) => d.label)).toEqual([
      "部署规范.md",
      "发布流程.md",
      "监控告警手册.md",
    ]);
    expect(summary.referencedDocs[1].readFull).toBe(true);
    expect(summary.referencedDocs[2].readFull).toBe(false);
    expect(summary.byCiteId.get("ev_aaa_1")?.citeIndex).toBe(1);
    expect(summary.byCiteId.get("ev_aaa_2")).toBeUndefined();
  });

  test("buildKnowledgeSummary deduplicates same document across search and read_document", () => {
    const reads = [
      {
        id: "r1",
        tool: "search_knowledge",
        status: "命中",
        cited: true,
        evidences: [
          {
            id: "ev_111_1",
            sourceName: "架构库",
            documentId: "doc-1",
            title: "架构设计.md",
            locator: "#chunk-1",
            cited: true,
          },
        ],
      },
      {
        id: "r2",
        tool: "read_document",
        status: "命中",
        cited: false,
        evidences: [
          {
            id: "ev_222_1",
            sourceName: "架构库",
            documentId: "doc-1",
            title: "架构设计.md",
            locator: "#chunk-0..#chunk-3",
            cited: false,
          },
        ],
      },
    ];
    const summary = buildKnowledgeSummary(reads, ["见 [[knowledge:ev_111_1]]"]);
    expect(summary.referencedDocs).toHaveLength(1);
    expect(summary.usedDocs).toHaveLength(1);
    expect(summary.usedDocs[0].readFull).toBe(true);
    expect(summary.usedDocs[0].locators).toEqual([
      "#chunk-1",
      "#chunk-0..#chunk-3",
    ]);

    // If model emits both search and read IDs of the same document side-by-side,
    // RichContent and formatTextForCopy collapse the duplicate [1][1] into a single [1],
    // while keeping distinct documents [1][2] intact when multiple docs are cited.
    const multiDocReads = [
      ...reads,
      {
        id: "r3",
        tool: "read_document",
        status: "命中",
        cited: true,
        evidences: [
          {
            id: "ev_333_1",
            sourceName: "网络库",
            documentId: "doc-2",
            title: "网络规范.md",
            locator: "#chunk-0",
            cited: true,
          },
        ],
      },
    ];
    const multiText =
      "同文档重复标记 [[knowledge:ev_111_1]][[knowledge:ev_222_1]]，多文档共同引用 [[knowledge:ev_111_1]][[knowledge:ev_333_1]]。";
    const { container } = render(
      <RichContent message={multiText} knowledgeReads={multiDocReads} />,
    );
    const buttons = container.querySelectorAll("button.knowledge-cite-sup");
    expect(Array.from(buttons).map((b) => b.textContent)).toEqual([
      "1",
      "1",
      "2",
    ]);
    const copiedMulti = formatTextForCopy(multiText, multiDocReads, [
      multiText,
    ]);
    expect(copiedMulti).toContain(
      "同文档重复标记 [1]，多文档共同引用 [1][2]。",
    );
  });

  test("RichContent renders numbered superscripts and clicking highlights matching KnowledgeSources row", () => {
    function Harness() {
      const [highlighted, setHighlighted] = useState(null);
      const ref = useRef(null);
      const text =
        "服务默认监听 8080 端口 [[knowledge:ev_aaa_1]]，未匹配标记 [[knowledge:ev_missing]]。";
      return (
        <div>
          <RichContent
            message={text}
            knowledgeReads={sampleReads}
            onCitationClick={setHighlighted}
          />
          <KnowledgeSources
            knowledgeReads={sampleReads}
            text={text}
            highlightedIndex={highlighted}
            containerRef={ref}
          />
        </div>
      );
    }

    const { container } = render(<Harness />);
    expect(container.textContent).not.toContain("[[knowledge:");

    const citeBtn = screen.getByRole("button", { name: "引用 [1] 部署规范.md" });
    expect(citeBtn).toHaveTextContent("1");
    expect(citeBtn).toHaveAttribute(
      "title",
      "研发规范库 · 部署规范.md · #chunk-2",
    );
    expect(container.querySelector(".knowledge-cite-sup.is-unresolved")).toHaveTextContent("?");

    expect(screen.getByText("实际使用 1 篇 · 检索读取 3 篇")).toBeInTheDocument();
    // Default collapsed: details are hidden until toggled or a citation superscript is clicked.
    expect(screen.queryByText("[1] 已用于回答")).not.toBeInTheDocument();

    const headerToggle = screen.getByRole("button", { name: /知识库来源/ });
    expect(headerToggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(headerToggle);
    expect(headerToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("[1] 已用于回答")).toBeInTheDocument();
    expect(screen.getByText("已读正文 · 未引用")).toBeInTheDocument();
    expect(screen.getByText("仅检索未采用")).toBeInTheDocument();
    expect(container.textContent).not.toContain("目录里的无关文档.md");

    // Clicking header again hides the drawer.
    fireEvent.click(headerToggle);
    expect(headerToggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("[1] 已用于回答")).not.toBeInTheDocument();

    // Clicking an inline citation superscript automatically opens the drawer and highlights the row.
    fireEvent.click(citeBtn);
    expect(headerToggle).toHaveAttribute("aria-expanded", "true");
    const usedRow = container.querySelector('.knowledge-doc-row[data-cite-index="1"]');
    expect(usedRow).toHaveClass("is-highlighted");
  });

  test("RichContent preserves literal [[knowledge:...]] for non-KB messages and renders streaming numbers when enabled", () => {
    const { container, rerender } = render(
      <RichContent message="原文示例 [[knowledge:demo]]" />,
    );
    expect(container.textContent).toContain("[[knowledge:demo]]");

    rerender(
      <RichContent
        message="正在生成结论 [[knowledge:ev_stream_1]]"
        citationsEnabled
      />,
    );
    expect(container.textContent).not.toContain("[[knowledge:");
    expect(
      container.querySelector(".knowledge-cite-sup.is-streaming"),
    ).toHaveTextContent("1");
  });

  test("KnowledgeSources strict empty states, catalog-only notice, and >6 expand toggle", () => {
    const manyReads = [
      {
        id: "kr_many",
        serverId: "agentflow-knowledge",
        tool: "search_knowledge",
        status: "命中",
        cited: false,
        evidences: Array.from({ length: 8 }, (_, idx) => ({
          id: `ev_m_${idx + 1}`,
          documentId: `doc-${idx + 1}`,
          title: `候选文档-${idx + 1}.md`,
          cited: false,
        })),
      },
    ];
    const { rerender } = render(
      <KnowledgeSources knowledgeReads={manyReads} text="普通回答无标记" />,
    );
    expect(screen.getByText("实际使用 0 篇 · 检索读取 8 篇")).toBeInTheDocument();
    expect(
      screen.queryByText("未显式标注具体引用文档（以下为本轮检索/读取的候选文档）"),
    ).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /知识库来源/ }));
    expect(
      screen.getByText("未显式标注具体引用文档（以下为本轮检索/读取的候选文档）"),
    ).toBeInTheDocument();

    const expandBtn = screen.getByRole("button", { name: "展开其余 2 篇" });
    expect(screen.queryByText("候选文档-8.md")).not.toBeInTheDocument();
    fireEvent.click(expandBtn);
    expect(screen.getByText("候选文档-8.md")).toBeInTheDocument();

    rerender(
      <KnowledgeSources
        knowledgeReads={[
          {
            id: "kr_cat_only",
            serverId: "agentflow-knowledge",
            tool: "list_knowledge_bases",
            status: "命中",
          },
        ]}
        text="查看目录"
      />,
    );
    expect(
      screen.getByText("仅执行了知识库目录查询，未读取具体文档"),
    ).toBeInTheDocument();
  });

  test("ArtifactPreview passes knowledgeReads to Markdown RichContent", () => {
    const { container } = render(
      <ArtifactPreview
        artifact={{
          id: "art-1",
          name: "report.md",
          mimeType: "text/markdown",
          content: "端口配置见规范 [[knowledge:ev_aaa_1]]。",
        }}
        knowledgeReads={sampleReads}
      />,
    );
    expect(container.textContent).not.toContain("[[knowledge:");
    expect(
      screen.getByRole("button", { name: "引用 [1] 部署规范.md" }),
    ).toBeInTheDocument();
  });

  test("attempt filtering isolates empty retries, cross-KB keys don't collide, title upgrades, and legacy read.cited doesn't inflate", () => {
    const attempt1Only = [
      { id: "kr_old", attempt: 1, status: "hit", cited: true, evidences: [{ id: "ev_old", title: "Old.md", cited: true }] },
    ];
    expect(filterKnowledgeReadsForAttempt(attempt1Only, 2)).toEqual([]);
    expect(filterKnowledgeReadsForAttempt(attempt1Only, 1)).toHaveLength(1);

    const crossKBReads = [
      {
        id: "r_a",
        serverId: "srv-a",
        tool: "search_knowledge",
        status: "hit",
        evidences: [{ id: "ev_a", sourceId: "kb-1", documentId: "doc-1", locator: "#chunk-1", cited: true }],
      },
      {
        id: "r_a_read",
        serverId: "srv-a",
        tool: "read_document",
        status: "hit",
        evidences: [{ id: "ev_a_read", sourceId: "kb-1", documentId: "doc-1", title: "真实标题.md", locator: "#chunk-0..#chunk-4", cited: false }],
      },
      {
        id: "r_b",
        serverId: "srv-b",
        tool: "search_knowledge",
        status: "hit",
        evidences: [{ id: "ev_b", sourceId: "kb-2", documentId: "doc-1", title: "同名文档.md", locator: "#chunk-0", cited: false }],
      },
      {
        id: "r_legacy_multi",
        serverId: "srv-a",
        tool: "search_knowledge",
        query: "并发",
        status: "hit",
        cited: true,
        evidences: [
          { id: "ev_m1", sourceId: "kb-9", documentId: "m1", title: "候选一.md", cited: false },
          { id: "ev_m2", sourceId: "kb-9", documentId: "m2", title: "候选二.md", cited: false },
        ],
      },
    ];
    const summary = buildKnowledgeSummary(crossKBReads, [
      "结论一 [[knowledge:ev_a]]，结论二 [[knowledge:r_legacy_multi]]。",
    ]);
    expect(summary.referencedDocs.filter((d) => d.serverId === "srv-a" && d.documentId === "doc-1")).toHaveLength(1);
    const mergedDoc = summary.referencedDocs.find((d) => d.serverId === "srv-a" && d.documentId === "doc-1");
    expect(mergedDoc.label).toBe("真实标题.md");
    expect(mergedDoc.citedLocators).toEqual(["#chunk-1"]);
    expect(mergedDoc.locators).toEqual(["#chunk-1", "#chunk-0..#chunk-4"]);
    expect(summary.referencedDocs.some((d) => d.serverId === "srv-b" && d.documentId === "doc-1")).toBe(true);

    // Multi-evidence legacy call-level citation must NOT mark m1/m2 candidates as used.
    expect(summary.referencedDocs.find((d) => d.title === "候选一.md")?.used).toBe(false);
    expect(summary.referencedDocs.find((d) => d.title === "候选二.md")?.used).toBe(false);
    expect(summary.usedDocs).toHaveLength(2);
    expect(summary.byCiteId.get("r_legacy_multi")?.citeIndex).toBe(2);

    const fileNameSummary = buildKnowledgeSummary([
      {
        id: "r_fn_1",
        serverId: "agentflow-knowledge",
        tool: "read_document",
        status: "hit",
        evidences: [
          { id: "ev_fn_1", sourceName: "loopforge-knowledge", documentId: "d1", title: "我是谁", fileName: "我是谁.md", locator: "#chunk-0", cited: true },
          { id: "ev_fn_2", sourceName: "loopforge-knowledge", documentId: "d2", title: "部署规范", fileName: "deploy.pdf", locator: "#chunk-1", cited: false },
        ],
      },
    ], ["见 [[knowledge:ev_fn_1]]"]);
    expect(fileNameSummary.usedDocs[0].label).toBe("我是谁.md");
    expect(fileNameSummary.referencedDocs[1].label).toBe("部署规范 (deploy.pdf)");

    // Adjacent markers for the same document collapse to one [1], while distinct docs keep [1][2].
    const dedupReads = [
      {
        id: "r_s",
        serverId: "agentflow-knowledge",
        tool: "search_knowledge",
        status: "hit",
        evidences: [
          { id: "ev_s1", sourceId: "kb1", sourceName: "库一", documentId: "doc1", title: "我是谁", fileName: "我是谁.md", cited: true },
          { id: "ev_s2", sourceId: "kb2", sourceName: "库二", documentId: "doc2", title: "我是谁", fileName: "我是谁.md", cited: true },
        ],
      },
      {
        id: "r_r",
        serverId: "agentflow-knowledge",
        tool: "read_document",
        status: "hit",
        evidences: [
          { id: "ev_r1", sourceId: "kb1", sourceName: "库一", documentId: "doc1", title: "我是谁", fileName: "我是谁.md", cited: true },
        ],
      },
    ];
    const multiText = "同文档重复 [[knowledge:ev_s1]][[knowledge:ev_r1]]，多文档综合 [[knowledge:ev_s1]][[knowledge:ev_s2]]。";
    const { container: dedupContainer } = render(
      <RichContent message={multiText} knowledgeReads={dedupReads} />,
    );
    const supButtons = [...dedupContainer.querySelectorAll("button.knowledge-cite-sup")].map((b) => b.textContent);
    expect(supButtons).toEqual(["1", "1", "2"]);
    const copiedMulti = formatTextForCopy(multiText, dedupReads, [multiText]);
    expect(copiedMulti).toContain("同文档重复 [1]，多文档综合 [1][2]。");
  });

  test("ignores markers inside code blocks/inline code and formats clean copy text & streaming mask", () => {
    const textWithCode =
      "示例 ```[[knowledge:ev_aaa_2]]```\n\n```markdown\n[[knowledge:ev_aaa_2]]\n```\n\n正文引用 [[knowledge:ev_aaa_1]]。";
    expect(extractCitedIdsInOrder([textWithCode])).toEqual(["ev_aaa_1"]);
    const copied = formatTextForCopy(textWithCode, sampleReads, [textWithCode]);
    expect(copied).toContain("示例 ```[[knowledge:ev_aaa_2]]```");
    expect(copied).toContain("正文引用 [1]。");
    expect(copied).toContain("参考来源：\n[1] 部署规范.md（研发规范库 · #chunk-2）");

    expect(maskIncompleteStreamingCitation("答案进行中 [[knowledge:ev_1")).toBe("答案进行中 ");
    expect(maskIncompleteStreamingCitation("```js\nconst a = '[[knowledge:ev_1'")).toBe(
      "```js\nconst a = '[[knowledge:ev_1'",
    );

    // Non-KB message with literal #knowledge-cite link does not render a fake cite button.
    const { container } = render(
      <RichContent message="普通链接 [点击](#knowledge-cite:fake) 测试" />,
    );
    expect(container.querySelector(".knowledge-cite-sup")).toBeNull();
    expect(screen.getByRole("link", { name: "点击" })).toBeInTheDocument();
  });

  test("KnowledgeSources renders allContentFailed and hasUnstructuredOnly states & ArtifactPreview scopes footer and copy", () => {
    const { rerender } = render(
      <KnowledgeSources
        knowledgeReads={[{ id: "kr_err", serverId: "srv", tool: "search_knowledge", status: "unavailable" }]}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /知识库来源/ }));
    expect(screen.getByText("知识库检索调用失败，未能获取可用文档")).toBeInTheDocument();

    rerender(
      <KnowledgeSources
        knowledgeReads={[{ id: "kr_unstruct", serverId: "ext", tool: "custom_search", status: "unstructured" }]}
      />,
    );
    expect(screen.getByText("已检索到非结构化内容，未显式标注引用文档")).toBeInTheDocument();

    const { container } = render(
      <ArtifactPreview
        artifact={{
          id: "art-scoped",
          name: "scoped.md",
          attempt: 1,
          mimeType: "text/markdown",
          content: "仅引用第二条 [[knowledge:ev_aaa_1]]。",
        }}
        knowledgeReads={[
          {
            id: "kr_s",
            attempt: 1,
            serverId: "agentflow-knowledge",
            tool: "search_knowledge",
            status: "命中",
            cited: true,
            evidences: [
              { id: "ev_other_art", documentId: "d-other", title: "其他文档.md", cited: true },
              { id: "ev_aaa_1", documentId: "wk::doc1", sourceName: "研发规范库", title: "部署规范.md", locator: "#chunk-2", cited: true },
            ],
          },
          { id: "kr_retry", attempt: 2, tool: "search_knowledge", status: "empty" },
        ]}
      />,
    );
    expect(screen.getByText("产物引用来源")).toBeInTheDocument();
    expect(screen.getByText("实际使用 1 篇 · 检索读取 2 篇")).toBeInTheDocument();
    expect(container.querySelectorAll(".knowledge-doc-row.is-used-row")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "引用 [1] 部署规范.md" }));
    expect(container.querySelectorAll(".knowledge-doc-row.is-used-row")).toHaveLength(1);
    expect(container.querySelector(".knowledge-doc-row.is-used-row")).toHaveTextContent("部署规范.md");
    expect(screen.getByText("仅检索未采用")).toBeInTheDocument();
    expect(container.querySelector('.knowledge-doc-row[data-cite-index="1"]')).toHaveClass("is-highlighted");
    const copyBtn = screen.getByRole("button", { name: "复制正文" });
    expect(copyBtn).toBeInTheDocument();
  });

  test("ReplyTimeline narration, SopRunPanel live citations, and activityFace/activityTarget work", async () => {
    expect(activityFace({ kind: "tool", tool: "mcp__knowledge__kb_fetch" }).kind).toBe("read");
    expect(activityFace({ kind: "tool", tool: "mcp__knowledge__search_knowledge" }).kind).toBe("search");
    expect(
      activityTarget({ kind: "tool", tool: "mcp__knowledge__read_document", detail: '{"knowledge_id":"wk::doc-xyz"}' }),
    ).toBe("wk::doc-xyz");

    const summary = buildKnowledgeSummary(sampleReads, ["先查到规范 [[knowledge:ev_aaa_1]]"]);
    render(
      <ReplyTimeline
        steps={[
          { id: "s1", kind: "text", text: "先查到规范 [[knowledge:ev_aaa_1]]" },
          { id: "s2", kind: "tool", tool: "search_knowledge", finishedAt: "2026-01-01T00:00:01Z" },
        ]}
        live
        knowledgeReads={sampleReads}
        knowledgeSummary={summary}
      />,
    );
    expect(screen.getByRole("button", { name: "引用 [1] 部署规范.md" })).toBeInTheDocument();

    const origFetch = globalThis.fetch;
    globalThis.fetch = vi.fn(async () => ({
      ok: true,
      json: async () => ({
        events: [
          { sequence: 1, nodeId: "n1", category: "output", phase: "finish", message: "草稿已引用 [[knowledge:ev_live_1]]" },
        ],
        hasMore: false,
        nextAfter: 1,
      }),
    }));
    try {
      const liveRun = {
        id: "run-live-1",
        status: "running",
        workflowSnapshot: { name: "Demo", nodes: [{ id: "n1", name: "Node 1", type: "agent" }], edges: [] },
        nodes: {
          n1: {
            nodeId: "n1",
            status: "running",
            attempt: 1,
            knowledgeReads: [
              {
                id: "kr_live",
                attempt: 1,
                serverId: "agentflow-knowledge",
                tool: "search_knowledge",
                status: "hit",
                evidences: [
                  { id: "ev_live_1", documentId: "d-live", title: "实时规范.md", locator: "#chunk-0", cited: false },
                ],
              },
            ],
          },
        },
        artifacts: [],
      };
      render(<SopRunPanel runs={[liveRun]} embedded selectedNodeId="n1" onSelectNode={() => {}} />);
      await waitFor(() => {
        expect(screen.getByRole("button", { name: "引用 [1] 实时规范.md" })).toBeInTheDocument();
      });
    } finally {
      globalThis.fetch = origFetch;
    }
  });

  test("KnowledgeSources honors backend readFull flag for custom MCP read tools like get_content", () => {
    const tencentReads = [
      {
        id: "kr_td_read",
        serverId: "kb_53e7dad4dd0e",
        tool: "get_content",
        status: "命中",
        readFull: true,
        cited: true,
        evidences: [
          {
            id: "ev_td_1",
            documentId: "doc_td_123",
            fileName: "研发发布流程规范.docx",
            cited: true,
          },
        ],
      },
    ];
    const summary = buildKnowledgeSummary(tencentReads, ["见 [[knowledge:ev_td_1]]"]);
    expect(summary.usedDocs).toHaveLength(1);
    expect(summary.usedDocs[0].label).toBe("研发发布流程规范.docx");
    expect(summary.usedDocs[0].readFull).toBe(true);
  });
});
