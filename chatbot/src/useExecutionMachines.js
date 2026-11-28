import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";

function selectionForTarget(target) {
  if (target?.pool === "runner") return "runner:" + target.runnerId;
  if (target?.pool === "cluster") return "cluster:" + target.clusterId;
  return target?.pool || "";
}

function targetForSelection(selection) {
  if (selection.startsWith("runner:")) return { pool: "runner", runnerId: selection.slice(7) };
  if (selection.startsWith("cluster:")) return { pool: "cluster", clusterId: selection.slice(8) };
  return { pool: selection || "cloud" };
}

function canSelectTarget(target, machines, clusters, projectId) {
  switch (target?.pool) {
    case "runner":
      return machines.some((row) => row.id === target.runnerId);
    case "cluster":
      return clusters.some((row) => row.id === target.clusterId);
    case "cluster-auto":
      return clusters.length > 0;
    case "machine-auto":
    case "project":
      return !!projectId;
    case "cloud":
      return true;
    default:
      return false;
  }
}

// Execution selection belongs to the local draft until its first turn starts.
export function useExecutionMachines(userId, busy, readOnly, onError, bindMachine, draftTarget, projectId = "") {
  const [loaded, setLoaded] = useState(null);
  const [chosen, setChosen] = useState(null);
  const scope = `${userId ?? ""}/${projectId}`;
  const machinesReady = loaded?.scope === scope;
  const machines = machinesReady ? loaded.machines : [];
  const availableMachines = machinesReady ? loaded.availableMachines : [];
  const availableClusters = machinesReady ? loaded.availableClusters : [];
  const cloudPool = machinesReady ? loaded.cloudPool : null;
  const projectTarget = machinesReady ? loaded.projectTarget : null;
  let selection = "";
  if (machinesReady) {
    const savedSelection = canSelectTarget(draftTarget, availableMachines, availableClusters, projectId)
      ? selectionForTarget(draftTarget)
      : "";
    selection = chosen || savedSelection || (projectId ? "project" : selectionForTarget(projectTarget) || "cloud");
  }
  const draftExecutionTarget = targetForSelection(selection);
  const effectiveTarget = selection === "project" ? projectTarget : draftExecutionTarget;
  const selectedClusterId = effectiveTarget?.clusterId || "";
  const defaultRunnerId = effectiveTarget?.runnerId || "";
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
      const [rows, selected, clusters] = await Promise.all([
        api(`/api/runners?${projectQuery || "mine=1"}`),
        api(`/api/runners/default?${defaultQuery}`),
        api(`/api/execution-clusters${projectQuery ? "?" + projectQuery : ""}`).catch((error) => {
          if (error.status === 404) return null;
          throw error;
        }),
      ]);
      if (request !== revision.current) return;
      const machines = Array.isArray(rows) ? rows : [];
      const availableMachines = machines.filter((row) =>
        row.supportsConversation !== false && (!projectId || row.canUseInConversation),
      );
      const availableClusters = clusters?.available || [];
      const hasMachine = (id) => availableMachines.some((row) => row.id === id);
      let projectTarget = clusters?.projectTarget;
      if (!projectTarget?.pool || projectTarget.pool === "personal") {
        const runnerId = projectTarget?.pool === "personal" ? selected.personalRunnerId : selected.runnerId;
        projectTarget = hasMachine(runnerId) ? { pool: "runner", runnerId } : { pool: "cloud" };
      }
      setChosen((current) =>
        current && !canSelectTarget(targetForSelection(current), availableMachines, availableClusters, projectId)
          ? null
          : current,
      );
      setLoaded({
        scope,
        machines,
        availableMachines,
        availableClusters,
        projectTarget,
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

  async function selectMachine(value) {
    const target = targetForSelection(value);
    if (
      !userId ||
      busy ||
      readOnly ||
      !machinesReady ||
      machineSavingRef.current ||
      !canSelectTarget(target, availableMachines, availableClusters, projectId)
    )
      return false;
    machineSavingRef.current = true;
    setMachineSaving(true);
    const request = ++revision.current;
    onError("");
    try {
      await bindMachine(target.runnerId || "");
      if (request !== revision.current) return false;
      setChosen(selectionForTarget(target));
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
    availableClusters,
    selectedClusterId,
    executionProjectId: projectId,
    effectiveExecutionPool: effectiveTarget?.pool,
    draftExecutionTarget,
    executionSelection: selection,
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
