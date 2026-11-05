import { test } from "vitest";
import assert from "node:assert/strict";
import {
  PANEL_ROOM_BREAKPOINT,
  isGeneratedArtifact,
  panelFitsBesideConversation,
  shouldRevealArtifactPanel,
} from "../src/artifact-autoreveal.js";

test("a generated artifact opens the closed artifact drawer on a wide layout", () => {
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: 1, panelOpen: false, viewportWidth: 1440 }),
    true,
  );
});

test("conversations without new artifacts keep the drawer as the user left it", () => {
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: 0, panelOpen: false, viewportWidth: 1440 }),
    false,
  );
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: -1, panelOpen: false, viewportWidth: 1440 }),
    false,
  );
});

test("an already open drawer is never re-opened or reset by a new artifact", () => {
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: 2, panelOpen: true, viewportWidth: 1440 }),
    false,
  );
});

test("the drawer does not cover the conversation below the layout breakpoint", () => {
  assert.equal(PANEL_ROOM_BREAKPOINT, 800);
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: 1, viewportWidth: 760 }),
    false,
  );
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: 1, viewportWidth: 800 }),
    true,
  );
});

test("the open navigation sidebar counts against the room for the drawer", () => {
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: 1, viewportWidth: 1100, sidebarOpen: true, sidebarWidth: 304 }),
    false,
  );
  assert.equal(
    shouldRevealArtifactPanel({ addedArtifacts: 1, viewportWidth: 1100, sidebarOpen: false, sidebarWidth: 304 }),
    true,
  );
  // A collapsed navigation sidebar releases its width even if a stale width is kept.
  assert.equal(panelFitsBesideConversation({ viewportWidth: 900, sidebarOpen: false, sidebarWidth: 304 }), true);
});

test("an unmeasured viewport never auto-reveals the drawer", () => {
  assert.equal(panelFitsBesideConversation({ viewportWidth: 0 }), false);
  assert.equal(panelFitsBesideConversation({ viewportWidth: "wide" }), false);
  assert.equal(shouldRevealArtifactPanel({ addedArtifacts: 1 }), false);
});

test("a file the user attached is not a generated artifact worth opening the drawer", () => {
  assert.equal(isGeneratedArtifact({ kind: "attachment", source: "AI 生成" }), true);
  assert.equal(isGeneratedArtifact({ kind: "attachment", source: "上传的文件" }), false);
  assert.equal(isGeneratedArtifact({ kind: "sop", source: "已保存" }), true);
  assert.equal(isGeneratedArtifact(null), false);
});
