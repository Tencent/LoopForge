import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

// Machine selection belongs to the local draft until its first turn starts.
export function useExecutionMachines(userId, busy, readOnly, onError, bindMachine, draftTarget, projectId = "") {
  const [machines, setMachines] = useState([]);
  const [defaultRunnerId, setDefaultRunnerId] = useState("");
  const [cloudPool, setCloudPool] = useState(null);
  const [loadedScope, setLoadedScope] = useState(null);
  const scope = `${userId ?? ""}/${projectId}`;
  const machinesReady = loadedScope === scope;
  const [machineError, setMachineError] = useState("");
  const [machineSaving, setMachineSaving] = useState(false);
  const machineSavingRef = useRef(false);
  const revision = useRef(0);
  const chosen = useRef(null);
  const suggestedRunner = useRef("");
  const targetRef = useRef(draftTarget);
  targetRef.current = draftTarget;
  const resetMachine = useCallback(() => {
    chosen.current = null;
    setDefaultRunnerId(suggestedRunner.current);
  }, []);
  useEffect(() => {
    if (draftTarget && chosen.current === null)
      setDefaultRunnerId(draftTarget.pool === "runner" ? draftTarget.runnerId || "" : "");
  }, [draftTarget]);

  const refreshMachines = useCallback(async (force) => {
    if (!userId || (machineSavingRef.current && force !== true)) return;
    const request = ++revision.current;
    const query = projectId ? `?projectId=${encodeURIComponent(projectId)}` : "";
    try {
      const [rows, selected] = await Promise.all([
        api(`/api/runners${query || "?mine=1"}`),
        api(`/api/runners/default${query}`),
      ]);
      if (request !== revision.current) return;
      const available = Array.isArray(rows) ? rows : [];
      const hasMachine = (id) => available.some((row) => row.id === id);
      setMachines(available);
      suggestedRunner.current = selected.runnerId || "";
      if (chosen.current && !hasMachine(chosen.current)) chosen.current = null;
      if (chosen.current === null) {
        const target = targetRef.current;
        const draftRunner = target?.pool === "runner" ? target.runnerId : "";
        setDefaultRunnerId(target?.pool === "cloud" ? "" : hasMachine(draftRunner) ? draftRunner : suggestedRunner.current);
      }
      setCloudPool(selected.cloudPool || null);
      setLoadedScope(scope);
      setMachineError("");
    } catch (e) {
      if (request === revision.current) setMachineError(e.message);
    }
  }, [projectId, scope, userId]);

  useEffect(() => {
    setLoadedScope(null);
    setMachines([]);
    setDefaultRunnerId("");
    chosen.current = null;
    suggestedRunner.current = "";
    setCloudPool(null);
    setMachineError("");
    void refreshMachines(true);
    window.addEventListener("focus", refreshMachines);
    const returnedToChat = (event) => {
      if (
        event.source === window.parent &&
        event.origin === location.origin &&
        event.data?.type === "agentflow-environments-changed"
      )
        void refreshMachines();
    };
    window.addEventListener("message", returnedToChat);
    return () => {
      ++revision.current;
      window.removeEventListener("focus", refreshMachines);
      window.removeEventListener("message", returnedToChat);
    };
  }, [refreshMachines]);

  async function selectMachine(runnerId) {
    if (
      !userId ||
      busy ||
      readOnly ||
      !machinesReady ||
      machineSavingRef.current
    )
      return false;
    machineSavingRef.current = true;
    setMachineSaving(true);
    const request = ++revision.current;
    onError("");
    try {
      const selected = await bindMachine(runnerId);
      if (request !== revision.current) return false;
      chosen.current = selected.runnerId || "";
      setDefaultRunnerId(selected.runnerId || "");
      setMachineError("");
      return true;
    } catch (e) {
      if (request === revision.current) onError(e.message);
      return false;
    } finally {
      machineSavingRef.current = false;
      setMachineSaving(false);
    }
  }

  return {
    machines,
    defaultRunnerId,
    cloudPool,
    machinesReady,
    machineError,
    machineSaving,
    machineSavingRef,
    selectMachine,
    refreshMachines,
    resetMachine,
  };
}
