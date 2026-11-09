import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

vi.mock("../src/api.js", async (original) => ({
  ...(await original()),
  write: vi.fn(async () => ({ ok: true, recorded: true })),
}));

import { write } from "../src/api.js";
import { unreadReply, useReadReplies } from "../src/session-unread.js";
import { sessionReplyStatus, sidebarReplyStatus } from "../src/session-status.js";

const row = (id, generationId, extra = {}) => ({
  id,
  generationId,
  generationStatus: "completed",
  unread: true,
  ...extra,
});

describe("unread replies", () => {
  test("a reply the server counts as unread keeps its dot until it is opened", () => {
    const session = row("a", "g2");
    expect(unreadReply(session)).toBe(true);
    expect(unreadReply(session, { activeId: "a" })).toBe(false);
    expect(unreadReply(session, { read: { a: "g2" } })).toBe(false);
    // A newer reply than the one read on this page is unread again.
    expect(unreadReply(session, { read: { a: "g1" } })).toBe(true);
    expect(unreadReply(row("b", "g3", { unread: false }))).toBe(false);
  });

  test("the sidebar swaps a completed reply for a blue dot only while it is unread", () => {
    const completed = sessionReplyStatus({ id: "a", generationStatus: "completed" });
    expect(sidebarReplyStatus(completed, false)).toBe(completed);
    expect(sidebarReplyStatus(completed, true)).toMatchObject({ state: "unread", dot: true });
    expect(completed.icon).toBeUndefined();
    const running = sessionReplyStatus({ id: "a", generationStatus: "running" });
    expect(sidebarReplyStatus(running, true)).toBe(running);
    expect(running).toMatchObject({ icon: "arc-spinner", busy: true });
    const failed = sessionReplyStatus({ id: "a", generationStatus: "failed" });
    expect(sidebarReplyStatus(failed, true)).toBe(failed);
    expect(failed.icon).toBe("alert");
  });
});

describe("useReadReplies", () => {
  beforeEach(() => write.mockClear());

  test("opening a conversation reports its unread reply once and clears the dot", async () => {
    const sessions = [row("a", "g1"), row("b", "g2")];
    const { result, rerender } = renderHook((props) => useReadReplies(props), {
      initialProps: { enabled: true, sessions, activeId: null },
    });
    expect(write).not.toHaveBeenCalled();
    rerender({ enabled: true, sessions, activeId: "b" });
    await waitFor(() => expect(result.current).toEqual({ b: "g2" }));
    expect(write).toHaveBeenCalledWith("/api/assistant/sessions/b/read", { generationId: "g2" });
    rerender({ enabled: true, sessions: [...sessions], activeId: "b" });
    expect(write).toHaveBeenCalledTimes(1);
    expect(unreadReply(sessions[1], { read: result.current })).toBe(false);
    expect(unreadReply(sessions[0], { read: result.current })).toBe(true);
  });

  test("a reply that completes while its conversation is open is read", async () => {
    const sessions = [row("a", "g1", { unread: false })];
    const running = { id: "g2", sessionId: "a", status: "running" };
    const { result, rerender } = renderHook((props) => useReadReplies(props), {
      initialProps: { enabled: true, sessions, activeId: "a", generation: running },
    });
    expect(write).not.toHaveBeenCalled();
    rerender({ enabled: true, sessions, activeId: "a", generation: { ...running, status: "completed" } });
    await waitFor(() => expect(result.current).toEqual({ a: "g2" }));
    expect(write).toHaveBeenCalledWith("/api/assistant/sessions/a/read", { generationId: "g2" });
  });

  test("failed replies, another conversation's reply and a page still loading report nothing", () => {
    const failed = [row("a", "g1", { unread: false, generationStatus: "failed" })];
    renderHook(() =>
      useReadReplies({ sessions: failed, activeId: "a", generation: { id: "g1", sessionId: "a", status: "failed" } }),
    );
    renderHook(() =>
      useReadReplies({ sessions: failed, activeId: "a", generation: { id: "g9", sessionId: "b", status: "completed" } }),
    );
    renderHook(() => useReadReplies({ enabled: false, sessions: [row("a", "g1")], activeId: "a" }));
    expect(write).not.toHaveBeenCalled();
  });

  test("a report that failed is sent again the next time the conversation is opened", async () => {
    write.mockRejectedValueOnce(new Error("offline"));
    const sessions = [row("a", "g1")];
    const { rerender } = renderHook((props) => useReadReplies(props), {
      initialProps: { sessions, activeId: "a" },
    });
    await waitFor(() => expect(write).toHaveBeenCalledTimes(1));
    await Promise.resolve();
    rerender({ sessions, activeId: null });
    rerender({ sessions, activeId: "a" });
    await waitFor(() => expect(write).toHaveBeenCalledTimes(2));
  });
});
