// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const card = readFileSync(new URL("../src/native-approval.css", import.meta.url), "utf8");
const styles = readFileSync(new URL("../src/styles.css", import.meta.url), "utf8");
const navigation = readFileSync(new URL("../src/navigation.css", import.meta.url), "utf8");
const tokens = readFileSync(new URL("../src/hui-tokens.css", import.meta.url), "utf8");

const declarations = [...card.matchAll(/([a-z-]+)\s*:\s*([^;{}]+);/g)].map((m) => [
  m[1],
  m[2].trim(),
]);

test("the approval card carries no colour literal, so both themes stay consistent", () => {
  assert.doesNotMatch(card, /#[0-9a-fA-F]{3,8}/);
  assert.doesNotMatch(card, /\b(rgb|rgba|hsl|hsla)\(/);
});

test("every colour, surface and border of the card comes from a hui token", () => {
  const painted = declarations.filter(([property]) =>
    /^(color|background|background-color|border|border-color|border-top|border-left|outline|box-shadow|fill|stroke)$/.test(
      property,
    ),
  );
  assert.ok(painted.length >= 8, "the card still paints something");
  for (const [property, value] of painted) {
    const ok =
      /var\(--hui-/.test(value) || /^(transparent|none|inherit|currentColor|0)$/.test(value);
    assert.ok(ok, `${property}: ${value} must use a --hui-* token`);
  }
  // The card is a panel, not an accent block: no coloured edge or gradient.
  assert.doesNotMatch(card, /linear-gradient|border-left:\s*\d+px solid var\(--hui-(warn|error|accent)\)/);
});

test("the card file does not redeclare the shared tokens", () => {
  assert.doesNotMatch(card, /--hui-[a-z-]+\s*:/);
});

test("the waiting state is coloured in both themes, in the header and the sidebar", () => {
  assert.match(
    styles,
    /\.conversation-status\[data-conversation-status="waiting_approval"\]\s*\{\s*color:\s*#b7791f;/,
  );
  assert.match(
    styles,
    /\[data-theme="dark"\]\s*\.conversation-status\[data-conversation-status="waiting_approval"\]\s*\{\s*color:\s*#f2c46d;/,
  );
  assert.match(
    navigation,
    /\.chat-shell \.session-status\[data-session-status="waiting_approval"\]\s*\{\s*color:\s*#b7791f;/,
  );
  assert.match(
    navigation,
    /\[data-theme="dark"\] \.chat-shell \.session-status\[data-session-status="waiting_approval"\]\s*\{\s*color:\s*#f2c46d;/,
  );
  // The two literals are the amber token's values, not a new colour.
  assert.match(tokens, /--hui-warn:\s*#b7791f;/);
  assert.match(tokens, /--hui-warn:\s*#f2c46d;/);
});

test("waiting for a person never animates", () => {
  const block = styles.match(
    /\.conversation-status\[data-conversation-status="waiting_approval"\]\s*\{([^}]*)\}/,
  );
  assert.ok(block);
  assert.doesNotMatch(block[1], /animation/);
  const rowBlock = styles.match(/\.execution-step\[data-status="waiting_approval"\]\s*\{([^}]*)\}/);
  assert.ok(rowBlock);
  assert.doesNotMatch(rowBlock[1], /animation/);
});
