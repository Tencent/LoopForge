import React from "react";
import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { collectArtifacts } from "../src/api.js";
import {
  cleanCodeFilePath,
  formatLineRange,
  highlightCodeNodes,
  inferLanguageFromPath,
  parseCodeBlockMeta,
  resolveHighlightLanguage,
  splitPathParts,
} from "../src/code-block.js";
import { CodeBlock, RichContent } from "../src/RichContent.jsx";

describe("code block reference parsing and syntax highlighting", () => {
  test("cleans internal workspace and sandbox path prefixes", () => {
    expect(
      cleanCodeFilePath(
        "/root/LoopForge-test/.data/conversations/shared/6f5756c4dfbf6116aaa00b4acc246f5fb9220ffb11ef2f53e254ac0910009961/ws/LoopForge/internal/domain/assistant.go",
      ),
    ).toBe("LoopForge/internal/domain/assistant.go");
    expect(
      cleanCodeFilePath("/root/.agentflow/assistant/9101/ws/src/main.go"),
    ).toBe("src/main.go");
    expect(
      cleanCodeFilePath("/data/runner/workspaces/run-123/web/app.js"),
    ).toBe("web/app.js");
    expect(cleanCodeFilePath("/workspace/chatbot/src/RichContent.jsx")).toBe(
      "chatbot/src/RichContent.jsx",
    );
    expect(cleanCodeFilePath("/etc/nginx/nginx.conf")).toBe(
      "/etc/nginx/nginx.conf",
    );
  });

  test("infers languages from file extensions and special filenames", () => {
    expect(inferLanguageFromPath("internal/httpapi/stream.go")).toBe("go");
    expect(inferLanguageFromPath("src/App.tsx")).toBe("tsx");
    expect(inferLanguageFromPath("deploy/Dockerfile.prod")).toBe("dockerfile");
    expect(inferLanguageFromPath("Makefile")).toBe("makefile");
    expect(inferLanguageFromPath("build/Makefile")).toBe("makefile");
    expect(inferLanguageFromPath("go.mod")).toBe("go");
    expect(inferLanguageFromPath("api/service.proto")).toBe("protobuf");
    expect(resolveHighlightLanguage("golang")).toBe("go");
    expect(resolveHighlightLanguage("vue")).toBe("xml");
    expect(resolveHighlightLanguage("plaintext")).toBe("");
    expect(resolveHighlightLanguage("unknown-lang-xyz")).toBe("");
  });

  test("parses various code reference and fence info formats", () => {
    expect(
      parseCodeBlockMeta(
        "968:1005:/root/LoopForge-test/.data/conversations/shared/abc/ws/LoopForge/internal/httpapi/stream.go",
      ),
    ).toEqual({
      language: "go",
      filePath: "LoopForge/internal/httpapi/stream.go",
      rawPath:
        "/root/LoopForge-test/.data/conversations/shared/abc/ws/LoopForge/internal/httpapi/stream.go",
      startLine: 968,
      endLine: 1005,
      isReference: true,
      visual: false,
    });

    expect(parseCodeBlockMeta("42:/workspace/src/app.py")).toEqual({
      language: "python",
      filePath: "src/app.py",
      rawPath: "/workspace/src/app.py",
      startLine: 42,
      endLine: null,
      isReference: true,
      visual: false,
    });

    expect(parseCodeBlockMeta("go", "10:20:internal/main.go")).toMatchObject({
      language: "go",
      filePath: "internal/main.go",
      startLine: 10,
      endLine: 20,
      isReference: true,
      visual: false,
    });

    expect(parseCodeBlockMeta("ts:src/App.tsx:5-12")).toMatchObject({
      language: "ts",
      filePath: "src/App.tsx",
      startLine: 5,
      endLine: 12,
      isReference: true,
      visual: false,
    });

    expect(parseCodeBlockMeta("web/index.html#L10-L25")).toMatchObject({
      language: "html",
      filePath: "web/index.html",
      startLine: 10,
      endLine: 25,
      isReference: true,
      visual: false,
    });

    expect(
      parseCodeBlockMeta("10:20:C:\\Users\\dev\\repo\\main.ts"),
    ).toMatchObject({
      language: "typescript",
      filePath: "C:\\Users\\dev\\repo\\main.ts",
      startLine: 10,
      endLine: 20,
      isReference: true,
    });

    expect(parseCodeBlockMeta("html", 'title="demo.html"')).toMatchObject({
      language: "html",
      filePath: "demo.html",
      isReference: false,
      visual: true,
    });

    expect(parseCodeBlockMeta("internal/httpapi/stream.go")).toMatchObject({
      language: "go",
      filePath: "internal/httpapi/stream.go",
      isReference: true,
      visual: false,
    });

    expect(parseCodeBlockMeta("dockerfile")).toEqual({
      language: "dockerfile",
      filePath: "",
      rawPath: "",
      startLine: null,
      endLine: null,
      isReference: false,
      visual: false,
    });
    expect(parseCodeBlockMeta("makefile")).toEqual({
      language: "makefile",
      filePath: "",
      rawPath: "",
      startLine: null,
      endLine: null,
      isReference: false,
      visual: false,
    });

    expect(splitPathParts("LoopForge/internal/httpapi/stream.go")).toEqual({
      dir: "LoopForge/internal/httpapi/",
      file: "stream.go",
    });
    expect(formatLineRange(21, 30)).toBe("21-30");
    expect(formatLineRange(21, 21)).toBe("21");
    expect(formatLineRange(null, null)).toBe("");
  });

  test("renders code references with clean header, copy button, stripped leading blank lines, and syntax highlighting", () => {
    const rawPath =
      "/root/LoopForge-test/.data/conversations/shared/6f5756c4dfbf6116aaa0ed/ws/LoopForge/internal/httpapi/stream.go";
    const md = [
      `\`\`\`968:1005:${rawPath}`,
      "",
      "func clarifyToolDetail(name string) string {",
      '  // comment line',
      '  return "ok"',
      "}",
      "```",
    ].join("\n");

    const { container } = render(<RichContent message={md} />);
    const title = container.querySelector(".code-toolbar-title");
    expect(title).toHaveAttribute("title", `${rawPath}:968-1005`);
    expect(container.querySelector(".code-toolbar-dir")?.textContent).toBe(
      "LoopForge/internal/httpapi/",
    );
    expect(container.querySelector(".code-toolbar-file")?.textContent).toBe(
      "stream.go",
    );
    expect(container.querySelector(".code-toolbar-lines")?.textContent).toBe(
      ":968-1005",
    );
    expect(container.querySelector(".code-toolbar-lang")?.textContent).toBe(
      "go",
    );
    expect(
      screen.getByRole("button", { name: "复制代码" }),
    ).toBeInTheDocument();

    const code = container.querySelector("pre code");
    expect(code).toHaveClass("hljs", "language-go");
    expect(code?.textContent).toBe(
      'func clarifyToolDetail(name string) string {\n  // comment line\n  return "ok"\n}',
    );
    expect(container.querySelectorAll(".hljs-keyword").length).toBeGreaterThan(
      0,
    );
    expect(container.querySelector(".hljs-title")?.textContent).toBe(
      "clarifyToolDetail",
    );
    expect(container.querySelector(".hljs-string")?.textContent).toBe('"ok"');
    expect(container.querySelector(".hljs-comment")?.textContent).toBe(
      "// comment line",
    );
  });

  test("renders cited HTML lines as highlighted source rather than an iframe preview", () => {
    const { container } = render(
      <RichContent
        message={"```1:3:/workspace/web/index.html\n<div class=\"app\">hi</div>\n```"}
      />,
    );
    expect(container.querySelector("iframe")).toBeNull();
    expect(container.querySelector("pre code")).toHaveClass("hljs", "language-xml");
    expect(container.querySelector(".hljs-tag")).not.toBeNull();
  });

  test("falls back cleanly for unknown languages and huge snippets", () => {
    expect(highlightCodeNodes("hello", "unknown-lang")).toBeNull();
    expect(highlightCodeNodes("a".repeat(100_001), "go")).toBeNull();
    const { container } = render(
      <CodeBlock language="custom-dsl" source={"alpha -> beta"} />,
    );
    expect(container.querySelector(".code-toolbar-title")?.textContent).toBe(
      "custom-dsl",
    );
    const code = container.querySelector("pre code");
    expect(code?.className).toBe("");
    expect(code?.textContent).toBe("alpha -> beta");
  });

  test("collectArtifacts parses code references into clean file names and source labels", () => {
    const artifacts = collectArtifacts({
      id: "s-ref",
      messages: [
        {
          role: "assistant",
          content: JSON.stringify({
            message: [
              "```968:1005:/root/LoopForge-test/.data/conversations/shared/abc/ws/LoopForge/internal/httpapi/stream.go",
              "func x() {}",
              "```",
              "```1:5:/workspace/web/index.html",
              "<h1>hello</h1>",
              "```",
            ].join("\n"),
          }),
        },
      ],
    });
    expect(artifacts).toHaveLength(2);
    expect(artifacts[0]).toMatchObject({
      name: "stream.go",
      kind: "code",
      language: "go",
      source: "代码引用 · LoopForge/internal/httpapi/stream.go:968-1005",
    });
    expect(artifacts[1]).toMatchObject({
      name: "index.html",
      kind: "code",
      language: "html",
      source: "代码引用 · web/index.html:1-5",
    });
  });
});
