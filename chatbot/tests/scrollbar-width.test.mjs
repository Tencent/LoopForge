// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const fallbackQuery = "@supports not selector(::-webkit-scrollbar)";

function block(source, start) {
  let depth = 0;
  for (let i = source.indexOf("{", start); i < source.length; i += 1) {
    if (source[i] === "{") depth += 1;
    if (source[i] === "}" && --depth === 0) return source.slice(start, i + 1);
  }
  throw new Error(`unterminated block at ${start}`);
}

function fallbackBlocks() {
  const blocks = [];
  for (let at = styles.indexOf(fallbackQuery); at !== -1; at = styles.indexOf(fallbackQuery, at + 1)) {
    blocks.push(block(styles, at));
  }
  return blocks;
}

function rules(source) {
  return [...source.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(([, selector, body]) => ({
    selectors: selector.split(",").map((part) => part.trim()),
    body,
  }));
}

function sessionListRules(source) {
  return rules(source).filter(({ selectors }) =>
    selectors.some((selector) => selector.startsWith(".session-list") && !selector.includes("::")),
  );
}

function outsideFallback() {
  let outside = styles;
  for (const fallback of fallbackBlocks()) outside = outside.replace(fallback, "");
  return outside;
}

test(".session-list keeps scrollbar-width/color out of its base rules so Chrome uses the 4px webkit scrollbar", () => {
  const found = sessionListRules(outsideFallback());
  assert.ok(found.some(({ selectors }) => selectors.includes(".session-list")), "missing .session-list base rule");
  for (const { selectors, body } of found) {
    assert.doesNotMatch(body, /scrollbar-(?:width|color)/, `${selectors.join(", ")} must not set standard scrollbar properties`);
  }
});

test("the standard scrollbar properties only remain as a non-WebKit fallback", () => {
  const fallback = fallbackBlocks().find((source) => /\.session-list\s*\{/.test(source));
  assert.ok(fallback, `missing ${fallbackQuery} block for .session-list`);
  assert.match(fallback, /\.session-list\s*\{[^}]*scrollbar-width:\s*thin/);
  assert.match(fallback, /\.session-list:hover,\s*\.session-list:focus-within\s*\{[^}]*scrollbar-color:/);
});

test("the WebKit session-list scrollbar is 4px wide", () => {
  const match = styles.match(/\.session-list::-webkit-scrollbar\s*\{([^}]*)\}/);
  assert.ok(match, "missing .session-list::-webkit-scrollbar rule");
  assert.match(match[1], /width:\s*4px/);
});

test("hovering the session list toggles a property on the list itself so Chrome repaints the thumb", () => {
  const outside = rules(outsideFallback());
  const thumb = outside.find(({ selectors }) => selectors.includes(".session-list::-webkit-scrollbar-thumb"));
  assert.ok(thumb, "missing .session-list::-webkit-scrollbar-thumb rule");
  assert.match(thumb.body, /background-color:\s*var\(--session-scrollbar-thumb\)/);

  const base = outside.find(({ selectors }) => selectors.length === 1 && selectors[0] === ".session-list");
  assert.match(base.body, /--session-scrollbar-thumb:\s*transparent/);

  const active = outside.find(
    ({ selectors }) => selectors.includes(".session-list:hover") && selectors.includes(".session-list:focus-within"),
  );
  assert.ok(active, "missing .session-list:hover/:focus-within rule");
  assert.match(active.body, /--session-scrollbar-thumb:\s*color-mix\(in srgb, var\(--muted\) 22%, transparent\)/);

  for (const { selectors } of outside) {
    for (const selector of selectors) {
      assert.doesNotMatch(
        selector,
        /^\.session-list:(?:hover|focus-within)::-webkit-scrollbar-thumb$/,
        "Chrome does not repaint the thumb when only the pseudo-element rule depends on :hover",
      );
    }
  }
});
