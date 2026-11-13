import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

// Machine selection belongs to the local draft until its first turn starts.
export function useExecutionMachines(userId, busy, readOnly, onError, bindMachine, draftTarget) {
  const [machines, setMachines] = useState([]);
  const [defaultRunnerId, setDefaultRunnerId] = useState("");
  const [cloudPool, setCloudPool] = useState(null);
  const [machinesReady, setMachinesReady] = useState(false);
  const [machineError, setMachineError] = useState("");
  const [machineSaving, setMachineSaving] = useState(false);
  const machineSavingRef = useRef(false);
  const revision = useRef(0);
  const chosen = useRef(false);
  const suggestedRunner = useRef("");
  const targetRef = useRef(draftTarget);
  targetRef.current = draftTarget;
  const resetMachine = useCallback(() => {
    chosen.current = false;
    setDefaultRunnerId(suggestedRunner.current);
  }, []);
  useEffect(() => {
    if (draftTarget && !chosen.current)
      setDefaultRunnerId(draftTarget.pool === "runner" ? draftTarget.runnerId || "" : "");
  }, [draftTarget]);

  const refreshMachines = useCallback(async () => {
    if (!userId || machineSavingRef.current) return;
    const request = ++revision.current;
    try {
      const [rows, selected] = await Promise.all([
        api("/api/runners?mine=1"),
        api("/api/runners/default"),
      ]);
      if (request !== revision.current) return;
      setMachines(Array.isArray(rows) ? rows : []);
      suggestedRunner.current = selected.runnerId || "";
      if (!chosen.current) {
        const target = targetRef.current;
        setDefaultRunnerId(target ? target.pool === "runner" ? target.runnerId || "" : "" : suggestedRunner.current);
      }
      setCloudPool(selected.cloudPool || null);
      setMachinesReady(true);
      setMachineError("");
    } catch (e) {
      if (request === revision.current) setMachineError(e.message);
    }
  }, [userId]);

  useEffect(() => {
    setMachinesReady(false);
    setMachines([]);
    setDefaultRunnerId("");
    chosen.current = false;
    suggestedRunner.current = "";
    setCloudPool(null);
    setMachineError("");
    void refreshMachines();
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
      chosen.current = true;
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
