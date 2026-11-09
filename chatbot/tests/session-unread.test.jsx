import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, test } from "vitest";
import {
  reconcileSeenReplies,
  seenRepliesKey,
  unreadReply,
  useSeenReplies,
} from "../src/session-unread.js";
import { sessionReplyStatus, sidebarReplyStatus } from "../src/session-status.js";

const row = (id, generationId, generationStatus = "completed", extra = {}) => ({
  id,
  userId: "u1",
  generationId,
  generationStatus,
  ...extra,
});

describe("seen replies", () => {
  test("the first visit treats every finished reply as read but leaves a running one to finish", () => {
    const seen = reconcileSeenReplies(null, [
      row("done", "g1"),
      row("failed", "g2", "failed"),
      row("busy", "g3", "running"),
      row("fresh", "", "idle"),
    ]);
    expect(seen).toEqual({ done: "g1", failed: "g2" });
  });

  test("the open conversation records its finished reply, preferring the live generation", () => {
    const sessions = [row("a", "g1"), row("b", "g2", "running")];
    expect(reconcileSeenReplies({}, sessions, { activeId: "a" })).toEqual({ a: "g1" });
    // The list still says g1 is the latest, but the open page already saw g4.
    expect(
      reconcileSeenReplies({}, sessions, {
        activeId: "a",
        generation: { id: "g4", status: "completed", sessionId: "a" },
      }),
    ).toEqual({ a: "g4" });
    // A reply still running is not read yet, and another session's generation is ignored.
    expect(reconcileSeenReplies({}, sessions, { activeId: "b" })).toEqual({});
    expect(
      reconcileSeenReplies({}, sessions, {
        activeId: "b",
        generation: { id: "g9", status: "completed", sessionId: "a" },
      }),
    ).toEqual({});
  });

  test("deleted conversations leave the record, and an empty list keeps it", () => {
    const stored = { kept: "g1", deleted: "g2" };
    expect(reconcileSeenReplies(stored, [row("kept", "g1")])).toEqual({ kept: "g1" });
    expect(reconcileSeenReplies(stored, [])).toEqual(stored);
  });

  test("a completed reply is unread until its conversation is opened", () => {
    const session = row("a", "g2");
    expect(unreadReply(session, { a: "g1" }, { userId: "u1" })).toBe(true);
    expect(unreadReply(session, {}, { userId: "u1" })).toBe(true);
    expect(unreadReply(session, { a: "g2" }, { userId: "u1" })).toBe(false);
    expect(unreadReply(session, { a: "g1" }, { userId: "u1", activeId: "a" })).toBe(false);
    // Nothing is unread before the record has loaded.
    expect(unreadReply(session, null, { userId: "u1" })).toBe(false);
  });

  test("only completed replies get the dot; running and failed ones keep their icons", () => {
    for (const status of ["running", "failed", "interrupted", "cancelled", "idle"])
      expect(unreadReply(row("a", "g2", status), {}, { userId: "u1" })).toBe(false);
  });

  test("a colleague's conversation the reader never joined stays quiet", () => {
    const colleague = row("c", "g1", "completed", {
      userId: "u2",
      participants: [{ id: "u2" }],
    });
    expect(unreadReply(colleague, {}, { userId: "u1" })).toBe(false);
    const joined = { ...colleague, participants: [{ id: "u2" }, { id: "u1" }] };
    expect(unreadReply(joined, {}, { userId: "u1" })).toBe(true);
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

describe("useSeenReplies", () => {
  afterEach(() => localStorage.clear());

  test("persists the record per user and clears a dot once the conversation is opened", () => {
    const key = seenRepliesKey("u1");
    const first = [row("a", "g1"), row("b", "g2", "running")];
    const { result, rerender } = renderHook((props) => useSeenReplies(props), {
      initialProps: { userId: "u1", sessions: first, enabled: true, activeId: "" },
    });
    expect(result.current).toEqual({ a: "g1" });
    expect(JSON.parse(localStorage.getItem(key))).toEqual({ a: "g1" });

    // b finished while the reader was elsewhere.
    const second = [row("a", "g1"), row("b", "g2")];
    rerender({ userId: "u1", sessions: second, enabled: true, activeId: "" });
    expect(unreadReply(second[1], result.current, { userId: "u1" })).toBe(true);

    act(() => rerender({ userId: "u1", sessions: second, enabled: true, activeId: "b" }));
    expect(result.current).toEqual({ a: "g1", b: "g2" });
    expect(JSON.parse(localStorage.getItem(key))).toEqual({ a: "g1", b: "g2" });
    expect(unreadReply(second[1], result.current, { userId: "u1" })).toBe(false);
  });

  test("waits for the list and ignores a broken stored record", () => {
    const { result, rerender } = renderHook((props) => useSeenReplies(props), {
      initialProps: { userId: "u1", sessions: [], enabled: false },
    });
    expect(result.current).toBeNull();
    localStorage.setItem(seenRepliesKey("u1"), "[1,2]");
    rerender({ userId: "u1", sessions: [row("a", "g1")], enabled: true });
    expect(result.current).toEqual({ a: "g1" });
  });
});
