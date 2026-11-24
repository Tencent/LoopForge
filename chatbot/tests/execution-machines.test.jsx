import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

const { apiMock, writeMock } = vi.hoisted(() => ({
  apiMock: vi.fn(),
  writeMock: vi.fn(),
}));
vi.mock("../src/api.js", () => ({ api: apiMock, write: writeMock }));
import { useExecutionMachines } from "../src/useExecutionMachines";

const bindMock = vi.fn();
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
    bindMock.mockReset();
    bindMock.mockImplementation(async (runnerId) => ({runnerId}));
    apiMock.mockImplementation(async (path) =>
      path.includes("?mine=1") ? machines : { runnerId: "mac" },
    );
    writeMock.mockImplementation(async (_path, body) => body);
  });

  test("loads only the current user's bound machines and binds the conversation without updating the personal default", async () => {
    const error = vi.fn();
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, error, bindMock),
    );
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    expect(result.current.machines).toEqual(machines);
    expect(result.current.defaultRunnerId).toBe("mac");
    expect(apiMock).toHaveBeenCalledWith("/api/runners?mine=1");
    await act(() => result.current.selectMachine(""));
    expect(bindMock).toHaveBeenCalledWith("");
    expect(writeMock).not.toHaveBeenCalled();
    expect(result.current.defaultRunnerId).toBe("");
  });

  test("holds the selection during saving, rejects overlapping saves and preserves the default on failure", async () => {
    const error = vi.fn(),
      save = pending();
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, error, bindMock),
    );
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    bindMock.mockReturnValueOnce(save.promise);
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
    expect(bindMock).toHaveBeenCalledTimes(1);
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
      useExecutionMachines(1, false, false, vi.fn(), bindMock),
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
      useExecutionMachines(1, false, false, vi.fn(), bindMock),
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

  test("replaces the scoped draft selection when switching projects", async () => {
    const shared = { id: "shared", name: "Shared machine", ownerUserId: 2, canUseInProject: true };
    const afterSwitch = pending();
    apiMock.mockImplementation(async (path) => {
      if (path === "/api/runners?projectId=one") return [...machines, shared];
      if (path === "/api/runners?projectId=two") return afterSwitch.promise;
      return { runnerId: "mac" };
    });
    const { result, rerender } = renderHook(({ projectId }) =>
      useExecutionMachines(1, false, false, vi.fn(), bindMock, null, projectId),
      { initialProps: { projectId: "one" } },
    );
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    expect(result.current.availableMachines).toEqual([shared]);
    expect(result.current.defaultRunnerId).toBe("");
    await act(async () => expect(await result.current.selectMachine("mac")).toBe(false));
    expect(bindMock).not.toHaveBeenCalled();
    await act(() => result.current.selectMachine(shared.id));
    expect(result.current.defaultRunnerId).toBe(shared.id);

    rerender({ projectId: "two" });
    expect(result.current.machinesReady).toBe(false);
    await act(async () => afterSwitch.resolve(machines));
    expect(result.current.machinesReady).toBe(true);
    expect(result.current.machines).toEqual(machines);
    expect(result.current.availableMachines).toEqual([]);
    expect(result.current.defaultRunnerId).toBe("");
    expect(apiMock).toHaveBeenCalledWith("/api/runners/default?projectId=two&conversation=1");
  });

  test("uses an eligible project default when a saved draft loses its grant", async () => {
    const owned = { ...machines[0], canUseInProject: true };
    const shared = { id: "shared", ownerUserId: 2, canUseInProject: true };
    let listed = [owned, shared];
    apiMock.mockImplementation(async (path) =>
      path === "/api/runners?projectId=one" ? listed : { runnerId: "shared" },
    );
    const { result, rerender } = renderHook(({ target }) =>
      useExecutionMachines(1, false, false, vi.fn(), bindMock, target, "one"),
      { initialProps: { target: null } },
    );
    await waitFor(() => expect(result.current.defaultRunnerId).toBe("shared"));
    rerender({ target: { pool: "runner", runnerId: "mac" } });
    expect(result.current.defaultRunnerId).toBe("mac");

    listed = [machines[0], shared];
    await act(() => result.current.refreshMachines());
    expect(result.current.machines).toEqual(listed);
    expect(result.current.availableMachines).toEqual([shared]);
    expect(result.current.defaultRunnerId).toBe("shared");
    rerender({ target: { pool: "runner", runnerId: "mac" } });
    expect(result.current.defaultRunnerId).toBe("shared");
  });

  test("hides SOP-only machines and uses the chat default for an incompatible draft", async () => {
    const oldRunner = { id: "old", canUseInProject: true, supportsConversation: false };
    const chatRunner = { id: "chat", canUseInProject: true, supportsConversation: true };
    apiMock.mockImplementation(async (path) =>
      path === "/api/runners?projectId=one" ? [oldRunner, chatRunner] : { runnerId: "chat" },
    );
    const target = { pool: "runner", runnerId: oldRunner.id };
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, vi.fn(), bindMock, target, "one"),
    );
    await waitFor(() => expect(result.current.defaultRunnerId).toBe("chat"));
    expect(result.current.availableMachines).toEqual([chatRunner]);
    expect(apiMock).toHaveBeenCalledWith("/api/runners/default?projectId=one&conversation=1");
    await act(async () => expect(await result.current.selectMachine(oldRunner.id)).toBe(false));
    expect(bindMock).not.toHaveBeenCalled();
  });

  test("keeps an explicit cloud choice through refresh, and resets only for a new draft", async () => {
    const { result } = renderHook(() => useExecutionMachines(1, false, false, vi.fn(), bindMock));
    await waitFor(() => expect(result.current.machinesReady).toBe(true));
    await act(() => result.current.selectMachine(""));
    await act(() => result.current.refreshMachines());
    expect(result.current.defaultRunnerId).toBe("");
    await act(() => result.current.selectMachine("mac"));
    await act(() => result.current.selectMachine(""));
    expect(writeMock).not.toHaveBeenCalled();
    act(() => result.current.resetMachine());
    expect(result.current.defaultRunnerId).toBe("mac");
  });

  test("a failed initial load stays unknown and can be retried", async () => {
    apiMock.mockRejectedValueOnce(Error("网络中断"));
    const { result } = renderHook(() =>
      useExecutionMachines(1, false, false, vi.fn(), bindMock),
    );
    await waitFor(() => expect(result.current.machineError).toBe("网络中断"));
    expect(result.current.machinesReady).toBe(false);
    await act(() => result.current.refreshMachines());
    expect(result.current.machinesReady).toBe(true);
    expect(result.current.defaultRunnerId).toBe("mac");
    expect(result.current.machineError).toBe("");
  });
});
