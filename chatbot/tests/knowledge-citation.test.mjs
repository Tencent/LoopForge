import { test, vi } from "vitest";
import assert from "node:assert/strict";
import { createElement } from "react";
import { renderHook, waitFor } from "@testing-library/react";
import {
  applyCitationMarks,
  citationHref,
  findCitations,
  KnowledgeProject,
  remarkKnowledgeCitations,
  useCitationInfo,
} from "../src/knowledge-citation.jsx";

test("external citations stay navigable without calling the builtin freshness API", async () => {
  const citation = "source@revision:docs/guide.md#L1";
  const fetch = vi.fn(async () => new Response(JSON.stringify({
    items: [{ citation, status: "changed" }],
  }), { headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetch);
  const renderCitation = (builtin) => renderHook(() => useCitationInfo(citation), {
    wrapper: ({ children }) => createElement(KnowledgeProject.Provider, {
      value: { projectId: "project", builtin },
    }, children),
  });
  try {
    const external = renderCitation(false);
    assert.equal(fetch.mock.calls.length, 0);
    assert.match(citationHref(citation, external.result.current), /^knowledge-citation:/);
    external.unmount();

    const builtin = renderCitation(true);
    await waitFor(() => assert.equal(builtin.result.current[citation]?.status, "changed"));
    assert.equal(fetch.mock.calls.length, 1);
    assert.match(String(fetch.mock.calls[0][0]), /\/api\/knowledge\/citations\/freshness$/);
    builtin.unmount();
  } finally {
    vi.unstubAllGlobals();
  }
});

test("citation marks compare the cited version without touching ordinary text", () => {
  const text = "见 docs@1:guides/a.md#L2-L4，以及 user@example.com 和 docs@2:guides/a.md#L2。";
  assert.deepEqual(findCitations(text), ["docs@1:guides/a.md#L2-L4", "docs@2:guides/a.md#L2"]);
  const marked = applyCitationMarks(text, {
    "docs@1:guides/a.md#L2-L4": "changed",
    "docs@2:guides/a.md#L2": "deleted",
  });
  assert.match(marked, /docs@1:guides\/a\.md#L2-L4（原文已变更）/);
  assert.match(marked, /docs@2:guides\/a\.md#L2（原文已删除）/);
  assert.match(marked, /user@example\.com/);
  assert.equal(applyCitationMarks(text, {}), text);
});

test("citations become links in text and inline code", () => {
  const hosted = "docs@1:guides/a.md#L2-L4";
  const git = "repo@0123456789abcdef0123456789abcdef01234567:docs/b.md#L7";
  const tree = {
    type: "root",
    children: [
      {
        type: "paragraph",
        children: [
          { type: "text", value: `见 ${hosted}，` },
          { type: "inlineCode", value: git },
        ],
      },
    ],
  };
  const info = {
    [hosted]: { kind: "hosted", status: "current" },
    [git]: { kind: "git", status: "current", url: "https://github.com/o/r/blob/abc/docs/b.md#L7" },
  };
  remarkKnowledgeCitations({ info })(tree);
  const children = tree.children[0].children;
  const hostedLink = children.find((node) => node.type === "link" && node.url.startsWith("knowledge-citation:"));
  const gitLink = children.find((node) => node.type === "link" && node.url.startsWith("https://"));
  assert.ok(hostedLink, "hosted citation should route to the knowledge center");
  assert.ok(gitLink, "git citation should open the host URL");
  assert.equal(gitLink.url, info[git].url);
  assert.equal(citationHref(git, info), info[git].url);
  assert.match(citationHref(hosted, info), /^knowledge-citation:/);
});

test("changed citations add the freshness note after the link", () => {
  const citation = "docs@1:guides/a.md#L2";
  const tree = {
    type: "root",
    children: [{ type: "paragraph", children: [{ type: "text", value: citation }] }],
  };
  remarkKnowledgeCitations({ info: { [citation]: { status: "changed" } } })(tree);
  const children = tree.children[0].children;
  assert.equal(children[0].type, "link");
  assert.equal(children[1].value, "（原文已变更）");
});
