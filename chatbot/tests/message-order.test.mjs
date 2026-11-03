// @vitest-environment node
import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// "让 AI 分析" sends a question about a run alert. The reply streamed below
// the alert and then jumped above it on completion, so the alert looked
// unanswered and people asked again. The browser check is
// e2e/ui/02-interactions.spec.mjs; this keeps the order visible in CI.
test("the live reply renders before the run alerts", () => {
  const app = readFileSync(new URL("../src/App.jsx", import.meta.url), "utf8");
  const live = app.indexOf("<LiveMessage live={chat.live}");
  const alerts = app.indexOf("<RunAlerts");
  assert.ok(live > 0, "LiveMessage is rendered in the thread");
  assert.ok(alerts > 0, "RunAlerts is rendered in the thread");
  assert.ok(live < alerts, "LiveMessage must come before RunAlerts");
});
