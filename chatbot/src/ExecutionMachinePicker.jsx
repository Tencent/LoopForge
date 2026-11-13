import React, { useEffect, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { SelectMenu } from "./ui";
import "./execution-machine-picker.css";

export default function ExecutionMachinePicker({ chat, onManage }) {
  const trigger = useRef(null);
  const [open, setOpen] = useState(false);
  const locked = !!chat.session?.executionTarget;
  const disabled = !!(
    chat.busy ||
    chat.machineSaving ||
    chat.readOnly ||
    chat.local ||
    !chat.user ||
    chat.user.isGuest
  );
  useEffect(() => {
    if (disabled || locked) setOpen(false);
  }, [disabled, locked]);

  const execution = chat.session?.executionTarget;
  const runnerId = locked
    ? execution?.pool === "cloud"
      ? ""
      : execution?.runnerId || ""
    : chat.defaultRunnerId || "";
  const machine = chat.machines?.find((row) => row.id === runnerId);
  const known = locked ? !!execution : chat.machinesReady;
  const loadError = locked ? "" : chat.machineError;
  const label = !known
    ? loadError
      ? "执行机器加载失败"
      : "加载执行机器…"
    : runnerId
      ? machine?.name || runnerId
      : "LoopForge 云端";
  const online =
    known &&
    (runnerId
      ? machine?.status === "online"
      : chat.cloudPool?.configured
        ? chat.cloudPool.online > 0
        : true);
  const title = locked
    ? loadError || `当前会话执行机器：${label}。会话已绑定，后续运行始终使用此机器。`
    : chat.machineError || "为新会话选择执行机器，选定后固定";

  if (locked)
    return (
      <span className="machine-context machine-fixed" title={title}>
        <HarnessIcon name="server" size={13} interactive={false} />
        <span>{label}</span>
        <HarnessIcon name="lock" size={11} interactive={false} />
      </span>
    );

  const options = chat.machinesReady
    ? [
        {
          id: "",
          label: "LoopForge 云端",
          icon: "server",
          description:
            chat.cloudPool?.configured && !chat.cloudPool.online
              ? "暂无在线机器"
              : "平台自动分配",
        },
        ...(chat.machines || []).map((row) => ({
          id: row.id,
          label: row.name || row.id,
          icon: "monitor",
          description: row.status === "online" ? "在线" : "离线，执行前需启动",
        })),
      ]
    : [];

  return (
    <span className="menu-anchor machine-context">
      <button
        ref={trigger}
        type="button"
        className="context-pill machine-picker"
        title={title}
        aria-label={`执行机器：${label}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-busy={chat.machineSaving || undefined}
        disabled={disabled}
        onClick={() => {
          if (!open) void chat.refreshMachines?.();
          setOpen(!open);
        }}
      >
        <HarnessIcon name="server" size={13} interactive={false} />
        <span>{label}</span>
        <i
          className={`connection-dot${online ? " online" : ""}`}
          aria-hidden="true"
        />
        <HarnessIcon name="chevron-down" size={12} interactive={false} />
      </button>
      <SelectMenu
        open={open}
        label="会话执行机器"
        value={chat.defaultRunnerId || ""}
        options={options}
        onSelect={(id) => void chat.selectMachine(id)}
        onClose={() => setOpen(false)}
        anchorRef={trigger}
        searchable
        emptyText={
          chat.machinesReady
            ? "没有匹配的执行机器"
            : chat.machineError
              ? "执行机器加载失败，请重试"
              : "正在加载执行机器"
        }
        actions={[
          {
            id: "refresh-machines",
            label: chat.machineError ? "重新加载" : "刷新机器列表",
            icon: "refresh",
            onSelect: chat.refreshMachines,
          },
          ...(onManage
            ? [
                {
                  id: "manage-machines",
                  label: "管理执行机器",
                  icon: "settings",
                  onSelect: onManage,
                },
              ]
            : []),
        ]}
      />
    </span>
  );
}
