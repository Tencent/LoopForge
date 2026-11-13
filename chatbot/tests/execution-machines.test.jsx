import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

const { apiMock, writeMock } = vi.hoisted(() => ({
  apiMock: vi.fn(),
  writeMock: vi.fn(),
}));
vi.mock("../src/api.js", () => ({ api: apiMock, write: writeMock }));
import { useExecutionMachines } from "../src/useExecutionMachines";

const machines = [{ id: "mac", name: "Mac", status: "online" }];
function pending() {
  let resolve, reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

describe("personal execution machine selection", () => {
  beforeEach(() => {
    apiMock.mockReset();
    writeMock.mockReset();
    apiMock.mockImplementation(async (path) =>
      path.includes("?mine=1") ? machines : { runnerId: "mac" },
    );
    writeMock.mockImplementation(async (_path, body) => body);
  });

  test("loads only the current user's bound machines and saves through the existing default API", async () => {
    const error = vi.fn();
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, error),
    );
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    expect(result.current.machines).toEqual(machines);
    expect(result.current.defaultRunnerId).toBe("mac");
    expect(apiMock).toHaveBeenCalledWith("/api/runners?mine=1");
    await act(() => result.current.selectMachine(""));
    expect(writeMock).toHaveBeenCalledWith(
      "/api/runners/default",
      { runnerId: "" },
      "PUT",
    );
    expect(result.current.defaultRunnerId).toBe("");
  });

  test("holds the selection during saving, rejects overlapping saves and preserves the default on failure", async () => {
    const error = vi.fn(),
      save = pending();
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, error),
    );
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    writeMock.mockReturnValueOnce(save.promise);
    let saving;
    act(() => {
      saving = result.current.selectMachine("");
    });
    expect(result.current.machineSaving).toBe(true);
    expect(result.current.machineSavingRef.current).toBe(true);
    expect(result.current.defaultRunnerId).toBe("mac");
    await act(async () =>
      expect(await result.current.selectMachine("other")).toBe(false),
    );
    expect(writeMock).toHaveBeenCalledTimes(1);
    await act(async () => {
      save.reject(Error("没有权限"));
      await saving;
    });
    expect(result.current.defaultRunnerId).toBe("mac");
    expect(result.current.machineSaving).toBe(false);
    expect(error).toHaveBeenLastCalledWith("没有权限");
  });

  test("a late refresh cannot overwrite a saved selection", async () => {
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, vi.fn()),
    );
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    const stale = pending();
    apiMock.mockImplementation(async (path) =>
      path.includes("?mine=1") ? machines : stale.promise,
    );
    let refreshing;
    act(() => {
      refreshing = result.current.refreshMachines();
    });
    await act(() => result.current.selectMachine(""));
    await act(async () => {
      stale.resolve({ runnerId: "mac" });
      await refreshing;
    });
    expect(result.current.defaultRunnerId).toBe("");
  });

  test("refreshes the default after returning from settings", async () => {
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, vi.fn()),
    );
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    apiMock.mockImplementation(async (path) =>
      path.includes("?mine=1") ? machines : { runnerId: "" },
    );
    act(() =>
      window.dispatchEvent(
        new MessageEvent("message", {
          source: window.parent,
          origin: location.origin,
          data: { type: "agentflow-environments-changed" },
        }),
      ),
    );
    await waitFor(() => expect(result.current.defaultRunnerId).toBe(""));
  });

  test("a failed initial load stays unknown and can be retried", async () => {
    apiMock.mockRejectedValueOnce(Error("网络中断"));
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, vi.fn()),
    );
    await waitFor(() => expect(result.current.machineError).toBe("网络中断"));
    expect(result.current.machinesReady).toBe(false);
    await act(() => result.current.refreshMachines());
    expect(result.current.machinesReady).toBe(true);
    expect(result.current.defaultRunnerId).toBe("mac");
    expect(result.current.machineError).toBe("");
  });
});
