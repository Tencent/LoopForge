import React, { useEffect } from "react";
import "./workspace-ide.css";

// The same vscode-web workbench the SOP run record embeds (web/vscode.html),
// pointed at the conversation's workspace run. It only knows the run ID, so a
// conversation workspace and a borrowed SOP workspace open the same way.
export function workspaceIDEURL(runId, { embed = false } = {}) {
  const params = new URLSearchParams({ run: runId });
  if (embed) params.set("embed", "1");
  return `/vscode.html?${params.toString()}`;
}

export function openWorkspaceIDEWindow(runId) {
  const name = "agentflow-vscode-" + String(runId || "").replace(/[^A-Za-z0-9_-]/g, "");
  const win = window.open(workspaceIDEURL(runId), name, "popup=yes,width=1440,height=900");
  try {
    win?.focus();
  } catch {
    /* cross-window focus can be refused */
  }
  return !!win;
}

export default function WorkspaceIDE({ runId, onMaximize = () => {} }) {
  useEffect(() => {
    if (!runId) return undefined;
    const onMessage = (event) => {
      if (event.origin !== window.location.origin) return;
      const data = event.data;
      if (data?.type !== "agentflow-vscode" || data.runId !== runId) return;
      if (data.action === "popout") openWorkspaceIDEWindow(runId);
      if (data.action === "maximize") onMaximize();
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [runId, onMaximize]);
  return (
    <div className="workspace-ide">
      <iframe
        className="workspace-ide-frame"
        aria-label="VS Code"
        src={workspaceIDEURL(runId, { embed: true })}
        allow="clipboard-read; clipboard-write"
      />
    </div>
  );
}
