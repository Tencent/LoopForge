import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

// Machine selection belongs to the local draft until its first turn starts.
export function useExecutionMachines(userId, busy, readOnly, onError, bindMachine, draftTarget, projectId = "") {
  const [loaded, setLoaded] = useState(null);
  const [chosen, setChosen] = useState(null);
  const scope = `${userId ?? ""}/${projectId}`;
  const machinesReady = loaded?.scope === scope;
  const machines = machinesReady ? loaded.machines : [];
  const availableMachines = machinesReady ? loaded.availableMachines : [];
  const cloudPool = machinesReady ? loaded.cloudPool : null;
  let defaultRunnerId = "";
  if (machinesReady) {
    if (chosen !== null) defaultRunnerId = chosen;
    else if (draftTarget?.pool === "runner" && availableMachines.some((row) => row.id === draftTarget.runnerId))
      defaultRunnerId = draftTarget.runnerId;
    else if (draftTarget?.pool !== "cloud") defaultRunnerId = loaded.suggestedRunner;
  }
  const [machineError, setMachineError] = useState("");
  const [machineSaving, setMachineSaving] = useState(false);
  const machineSavingRef = useRef(false);
  const revision = useRef(0);
  const resetMachine = useCallback(() => setChosen(null), []);

  const refreshMachines = useCallback(async (force) => {
    if (!userId || (machineSavingRef.current && force !== true)) return;
    const request = ++revision.current;
    const projectQuery = projectId ? `projectId=${encodeURIComponent(projectId)}` : "";
    const defaultQuery = projectQuery ? `${projectQuery}&conversation=1` : "conversation=1";
    try {
      const [rows, selected] = await Promise.all([
        api(`/api/runners?${projectQuery || "mine=1"}`),
        api(`/api/runners/default?${defaultQuery}`),
      ]);
      if (request !== revision.current) return;
      const machines = Array.isArray(rows) ? rows : [];
      const availableMachines = machines.filter((row) =>
        row.supportsConversation !== false && (!projectId || row.canUseInConversation),
      );
      const hasMachine = (id) => availableMachines.some((row) => row.id === id);
      setChosen((current) => current && !hasMachine(current) ? null : current);
      setLoaded({
        scope,
        machines,
        availableMachines,
        suggestedRunner: hasMachine(selected.runnerId) ? selected.runnerId : "",
        cloudPool: selected.cloudPool || null,
      });
      setMachineError("");
    } catch (e) {
      if (request === revision.current) setMachineError(e.message);
    }
  }, [projectId, scope, userId]);

  useEffect(() => {
    setLoaded(null);
    resetMachine();
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
  }, [refreshMachines, resetMachine]);

  async function selectMachine(runnerId) {
    if (
      !userId ||
      busy ||
      readOnly ||
      !machinesReady ||
      machineSavingRef.current ||
      (runnerId && !availableMachines.some((row) => row.id === runnerId))
    )
      return false;
    machineSavingRef.current = true;
    setMachineSaving(true);
    const request = ++revision.current;
    onError("");
    try {
      const selected = await bindMachine(runnerId);
      if (request !== revision.current) return false;
      setChosen(selected.runnerId || "");
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
    availableMachines,
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
