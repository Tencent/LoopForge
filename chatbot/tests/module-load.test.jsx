import { describe, expect, test, vi } from "vitest";

vi.mock("mermaid", () => ({
  default: {
    initialize: vi.fn(),
    render: vi.fn(async () => ({ svg: "<svg></svg>" })),
  },
}));

vi.mock("pptxgenjs", () => ({
  default: class {
    addSlide() {
      return { addText: vi.fn(), addImage: vi.fn(), background: {} };
    }
    writeFile = vi.fn();
  },
}));

describe("source modules", () => {
  test("all browser modules load", async () => {
    const modules = await Promise.all([
      import("../src/ActivityTrace.jsx"),
      import("../src/App.jsx"),
      import("../src/ArtifactPanel.jsx"),
      import("../src/ConversationStatusIcon.jsx"),
      import("../src/HarnessIcon.jsx"),
      import("../src/HomeLogo.jsx"),
      import("../src/HomeWelcome.jsx"),
      import("../src/IssueContext.jsx"),
      import("../src/MentionPicker.jsx"),
      import("../src/ModelPicker.jsx"),
      import("../src/ModelProviderIcon.jsx"),
      import("../src/Participants.jsx"),
      import("../src/PermissionPicker.jsx"),
      import("../src/QuickAccessWizard.jsx"),
      import("../src/RichContent.jsx"),
      import("../src/RunAlerts.jsx"),
      import("../src/SessionContextMenu.jsx"),
      import("../src/SidePanel.jsx"),
      import("../src/SidebarPreview.jsx"),
      import("../src/SlidingTabs.jsx"),
      import("../src/SopRunPanel.jsx"),
      import("../src/ui.jsx"),
      import("../src/useArtifactPanelLayout.js"),
      import("../src/useChat.js"),
      import("../src/useLocalConversations.js"),
      import("../src/usePanelPresence.js"),
    ]);
    expect(modules).toHaveLength(26);
    expect(modules.every(Boolean)).toBe(true);
  });
});
