// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import {
  filterOptions,
  nextOptionIndex,
  normalizeOptions,
  placeSelectPanel,
  selectChangeEvent,
  selectedOption,
  showsSearch,
} from "../src/harness-select-model.js";

const src = (name) => readFileSync(new URL(`../src/${name}`, import.meta.url), "utf8");

test("normalizes option shapes and finds the selected one", () => {
  const options = normalizeOptions([{ value: "light", label: "浅色" }, "dark", { value: 3, label: "三", disabled: true, meta: 2 }]);
  assert.deepEqual(options, [
    { value: "light", label: "浅色", meta: "", disabled: false },
    { value: "dark", label: "dark", meta: "", disabled: false },
    { value: "3", label: "三", meta: "2", disabled: true },
  ]);
  assert.equal(selectedOption(options, "dark").label, "dark");
  assert.equal(selectedOption(options, 3).label, "三");
  assert.equal(selectedOption(options, "missing"), null);
});

test("change events carry target.value like the native select", () => {
  const event = selectChangeEvent("system");
  assert.equal(event.target.value, "system");
  assert.equal(event.currentTarget.value, "system");
  let seen = null;
  const handler = (e) => { seen = e.target.value; };
  handler(selectChangeEvent(42));
  assert.equal(seen, "42");
});

test("search shows above five options and filters by label, value and meta", () => {
  const few = normalizeOptions(["a", "b", "c", "d", "e"]);
  const many = normalizeOptions([...few, "f"]);
  assert.equal(showsSearch(few), false);
  assert.equal(showsSearch(many), true);
  assert.equal(showsSearch(few, true), true);
  const runs = normalizeOptions([{ value: "run-1", label: "需求澄清" }, { value: "run-2", label: "缺陷修复", meta: "失败" }]);
  assert.deepEqual(filterOptions(runs, "run-2").map((o) => o.value), ["run-2"]);
  assert.deepEqual(filterOptions(runs, "澄清").map((o) => o.value), ["run-1"]);
  assert.deepEqual(filterOptions(runs, "失败").map((o) => o.value), ["run-2"]);
  assert.equal(filterOptions(runs, "  ").length, 2);
});

test("keyboard movement skips disabled options and wraps", () => {
  const options = normalizeOptions([{ value: "a" }, { value: "b", disabled: true }, { value: "c" }]);
  assert.equal(nextOptionIndex(options, -1, "ArrowDown"), 0);
  assert.equal(nextOptionIndex(options, 0, "ArrowDown"), 2);
  assert.equal(nextOptionIndex(options, 2, "ArrowDown"), 0);
  assert.equal(nextOptionIndex(options, 0, "ArrowUp"), 2);
  assert.equal(nextOptionIndex(options, -1, "ArrowUp"), 2);
  assert.equal(nextOptionIndex(options, 2, "Home"), 0);
  assert.equal(nextOptionIndex(options, 0, "End"), 2);
  assert.equal(nextOptionIndex(normalizeOptions([{ value: "x", disabled: true }]), -1, "ArrowDown"), -1);
});

test("panel opens below, flips above near the bottom and stays in the viewport", () => {
  const viewport = { width: 1200, height: 800 };
  const anchor = (top, left = 100, width = 170) => ({ top, left, width, bottom: top + 34, height: 34, right: left + width });
  const below = placeSelectPanel(anchor(100), viewport, 200);
  assert.equal(below.placement, "bottom");
  assert.equal(below.top, 140);
  assert.equal(below.width, 240, "panel is at least 240px wide");
  const above = placeSelectPanel(anchor(700), viewport, 200);
  assert.equal(above.placement, "top");
  assert.equal(above.top, 700 - 6 - 200);
  const shifted = placeSelectPanel(anchor(100, 300, 170), { width: 320, height: 640 }, 200);
  assert.equal(shifted.width, 240);
  assert.equal(shifted.left, 72, "shifted left so the right edge keeps an 8px margin");
  const narrow = placeSelectPanel(anchor(100, 20, 150), { width: 200, height: 640 }, 200);
  assert.equal(narrow.width, 184, "never wider than the viewport minus margins");
  assert.equal(narrow.left, 8);
});

test("the workbench has no native select, and its checkboxes use the shared Harness styles", () => {
  for (const name of ["App.jsx", "SidePanel.jsx", "SopRunPanel.jsx"]) {
    assert.doesNotMatch(src(name), /<select[\s>]/, `${name} still renders a native <select>`);
    assert.match(src(name), /<HarnessSelect\b/, `${name} should render HarnessSelect`);
  }
  const main = src("main.jsx");
  assert.match(main, /web\/app\/hui-controls\.css/);
  assert.match(main, /web\/app\/hui-tooltip\.js/);
  assert.doesNotMatch(src("styles.css"), /accent-color/);
});

test("no native confirm / prompt / alert dialogs in the workbench source", () => {
  const files = readdirSync(new URL("../src/", import.meta.url)).filter((name) => /\.(jsx?|mjs)$/.test(name));
  for (const name of files) {
    assert.doesNotMatch(src(name), /(^|[^.\w])(window\.)?(confirm|prompt|alert)\(/m, `${name} calls a native dialog`);
  }
});
