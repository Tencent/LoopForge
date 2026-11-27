import React, { useEffect } from "react";
import HarnessIcon from "./HarnessIcon";
import visualStudioCodeLogo from "./assets/visual-studio-code.svg";
import "./workspace-ide.css";

// The same vscode-web workbench the LOOP run record embeds (web/vscode.html),
// pointed at the conversation's workspace run. It only knows the run ID, so a
// conversation workspace and a borrowed LOOP workspace open the same way.
export function workspaceIDEURL(runId, { embed = false } = {}) {
  const params = new URLSearchParams({ run: runId });
  if (embed) {
    params.set("embed", "1");
    params.set("source", "conversation");
  }
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

function desktopIDEURL(runId) {
  if (!/^[A-Za-z0-9_.-]{1,128}$/.test(runId) || runId === "." || runId === "..") return "";
  const hex = (value) => Array.from(new TextEncoder().encode(value), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
  const host = `loopforge-${hex(window.location.origin)}-${hex(runId)}`;
  return `vscode://vscode-remote/ssh-remote+${host}/workspace`;
}

export function WorkspaceIDEPicker({ runId, desktopIDE, onOpenOnline }) {
  const desktopURL = desktopIDE?.available ? desktopIDEURL(runId) : "";
  const desktopDetail = desktopURL
    ? "通过 Remote - SSH 连接当前工作区"
    : desktopIDE?.reason || (desktopIDE ? "当前工作区不支持桌面连接" : "正在检查工作区连接…");

  return (
    <div className="workspace-ide-picker" role="group" aria-label="选择 IDE">
      <button
        type="button"
        className="workspace-ide-option"
        aria-labelledby="online-ide-label"
        aria-describedby="online-ide-detail"
        onClick={onOpenOnline}
      >
        <span className="workspace-ide-option-icon"><HarnessIcon name="stage-ide" size={22} /></span>
        <span className="workspace-ide-option-copy">
          <strong id="online-ide-label">在线 IDE</strong>
          <span id="online-ide-detail">在当前页面浏览和编辑工作区</span>
        </span>
        <HarnessIcon name="chevron-right" className="workspace-ide-option-arrow" size={16} interactive={false} />
      </button>
      <a
        className="workspace-ide-option"
        href={desktopURL || undefined}
        role={desktopURL ? undefined : "link"}
        aria-disabled={!desktopURL || undefined}
        aria-labelledby="desktop-ide-label"
        aria-describedby="desktop-ide-detail"
      >
        <span className="workspace-ide-option-icon">
          <img src={visualStudioCodeLogo} alt="" width={22} height={22} />
        </span>
        <span className="workspace-ide-option-copy">
          <strong id="desktop-ide-label">本地 Visual Studio Code</strong>
          <span id="desktop-ide-detail">{desktopDetail}</span>
        </span>
        <HarnessIcon name="preview-expand" className="workspace-ide-option-arrow" size={16} interactive={false} />
      </a>
    </div>
  );
}

export default function WorkspaceIDE({ runId, active = true, onMaximize = () => {} }) {
  useEffect(() => {
    if (!active || !runId) return undefined;
    const onMessage = (event) => {
      if (event.origin !== window.location.origin) return;
      const data = event.data;
      if (data?.type !== "agentflow-vscode" || data.runId !== runId) return;
      if (data.action === "popout") openWorkspaceIDEWindow(runId);
      if (data.action === "maximize") onMaximize();
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [active, runId, onMaximize]);
  return (
    <div className="workspace-ide" hidden={!active}>
      <iframe
        className="workspace-ide-frame"
        aria-label="VS Code"
        src={workspaceIDEURL(runId, { embed: true })}
        allow="clipboard-read; clipboard-write"
      />
    </div>
  );
}
