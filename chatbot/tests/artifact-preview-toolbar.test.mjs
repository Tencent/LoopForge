// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const styles = readFileSync(
  new URL("../src/artifact-preview.css", import.meta.url),
  "utf8",
);
const [desktop, mobile] = styles.split("@media (max-width: 767px)");

function rule(selector, source) {
  const escaped = selector
    .replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
    .replace(/\s+/g, "\\s+");
  const match = source.match(
    new RegExp(`(?:^|\\n|\\})\\s*${escaped}\\s*\\{([^}]*)\\}`),
  );
  assert.ok(match, `missing rule for ${selector}`);
  return match[1];
}

const tabs =
  ".artifact-preview .artifact-preview-view-options.sliding-tabs[data-sliding-tabs]";
const tab = `${tabs} > [data-sliding-tab]`;

function px(body, property) {
  const match = body.match(new RegExp(`(?:^|[;\\s])${property}:\\s*(\\d+)px`));
  assert.ok(match, `missing ${property}`);
  return Number(match[1]);
}

test("the preview/source switch matches the height of the icon actions beside it", () => {
  const icon = rule(".artifact-preview .artifact-preview-icon-action", desktop);
  assert.equal(px(rule(tabs, desktop), "height"), px(icon, "height"));
  assert.equal(px(rule(tabs, desktop), "min-height"), px(icon, "height"));
});

test("the preview/source switch uses compact text instead of the 14px page tabs", () => {
  const body = rule(tab, desktop);
  assert.ok(px(body, "font-size") <= 13);
  assert.ok(px(body, "height") < px(rule(tabs, desktop), "height"));
  assert.match(body, /flex:\s*0 0 auto/);
});

test("the switch sizes to its labels rather than stretching across the toolbar", () => {
  assert.match(rule(tabs, desktop), /display:\s*inline-flex/);
});

test("on narrow screens the switch grows with the 36px touch targets", () => {
  const icon = rule(".artifact-preview .artifact-preview-icon-action", mobile);
  assert.equal(px(rule(tabs, mobile), "height"), px(icon, "height"));
  assert.ok(px(rule(tab, mobile), "height") < px(icon, "height"));
});
