import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import {
  addedFileDiff,
  diffTotals,
  findPatch,
  mergeChanges,
  parseUnifiedDiff,
} from "./changes";

const IDLE = {
  status: "idle",
  reason: "",
  warning: "",
  truncated: false,
  files: [],
  repositories: [],
  additions: 0,
  deletions: 0,
};

export const workspacePath = (runId) => `/api/runs/${encodeURIComponent(runId)}/workspace`;

// Read the conversation workspace git state: the changed file list plus the
// combined patch. Refreshes on demand and, while the panel is open, on a timer
// so edits streaming in from a running generation show up.
export function useWorkspaceChanges(runId, { active = false, interval = 6000 } = {}) {
  const [state, setState] = useState(IDLE);
  const [nonce, setNonce] = useState(0);
  const runIdRef = useRef(runId);
  runIdRef.current = runId;
  const refresh = useCallback(() => setNonce((value) => value + 1), []);

  useEffect(() => {
    if (!runId) {
      setState(IDLE);
      return undefined;
    }
    let current = true;
    setState((previous) =>
      previous.status === "ready" ? previous : { ...IDLE, status: "loading" },
    );
    Promise.all([
      api(workspacePath(runId)),
      api(`${workspacePath(runId)}/diff`).catch(() => null),
    ])
      .then(([info, patch]) => {
        if (!current) return;
        if (info?.available === false) {
          setState({
            ...IDLE,
            status: "unavailable",
            reason: info.reason || "工作区不可用",
            // A reclaimed workspace is the usual state once the work is
            // pushed; the branch on the code host is still where its changes live.
            repositories: info.repositoryDetails || [],
          });
          return;
        }
        const files = mergeChanges(
          info?.changes || [],
          parseUnifiedDiff(patch?.diff || ""),
        );
        setState({
          status: "ready",
          reason: "",
          warning: info?.warning || "",
          truncated: Boolean(patch?.truncated),
          files,
          // Branch names come from the run record, not the workspace: they let
          // the empty state point at the code host when everything is already committed.
          repositories: info?.repositoryDetails || [],
          ...diffTotals(files),
        });
      })
      .catch((error) => {
        if (current)
          setState({ ...IDLE, status: "error", reason: error.message });
      });
    return () => {
      current = false;
    };
  }, [runId, nonce]);

  useEffect(() => {
    if (!active || !runId) return undefined;
    // Reading the diff counts as workspace activity, so a hidden tab must not
    // keep polling or the workspace would never reach its idle cleanup.
    const timer = setInterval(() => {
      if (!document.hidden) refresh();
    }, interval);
    return () => clearInterval(timer);
  }, [active, runId, interval, refresh]);

  // Untracked files have no patch in the combined diff; load their content on
  // demand and present it as a fully added file.
  const loadFile = useCallback(async (file) => {
    const id = runIdRef.current;
    if (!id || !file) return null;
    if (file.patch) return file.patch;
    const payload = await api(
      `${workspacePath(id)}/diff?path=${encodeURIComponent(file.path)}`,
    );
    const found = findPatch(parseUnifiedDiff(payload?.diff || ""), file.path);
    if (found) return found;
    if (typeof payload?.modified === "string" && payload.modified)
      return addedFileDiff(file.path, payload.modified);
    return null;
  }, []);

  return { ...state, refresh, loadFile };
}
