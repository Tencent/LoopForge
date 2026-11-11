import { test } from "vitest";
import assert from "node:assert/strict";
import { applyCitationMarks, findCitations } from "../src/knowledge-citation.js";

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
