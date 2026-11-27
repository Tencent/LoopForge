import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";

import { useWorkspaceChanges } from "../src/useWorkspaceChanges.js";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

afterEach(() => {
  vi.unstubAllGlobals();
  delete document.hidden;
});

test("reads desktop IDE availability independently of the diff and scopes it to the current run", async () => {
  const pendingDiff = new Promise(() => {});
  vi.stubGlobal("fetch", vi.fn(async (path) => {
    if (path.endsWith("/diff")) return pendingDiff;
    return {
      ok: true,
      status: 200,
      json: async () => ({ available: true, desktopSSH: path.includes("/r1/"), changes: [] }),
    };
  }));

  const { result, rerender } = renderHook(({ runId }) => useWorkspaceChanges(runId), {
    initialProps: { runId: "r1" },
  });
  await waitFor(() => expect(result.current.desktopIDE?.available).toBe(true));
  expect(result.current.status).toBe("loading");

  rerender({ runId: "r2" });
  expect(result.current.desktopIDE).toBeNull();
  await waitFor(() => expect(result.current.desktopIDE?.available).toBe(false));
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
