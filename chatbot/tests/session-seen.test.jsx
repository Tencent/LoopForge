import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, test } from "vitest";
import { SEEN_SINCE, useSessionSeen } from "../src/useSessionSeen.js";

const key = "agentflow.native-chat.seen.7";

describe("useSessionSeen", () => {
  beforeEach(() => localStorage.clear());

  test("the first visit records a baseline instead of an empty history", () => {
    const before = Date.now();
    const { result } = renderHook(() => useSessionSeen(7));
    const since = Date.parse(result.current.seen[SEEN_SINCE]);
    expect(since).toBeGreaterThanOrEqual(before);
    expect(JSON.parse(localStorage.getItem(key))[SEEN_SINCE]).toBe(result.current.seen[SEEN_SINCE]);
  });

  test("an existing record keeps its entries and gains a baseline once", () => {
    localStorage.setItem(key, JSON.stringify({ "chat-a": "2026-11-01T00:00:00Z" }));
    const { result, rerender } = renderHook(() => useSessionSeen(7));
    const since = result.current.seen[SEEN_SINCE];
    expect(result.current.seen["chat-a"]).toBe("2026-11-01T00:00:00Z");
    expect(since).toBeTruthy();
    rerender();
    expect(JSON.parse(localStorage.getItem(key))[SEEN_SINCE]).toBe(since);
  });

  test("pruning old entries keeps the baseline", () => {
    const old = Object.fromEntries(
      Array.from({ length: 320 }, (_, i) => [`chat-${i}`, new Date(Date.UTC(2026, 0, 1, 0, i)).toISOString()]),
    );
    localStorage.setItem(key, JSON.stringify({ ...old, [SEEN_SINCE]: "2026-10-01T00:00:00Z" }));
    const { result } = renderHook(() => useSessionSeen(7));
    act(() => result.current.markSeen("chat-new", "2026-11-02T00:00:00Z"));
    const stored = JSON.parse(localStorage.getItem(key));
    expect(stored[SEEN_SINCE]).toBe("2026-10-01T00:00:00Z");
    expect(stored["chat-new"]).toBe("2026-11-02T00:00:00Z");
    expect(Object.keys(stored)).toHaveLength(301);
  });
});

describe("before the user is known", () => {
  beforeEach(() => localStorage.clear());
  test("nothing is written to storage", () => {
    const { result } = renderHook(() => useSessionSeen(undefined));
    expect(result.current.seen[SEEN_SINCE]).toBeTruthy();
    act(() => result.current.markSeen("chat-a", "2026-11-02T00:00:00Z"));
    expect(result.current.seen["chat-a"]).toBe("2026-11-02T00:00:00Z");
    expect(Object.keys(localStorage)).toHaveLength(0);
  });
});
