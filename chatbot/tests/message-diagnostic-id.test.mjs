import { test } from "vitest";
import assert from "node:assert/strict";
import { messageDiagnosticId } from "../src/message-info.js";

test("the accepted generation ID is available on its own user message while it runs", () => {
  const messages = [
    { role: "user", content: "first question" },
    { role: "assistant", generationId: "first-turn" },
    { role: "user", content: "next question" },
  ];
  const generation = { id: "next-turn", sessionId: "chat", messageCount: 3 };

  assert.equal(messageDiagnosticId(messages, 0, generation, "chat"), "first-turn");
  assert.equal(messageDiagnosticId(messages, 1, generation, "chat"), "first-turn");
  assert.equal(messageDiagnosticId(messages, 2, generation, "chat"), "next-turn");
  assert.equal(messageDiagnosticId(messages, 2, generation, "other-chat"), "");
});

test("only an adjacent saved reply can identify a user message without a live generation", () => {
  const messages = [
    { role: "user", content: "cancelled question" },
    { role: "user", content: "new question" },
    { role: "assistant", generationId: "new-turn" },
  ];
  assert.equal(messageDiagnosticId(messages, 0), "");
  assert.equal(messageDiagnosticId(messages, 1), "new-turn");
});
