import { test } from "vitest";
import assert from "node:assert/strict";
import {
  MENU_MAX_HEIGHT,
  anchorVisible,
  availableBounds,
  placeMenu,
} from "../src/menu-placement.js";

const viewport = { top: 0, left: 0, bottom: 800, right: 1200 };
const anchor = (top, left = 100, height = 28, width = 120) => ({
  top,
  left,
  bottom: top + height,
  right: left + width,
  height,
  width,
});

test("opens above when the menu fits above the trigger", () => {
  const p = placeMenu(anchor(600), viewport, 300, 300);
  assert.equal(p.side, "above");
  assert.equal(p.height, 300);
  assert.equal(p.top, 600 - 8 - 300);
  assert.equal(p.left, 100);
  assert.equal(p.width, 300);
});

test("flips below when above is too short and below has more room", () => {
  const p = placeMenu(anchor(150), viewport, 300, 300);
  assert.equal(p.side, "below");
  assert.equal(p.top, 150 + 28 + 8);
  assert.equal(p.height, 300);
});

test("stays above when above is too short but still larger than below", () => {
  const p = placeMenu(anchor(300), { ...viewport, bottom: 500 }, 600, 300);
  assert.equal(p.side, "above");
  assert.equal(p.height, 300 - 8 - 8);
  assert.equal(p.top, 8);
});

test("uses the larger side and caps the height when neither side fits", () => {
  const bounds = { ...viewport, bottom: 500 };
  const p = placeMenu(anchor(200), bounds, 600, 300);
  assert.equal(p.side, "below");
  assert.equal(p.height, 500 - 228 - 16);
  assert.equal(p.maxHeight, p.height);
  assert.ok(p.top + p.height <= bounds.bottom - 8);
});

test("never exceeds the height cap", () => {
  const p = placeMenu(anchor(780), { ...viewport, bottom: 1000 }, 1200, 300);
  assert.equal(p.side, "above");
  assert.equal(p.height, MENU_MAX_HEIGHT);
  assert.equal(p.maxHeight, MENU_MAX_HEIGHT);
});

test("clamps horizontally and shrinks to narrow viewports", () => {
  const right = placeMenu(anchor(600, 1100), viewport, 200, 300);
  assert.equal(right.left, 1200 - 300 - 8);
  const left = placeMenu(anchor(600, -40), viewport, 200, 300);
  assert.equal(left.left, 8);
  const narrow = placeMenu(
    anchor(600, 20),
    { ...viewport, right: 280 },
    200,
    300,
  );
  assert.equal(narrow.width, 280 - 16);
  assert.equal(narrow.left, 8);
});

test("keeps the previous side while searching", () => {
  const first = placeMenu(anchor(150), viewport, 400, 300);
  assert.equal(first.side, "below");
  const shorter = placeMenu(anchor(150), viewport, 60, 300, first.side);
  assert.equal(shorter.side, "below");
  assert.equal(shorter.height, 60);
  const fresh = placeMenu(anchor(150), viewport, 60, 300);
  assert.equal(fresh.side, "above");
});

test("clipping ancestors limit vertical room but not horizontal clamping", () => {
  const bounds = availableBounds(viewport, [
    { top: 120, left: 300, bottom: 760, right: 900 },
  ]);
  assert.deepEqual(bounds, { top: 120, left: 0, bottom: 760, right: 1200 });
  const p = placeMenu(anchor(260), bounds, 300, 300);
  assert.equal(p.side, "below", "the header area above the clip is unusable");
  assert.equal(p.top, 296);
});

test("detects a trigger scrolled out of the visible area", () => {
  const bounds = availableBounds(viewport, [
    { top: 100, left: 0, bottom: 700, right: 1200 },
  ]);
  assert.equal(anchorVisible(anchor(300), bounds), true);
  assert.equal(anchorVisible(anchor(40), bounds), false);
  assert.equal(anchorVisible(anchor(300, 100, 0, 0), bounds), false);
});
