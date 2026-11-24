import React, { useEffect, useRef, useState } from "react";
import { api } from "./api";
import HarnessIcon from "./HarnessIcon";
import { SelectMenu } from "./ui";
import "./sandbox-picker.css";

export default function ProjectCredentialPicker({ chat }) {
  const trigger = useRef(null);
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState([]);
  const [selection, setSelection] = useState([]);
  const [status, setStatus] = useState("");
  const { activeId, projectId, projectCredentialIds = [] } = chat;
  const disabled = chat.busy || chat.sandboxBusy || chat.readOnly;

  useEffect(() => {
    setOpen(false);
  }, [activeId, projectId, disabled]);

  useEffect(() => {
    if (!open || !projectId) return;
    let active = true;
    setItems([]);
    setStatus("正在加载项目凭据…");
    void api(`/api/projects/${encodeURIComponent(projectId)}/credentials`).then(({ credentials }) => {
      if (!active) return;
      setItems(credentials);
      setStatus("");
    }).catch((error) => {
      if (active) setStatus(error.message);
    });
    return () => { active = false; };
  }, [open, projectId]);

  if ((!projectId && !projectCredentialIds.length) || chat.session?.runId || chat.session?.shared || chat.readOnly) return null;
  const available = items.filter((item) => !item.disabled || selection.includes(item.id));
  const options = available.map((item) => ({
    id: item.id,
    label: item.name,
    description: item.disabled ? "已停用" : item.variables.join(" · "),
    icon: "lock",
  }));
  if (!status) {
    for (const id of selection) {
      if (!available.some((item) => item.id === id)) {
        options.push({ id, label: "凭据已不可用", description: "取消选中可解除绑定", icon: "lock" });
      }
    }
  }
  const changed = selection.length !== projectCredentialIds.length ||
    selection.some((id) => !projectCredentialIds.includes(id));
  const actions = changed ? [{ id: "apply-credentials", label: "应用", onSelect: () => void chat.selectProjectCredentials(selection) }] : [];
  if (projectCredentialIds.length) {
    actions.push({ id: "clear-credentials", label: "清除绑定", onSelect: () => void chat.selectProjectCredentials([]) });
  }
  const label = projectCredentialIds.length ? `项目凭据 · ${projectCredentialIds.length}` : "项目凭据";
  return (
    <span className="menu-anchor sandbox-context">
      <button
        ref={trigger}
        type="button"
        className="context-pill sandbox-picker"
        aria-label={label}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => {
          if (!open) setSelection(projectCredentialIds);
          setOpen(!open);
        }}
      >
        <HarnessIcon name="lock" size={13} interactive={false} />
        <span>{label}</span>
        <HarnessIcon name="chevron-down" size={12} interactive={false} />
      </button>
      <SelectMenu
        open={open}
        anchorRef={trigger}
        label="项目凭据"
        value={selection}
        options={options}
        multiple
        onSelect={(id) => setSelection((current) => current.includes(id) ? current.filter((key) => key !== id) : [...current, id])}
        onClose={() => setOpen(false)}
        emptyText={status || "当前项目没有可用的凭据"}
        actions={actions}
      />
    </span>
  );
}
