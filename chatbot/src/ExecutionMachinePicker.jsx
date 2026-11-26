import React, { useEffect, useRef, useState } from "react";
import HarnessIcon from "./HarnessIcon";
import { SelectMenu } from "./ui";
import { executionLocked } from "./execution-target";
import "./execution-machine-picker.css";

export default function ExecutionMachinePicker({ chat, onManage }) {
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
      ? "Không tải được máy thực thi"
      : "Đang tải máy thực thi…"
    : runnerId
      ? machine?.name || runnerId
      : "LoopForge Cloud";
  const online =
    known &&
    (runnerId
      ? machine?.status === "online"
      : chat.cloudPool?.configured
        ? chat.cloudPool.online > 0
        : true);
  const title = locked
    ? loadError || `Máy thực thi của phiên hiện tại: ${label}. Phiên đã gắn máy này; các lần chạy sau cũng dùng máy này.`
    : chat.machineError || "Chọn máy thực thi; sẽ được cố định sau khi bạn gửi tin nhắn đầu tiên";

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
          label: "LoopForge Cloud",
          icon: "server",
          description:
            chat.cloudPool?.configured && !chat.cloudPool.online
              ? "Chưa có máy đang online"
              : "Nền tảng tự phân bổ",
        },
        ...(chat.machines || []).map((row) => ({
          id: row.id,
          label: row.name || row.id,
          icon: "monitor",
          description: row.status === "online" ? "Online" : "Offline, cần khởi động trước khi chạy",
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
        aria-label={`Máy thực thi: ${label}`}
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
        label="Máy thực thi của phiên"
        value={chat.defaultRunnerId || ""}
        options={options}
        onSelect={(id) => void chat.selectMachine(id)}
        onClose={() => setOpen(false)}
        anchorRef={trigger}
        searchable
        emptyText={
          chat.machinesReady
            ? "Không có máy thực thi khớp"
            : chat.machineError
              ? "Không tải được máy thực thi, hãy thử lại"
              : "Đang tải máy thực thi"
        }
        actions={[
          {
            id: "refresh-machines",
            label: chat.machineError ? "Tải lại" : "Làm mới danh sách máy",
            icon: "refresh",
            onSelect: chat.refreshMachines,
          },
          ...(onManage
            ? [
                {
                  id: "manage-machines",
                  label: "Quản lý máy thực thi",
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
