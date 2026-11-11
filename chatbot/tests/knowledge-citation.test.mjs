import { test } from "vitest";
import assert from "node:assert/strict";
import {
  applyCitationMarks,
  citationHref,
  findCitations,
  remarkKnowledgeCitations,
} from "../src/knowledge-citation.jsx";

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
