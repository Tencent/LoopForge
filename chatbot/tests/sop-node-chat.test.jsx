import React from "react";
import { fireEvent, render, waitFor } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

// The node conversation shows the same compressed replies as the main chat, so
// expanding one has to ask the server for the stored message index — a partial
// tail view starts later in the transcript than the node view starts.
const loadActivity = vi.fn(() => Promise.resolve({ steps: [] }));
let session;

vi.mock("../src/useChat", () => ({
  useChat: () => ({
    ready: true,
    session,
    generation: {},
    live: { activity: [] },
    replyStatePending: false,
    activeId: null,
    readOnly: false,
    adoptCLIInstall: () => {},
    loadActivity,
  }),
}));

vi.mock("../src/sop-review", () => ({
  findNodeConversation: () => Promise.resolve(session),
  openNodeConversation: () => {},
}));

import SopNodeChat from "../src/SopNodeChat.jsx";

function nodeSession({ partial, offset }) {
  return {
    id: "sess-node",
    executor: "codebuddy",
    messagesPartial: partial,
    messagesOffset: offset,
    messages: [
      { role: "user", authorId: "bot", content: "开始执行" },
      { role: "assistant", content: "已完成", activityRef: "ref-1" },
    ],
  };
}

describe("node conversation activity", () => {
  test("a compressed reply loads its full trace by the stored index", async () => {
    session = nodeSession({ partial: true, offset: 100 });
    const { container } = render(<SopNodeChat run={{ id: "run-1", nodes: {} }} node={{ nodeId: "work" }} text="" onTextChange={() => {}} />);
    const trace = await waitFor(() => {
      const details = container.querySelector("details.execution-trace");
      expect(details).toBeTruthy();
      return details;
    });
    fireEvent.click(trace.querySelector("summary"));
    await waitFor(() => expect(loadActivity).toHaveBeenCalledWith("sess-node", 101, "ref-1"));
  });

  test("a complete transcript uses the position inside the session", async () => {
    loadActivity.mockClear();
    session = nodeSession({ partial: false, offset: 0 });
    const { container } = render(<SopNodeChat run={{ id: "run-1", nodes: {} }} node={{ nodeId: "work" }} text="" onTextChange={() => {}} />);
    const trace = await waitFor(() => {
      const details = container.querySelector("details.execution-trace");
      expect(details).toBeTruthy();
      return details;
    });
    fireEvent.click(trace.querySelector("summary"));
    await waitFor(() => expect(loadActivity).toHaveBeenCalledWith("sess-node", 1, "ref-1"));
  });
});
