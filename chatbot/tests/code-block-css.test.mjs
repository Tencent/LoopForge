// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const css = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");

test("styles.css prevents toolbar overflow and keeps copy action pinned", () => {
  assert.match(css, /\.code-block\s*\{[^}]*max-width:\s*100%/);
  assert.match(css, /\.code-toolbar\s*\{[^}]*min-width:\s*0/);
  assert.match(css, /\.code-toolbar-title\s*\{[^}]*min-width:\s*0/);
  assert.match(css, /\.code-toolbar-title\s*\{[^}]*overflow:\s*hidden/);
  assert.match(css, /\.code-toolbar-actions\s*\{[^}]*flex:\s*0 0 auto/);
});

test("styles.css defines syntax highlighting tokens for light and dark themes", () => {
  assert.match(css, /\.code-block\s+\.hljs-keyword/);
  assert.match(css, /\.code-block\s+\.hljs-string/);
  assert.match(css, /\.code-block\s+\.hljs-comment/);
  assert.match(css, /\[data-theme="dark"\]\s+\.code-block\s+\.hljs-keyword/);
  assert.match(css, /\[data-theme="dark"\]\s+\.code-block\s+\.hljs-string/);
  assert.match(css, /\[data-theme="dark"\]\s+\.code-block\s+\.hljs-comment/);
});
