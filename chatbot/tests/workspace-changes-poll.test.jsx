import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";

import { useWorkspaceChanges } from "../src/useWorkspaceChanges.js";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

afterEach(() => {
  vi.unstubAllGlobals();
  delete document.hidden;
});

test("a hidden tab stops polling so the workspace can still go idle", async () => {
  let hidden = false;
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
  const fetchMock = vi.fn(async (path) => ({
    ok: true,
    status: 200,
    json: async () =>
      path.endsWith("/diff") ? { diff: "" } : { available: true, changes: [] },
  }));
  vi.stubGlobal("fetch", fetchMock);

  renderHook(() => useWorkspaceChanges("r1", { active: true, interval: 20 }));
  await waitFor(() =>
    expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(4),
  );

  hidden = true;
  await sleep(40);
  const settled = fetchMock.mock.calls.length;
  await sleep(150);
  expect(fetchMock.mock.calls.length).toBe(settled);

  hidden = false;
  await waitFor(() =>
    expect(fetchMock.mock.calls.length).toBeGreaterThan(settled),
  );
});

test("switching workspaces clears the previous file list immediately", async () => {
  let resolveSecond;
  const second = new Promise((resolve) => { resolveSecond = resolve; });
  const response = (data) => ({ ok: true, status: 200, json: async () => data });
  vi.stubGlobal("fetch", vi.fn(async (path) => {
    if (path.endsWith("/diff")) return response({ diff: "" });
    if (path.includes("/r1/")) {
      return response({ available: true, changes: [{ path: "old.txt", worktree: "M" }] });
    }
    return second;
  }));

  const { result, rerender } = renderHook(
    ({ id }) => useWorkspaceChanges(id),
    { initialProps: { id: "r1" } },
  );
  await waitFor(() => expect(result.current.files[0]?.path).toBe("old.txt"));

  rerender({ id: "r2" });
  expect(result.current.status).toBe("loading");
  expect(result.current.files).toEqual([]);
  await act(async () => resolveSecond(
    response({ available: true, changes: [{ path: "new.txt", worktree: "M" }] }),
  ));
  expect(result.current.files.map(({ path }) => path)).toEqual(["new.txt"]);
});
