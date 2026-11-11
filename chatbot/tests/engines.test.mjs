import { test } from "vitest";
import assert from "node:assert/strict";
import { conversationEngine } from "../src/engines.js";

const reply = [{ role: "user", content: "hi" }, { role: "assistant", model: "gpt-5.4", content: "ok" }];

test("an unprefixed reply stays on the session executor saved before model prefixes", () => {
  assert.equal(conversationEngine(reply, [], "codex"), "codex");
  assert.equal(conversationEngine(reply, [], "claude"), "claude-code");
  assert.equal(conversationEngine(reply, [], "agy"), "agy");
  assert.equal(conversationEngine(reply, [], "codebuddy"), "codebuddy");
  assert.equal(conversationEngine(reply), "codebuddy");
  assert.equal(conversationEngine([{ role: "assistant", model: "codex:gpt-5.4", content: "ok" }], [], "codebuddy"), "codex");
  assert.equal(conversationEngine([{ role: "assistant", model: "gpt-5.4", speakerAgentId: "role", content: "ok" }], [], "codex"), "");
});
