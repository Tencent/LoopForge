import React, { useEffect, useRef, useState } from "react";
import { SelectMenu } from "./ui";
import HarnessIcon from "./HarnessIcon";
import "./sandbox-picker.css";

export function conversationSandboxOptions(sandboxes, project, selected = "") {
  const available = (sandboxes || []).filter(
    (s) => s.driver === "docker" && (s.projectId === project?.id || s.shared),
  );
  const options = [
    { value: "", label: "由 Agent 选择" },
    ...available.map((s) => ({ value: s.id, label: s.name || s.id })),
  ];
  if (selected && !available.some((s) => s.id === selected)) {
    options.push({
      value: selected,
      label: `环境已不可用 · ${selected}`,
      disabled: true,
    });
  }
  return options;
}

export default function SandboxPicker({ chat, onCreate }) {
  const trigger = useRef(null);
  const [open, setOpen] = useState(false);
  const disabled = !!(
    chat.busy ||
    chat.sandboxBusy ||
    chat.readOnly
  );
  useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);
  if (!chat.projectId) return null;
  if (chat.sandboxLocked) {
    return (
      <span
        className="sandbox-context sandbox-fixed"
        title="任务会话使用任务的运行环境。"
      >
        <HarnessIcon name="lock" size={12} interactive={false} />
        <span>{chat.workspaceSandbox?.name || "任务运行环境"}</span>
      </span>
    );
  }
  const project = chat.projects.find((p) => p.id === chat.projectId);
  const options = conversationSandboxOptions(
    chat.sandboxes,
    project,
    chat.sandboxProfileId,
  );
  const label =
    options.find((o) => o.value === chat.sandboxProfileId)?.label ||
    options[0].label;
  const menuOptions = options
    .filter((o) => !o.disabled)
    .map((o) => ({
      id: o.value,
      label: o.label,
      description: o.value ? "" : "按要改的代码自动选择，拿不准时用平台通用 Docker",
      icon: o.value ? "terminal" : "folder",
    }));
  return (
    <span className="menu-anchor sandbox-context" title={label}>
      <button
        ref={trigger}
        type="button"
        className="context-pill sandbox-picker"
        aria-label={`运行环境：${label}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen(!open)}
      >
        <HarnessIcon name="terminal" size={13} interactive={false} />
        <span>{label}</span>
        <HarnessIcon name="chevron-down" size={12} interactive={false} />
      </button>
      <SelectMenu
        open={open}
        label="沙箱"
        value={chat.sandboxProfileId || ""}
        options={menuOptions}
        onSelect={(id) => void chat.selectSandbox(id)}
        onClose={() => setOpen(false)}
        anchorRef={trigger}
        searchable
        actions={
          onCreate
            ? [
                {
                  id: "new-sandbox",
                  label: "新增沙箱",
                  icon: "plus",
                  onSelect: onCreate,
                },
              ]
            : []
        }
      />
    </span>
  );
}
