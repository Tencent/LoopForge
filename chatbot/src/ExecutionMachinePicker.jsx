import React, { useEffect, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { SelectMenu } from "./ui";
import { executionLocked } from "./execution-target";
import { sandboxDriverPresentation } from "./SandboxPicker";
import "./execution-machine-picker.css";

export default function ExecutionMachinePicker({ chat, onManage, onManageClusters }) {
  const trigger = useRef(null);
  const [open, setOpen] = useState(false);
  const locked = executionLocked(chat.session);
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
  const clusterId = locked ? execution?.clusterId : chat.selectedClusterId;
  const cluster = chat.availableClusters?.find((row) => row.id === clusterId);
  const clusterAuto = !locked && chat.effectiveExecutionPool === "cluster-auto";
  const machineAuto = !locked && chat.effectiveExecutionPool === "machine-auto";
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
      ? "执行位置加载失败"
      : "加载执行位置…"
    : clusterId
      ? cluster?.name || chat.workspace?.execution?.executionClusterName || clusterId
      : clusterAuto
      ? "K8S 自动分配"
      : machineAuto
      ? "机器自动分配"
      : runnerId
      ? machine?.name || runnerId
      : "LoopForge 云端";
  const online =
    known &&
    !clusterId && !clusterAuto && !machineAuto && (runnerId
      ? machine?.status === "online"
      : chat.cloudPool?.configured
        ? chat.cloudPool.online > 0
        : true);
  const title = locked
    ? loadError || `当前会话执行位置：${label}。后续对话继续使用此位置。`
    : chat.machineError || "选择执行位置，发送首条消息后固定";
  const sandbox = chat.workspaceSandbox;
  const sandboxType = sandboxDriverPresentation(sandbox?.driver);
  const sandboxLabel = sandbox?.id ? `${sandboxType.label}-${sandbox.id}` : "";

  if (locked)
    return (
      <span className={`machine-context machine-fixed${sandboxLabel ? " has-sandbox" : ""}`}>
        <span className="machine-name" title={title}>
          <HarnessIcon name={clusterId ? "layers" : "server"} size={13} interactive={false} />
          <span>{label}</span>
          <HarnessIcon name="lock" size={11} interactive={false} />
        </span>
        {sandboxLabel ? (
          <span
            className="machine-sandbox"
            title={sandboxLabel}
          >
            <HarnessIcon name={sandboxType.icon} size={13} interactive={false} />
            <span>{sandbox.id}</span>
          </span>
        ) : null}
      </span>
    );

  const options = chat.machinesReady
    ? [
        ...(chat.executionProjectId ? [
          { id: "project", label: "按项目调度", icon: "workflow" },
          { id: "machine-auto", label: "自动分配执行机器", icon: "server" },
        ] : []),
        {
          id: "cloud",
          label: "LoopForge 云端",
          icon: "server",
          description:
            chat.cloudPool?.configured && !chat.cloudPool.online
              ? "暂无在线机器"
              : "平台自动分配",
        },
        ...(chat.availableMachines || []).map((row) => ({
          id: "runner:" + row.id,
          label: row.name || row.id,
          icon: "monitor",
          description: [
            row.ownerUserId && row.ownerUserId !== chat.user?.id ? "项目共享" : "",
            row.status === "online" ? "在线" : "离线，执行前需启动",
          ].filter(Boolean).join(" · "),
        })),
        ...(chat.availableClusters?.length ? [
          { id: "cluster-auto", label: "自动分配 K8S 集群", icon: "layers" },
        ] : []),
        ...(chat.availableClusters || []).map((row) => ({
          id: "cluster:" + row.id,
          label: row.name,
          icon: "layers",
          description: "K8S 集群",
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
        aria-label={`执行位置：${label}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-busy={chat.machineSaving || undefined}
        disabled={disabled}
        onClick={() => {
          if (!open) void chat.refreshMachines?.();
          setOpen(!open);
        }}
      >
        <HarnessIcon name={clusterId || clusterAuto ? "layers" : "server"} size={13} interactive={false} />
        <span>{label}</span>
        {!clusterId && !clusterAuto && !machineAuto ? <i
          className={`connection-dot${online ? " online" : ""}`}
          aria-hidden="true"
        /> : null}
        <HarnessIcon name="chevron-down" size={12} interactive={false} />
      </button>
      <SelectMenu
        open={open}
        label="会话执行位置"
        value={chat.executionSelection || ""}
        options={options}
        onSelect={(id) => void chat.selectMachine(id)}
        onClose={() => setOpen(false)}
        anchorRef={trigger}
        searchable
        emptyText={
          chat.machinesReady
            ? "没有匹配的执行位置"
            : chat.machineError
              ? "执行位置加载失败，请重试"
              : "正在加载执行位置"
        }
        actions={[
          {
            id: "refresh-machines",
            label: chat.machineError ? "重新加载" : "刷新执行位置",
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
          ...(onManageClusters ? [{
            id: "manage-clusters",
            label: "管理 K8S 集群",
            icon: "layers",
            onSelect: onManageClusters,
          }] : []),
        ]}
      />
    </span>
  );
}
