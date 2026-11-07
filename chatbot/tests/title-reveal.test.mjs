import { test } from "vitest";
import assert from "node:assert/strict";
import { titleRevealTiming, shouldRevealTitle } from "../src/title-reveal.js";

test("a short title keeps the 16ms cadence and finishes within half a second", () => {
  const timing = titleRevealTiming("排查登录接口偶发五百错误");
  assert.equal(timing.chars.length, 12);
  assert.equal(timing.step, 16);
  assert.equal(timing.fade, 260);
  assert.equal(timing.total, 436);
});

test("long titles compress the cadence but never below 10ms per character", () => {
  const long = titleRevealTiming("字".repeat(52));
  assert.ok(long.step < 16);
  assert.ok(long.total <= 900);
  const huge = titleRevealTiming("字".repeat(300));
  assert.equal(huge.step, 10);
});

test("titles split by code point so emoji and full-width marks stay whole and spaces survive", () => {
  const { chars } = titleRevealTiming("周报 整理 📝✨（一）");
  assert.deepEqual(chars, ["周", "报", " ", "整", "理", " ", "📝", "✨", "（", "一", "）"]);
});

test("an empty title reveals the placeholder", () => {
  assert.equal(titleRevealTiming("").chars.join(""), "新对话");
  assert.equal(titleRevealTiming(undefined).chars.join(""), "新对话");
  assert.equal(titleRevealTiming("一").total, 260);
});

test("only a newly switched-to conversation reveals its title", () => {
  assert.equal(shouldRevealTitle({ sessionId: "b", revealId: "b", lastRevealedId: "a" }), true);
  assert.equal(shouldRevealTitle({ sessionId: "b", revealId: "b", lastRevealedId: null }), true);
  // Same conversation re-rendering after a rename or AI title.
  assert.equal(shouldRevealTitle({ sessionId: "b", revealId: "b", lastRevealedId: "b" }), false);
  // A conversation just created by sending from the home page was never opened.
  assert.equal(shouldRevealTitle({ sessionId: "new", revealId: "a", lastRevealedId: null }), false);
  assert.equal(shouldRevealTitle({ sessionId: null, revealId: null, lastRevealedId: null }), false);
  assert.equal(
    shouldRevealTitle({ sessionId: "b", revealId: "b", lastRevealedId: "a", reducedMotion: true }),
    false,
  );
});
