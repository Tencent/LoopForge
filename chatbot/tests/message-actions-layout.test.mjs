// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");

function rule(selector, source = styles) {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = source.match(new RegExp(`(?:^|\\n)\\s*${escaped}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `missing rule for ${selector}`);
  return match[1];
}

test("the hover toolbar of a plain message still collapses into the gap below the bubble", () => {
  const actions = rule(".user-actions");
  assert.match(actions, /height:\s*25px/);
  assert.match(actions, /margin-bottom:\s*-25px/);
});

test("the hover toolbar paints above attachment cards", () => {
  const actions = rule(".user-actions");
  assert.match(actions, /position:\s*relative/);
  assert.match(actions, /z-index:\s*1/);
});

test("a message with attachments keeps the toolbar in flow so images cannot cover it", () => {
  const attached = rule(".message.user:has(.message-attachments) .user-actions");
  assert.match(attached, /margin-bottom:\s*0/);
});

test("attachments still start below the toolbar with their own top margin", () => {
  const attachments = rule(".message-attachments");
  assert.match(attachments, /margin:\s*15px 0/);
});
