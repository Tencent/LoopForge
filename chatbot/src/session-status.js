const states = {
  running: { state: "running", label: "Đang trả lời", icon: "arc-spinner", busy: true },
  // 回复结束不再画勾：侧栏只提示进行中、异常和待接话的状态，
  // 避免每行都挂一个表示“已回复”的对勾。
  completed: { state: "completed", label: "Đã trả lời xong" },
  failed: { state: "failed", label: "Trả lời thất bại", icon: "alert" },
  // 手动停止和回复完成一样是结束状态：侧栏不再挂停止方块，
  // 否则被停过的会话会一直留着一个像“还能再点一次”的停止图标。
  cancelled: { state: "cancelled", label: "Đã dừng trả lời" },
  interrupted: { state: "interrupted", label: "Câu trả lời bị gián đoạn", icon: "warning" },
  idle: { state: "idle", label: "Hiện không có câu trả lời đang tạo", icon: "chat" },
};

// A generation describes a model reply, never the completion of a project/task.
// Only the selected session supplies live generation/submission information.
export function sessionReplyStatus(session, { generation, busy = false } = {}) {
  const matching = generation && (!generation.sessionId || generation.sessionId === session.id);
  const value = busy ? "running" : (matching && generation.status) || session.generationStatus;
  return states[value === "canceled" ? "cancelled" : value] || states.idle;
}

// A completed reply the reader has not opened yet carries a blue dot, as in
// Codex; once read, a completed row is quiet again.
const unreadState = { state: "unread", label: "Có câu trả lời mới chưa đọc", dot: true };

export function sidebarReplyStatus(status, unread = false) {
  return unread && status.state === "completed" ? unreadState : status;
}

// Run sessions only earn a chip while something needs the reader: a running
// SOP, a run waiting for a person, or a failure. Finished runs stay quiet; the
// title already says which SOP they belong to.
const runStates = {
  running: { state: "running", label: "Đang chạy" },
  waiting: { state: "waiting", label: "Đang chờ xử lý" },
  failed: { state: "failed", label: "Thất bại" },
};

export function runSessionState(session) {
  if (!session?.shared || !session?.runId) return null;
  const state = runStates[session.runStatus];
  if (!state) return null;
  return { ...state, label: session.runStatusLabel || state.label };
}

// The hover text of a history row. A run conversation's title says what the
// run does, so its SOP and run state are named here instead of in the row.
export function sessionTooltip(session, { projectName = "", replyLabel = "" } = {}) {
  const run = Boolean(session?.shared && session?.runId);
  const title = session?.displayTitle || session?.title || "Trò chuyện mới";
  const sop = run && session.sopName && !title.includes(session.sopName) ? session.sopName : "";
  return [title, projectName, sop, run ? session.runStatusLabel || replyLabel : replyLabel]
    .filter(Boolean)
    .join(" · ");
}
