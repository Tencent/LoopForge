import { renderHook, waitFor } from "@testing-library/react";
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
