import { useCallback, useEffect, useRef, useState } from "react";
import { api, write } from "./api";

// This is the same personal default used by Settings → 执行机器. Existing
// workspaces keep the target frozen in their run's executionContext.
export function useExecutionMachines(userId, busy, readOnly, onError) {
  const [machines, setMachines] = useState([]);
  const [defaultRunnerId, setDefaultRunnerId] = useState("");
  const [cloudPool, setCloudPool] = useState(null);
  const [machinesReady, setMachinesReady] = useState(false);
  const [machineError, setMachineError] = useState("");
  const [machineSaving, setMachineSaving] = useState(false);
  const machineSavingRef = useRef(false);
  const revision = useRef(0);

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
      setDefaultRunnerId(selected.runnerId || "");
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
      const selected = await write("/api/runners/default", { runnerId }, "PUT");
      if (request !== revision.current) return false;
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
  };
}
