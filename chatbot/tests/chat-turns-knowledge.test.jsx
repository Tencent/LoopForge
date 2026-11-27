import React from "react";
import { fireEvent, render } from "@testing-library/react";
import { describe, expect, test } from "vitest";
import { AssistantTurn, replyText } from "../src/ChatTurns.jsx";
import { runArtifact } from "../src/sop-review.js";

const reads = [
  {
    id: "kr_search_1",
    serverId: "agentflow-knowledge",
    tool: "search_knowledge",
    query: "发布规范",
    status: "命中",
    cited: true,
    evidences: [
      {
        id: "ev_release_1",
        sourceId: "kb::team",
        sourceName: "团队手册",
        documentId: "kb::doc-release",
        title: "发布规范.md",
        locator: "#chunk-0",
        cited: true,
      },
    ],
  },
];

const reply = (message) => ({
  role: "assistant",
  content: JSON.stringify({ message }),
  knowledgeReads: reads,
});

describe("chat turns keep knowledge citations after the extraction", () => {
  test("replyText copies citations as numbered references instead of raw markers", () => {
    const copied = replyText(reply("灰度比例 5%。[[knowledge:ev_release_1]]"), []);
    expect(copied).not.toContain("[[knowledge:");
    expect(copied).toContain("灰度比例 5%。[1]");
    expect(copied).toContain("参考来源：\n[1] 发布规范.md");
  });

  test("replyText without knowledge reads keeps the plain reply", () => {
    expect(replyText({ role: "assistant", content: JSON.stringify({ message: "好的" }) }, [])).toBe("好的");
  });

  test("AssistantTurn renders superscripts and highlights the matching source", () => {
    const { container } = render(
      <AssistantTurn message={reply("灰度比例 5%。[[knowledge:ev_release_1]]")} steps={[]} />,
    );
    const sup = container.querySelector(".knowledge-cite-sup:not(.is-unresolved)");
    expect(sup).not.toBeNull();
    expect(container.textContent).not.toContain("[[knowledge:");
    expect(container.querySelector(".knowledge-sources")).not.toBeNull();
    fireEvent.click(sup);
    expect(
      container.querySelector('.knowledge-doc-row.is-highlighted[data-cite-index="1"]'),
    ).not.toBeNull();
  });
});

describe("run review artifacts", () => {
  test("runArtifact carries the producing node's knowledge reads", () => {
    const run = { id: "run-1", nodes: { write: { knowledgeReads: reads } } };
    const artifact = runArtifact(run, {
      id: "art-1",
      nodeId: "write",
      attempt: 1,
      path: "out/release.md",
      mediaType: "text/markdown",
    });
    expect(artifact.knowledgeReads).toBe(reads);
    expect(runArtifact({ id: "run-1", nodes: {} }, { id: "a", nodeId: "x", path: "a.csv" }).knowledgeReads).toEqual([]);
  });
});
