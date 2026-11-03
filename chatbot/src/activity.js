// Both the live stream and saved message metadata use this reducer. Tool updates
// retain their original position; completion events never create duplicate rows.
export function mergeActivity(previous = [], events = []) {
  const steps = previous.map((step) => ({ ...step }));
  const closeThinking = (at) => {
    const last = steps.at(-1);
    if (last && last.kind !== "tool" && !last.finishedAt)
      last.finishedAt = at || "done";
  };
  for (const { data: event, sequence } of events) {
    const at = event.at || null;
    if (event.kind === "thinkingStart") {
      closeThinking(at);
      steps.push({
        id: `thinking-${sequence ?? steps.length}`,
        kind: "thinking",
        text: "",
        startedAt: at,
      });
    }
    if (event.kind === "thinking") {
      let last = steps.at(-1);
      if (
        !last ||
        last.kind !== "thinking" ||
        last.finishedAt ||
        (event.id && event.id !== last.id)
      ) {
        closeThinking(at);
        last = {
          id: event.id || `thinking-${sequence ?? steps.length}`,
          kind: "thinking",
          text: "",
          startedAt: at,
        };
        steps.push(last);
      }
      if (event.text && !(event.full && last.text.includes(event.text.trim())))
        last.text += event.text;
    }
    if (event.kind === "tool") {
      closeThinking(at);
      const id = event.id || `tool-${sequence ?? steps.length}`;
      const existing = steps.find((step) => step.id === id);
      if (existing) {
        if (event.detail) existing.detail = event.detail;
        if (event.tool) existing.tool = event.tool;
      } else
        steps.push({
          id,
          kind: "tool",
          tool: event.tool,
          detail: event.detail || "",
          text: event.text || "",
          startedAt: at,
        });
    }
    if (event.kind === "toolDone") {
      const step = steps.find((step) => step.id === event.id);
      if (step) {
        step.finishedAt = at || "done";
        step.failed = !!event.failed;
      } else if (event.failed)
        steps.push({
          id: event.id,
          kind: "tool",
          tool: "工具调用",
          text: "工具调用失败",
          failed: true,
          finishedAt: at || "done",
        });
    }
    if (event.kind === "status" && event.text) {
      closeThinking(at);
      if (steps.at(-1)?.kind !== "status" || steps.at(-1)?.text !== event.text)
        steps.push({
          id: `status-${sequence ?? steps.length}`,
          kind: "status",
          text: event.text,
          startedAt: at,
        });
    }
  }
  return steps;
}
export const savedActivity = (message) =>
  mergeActivity(
    [],
    (message.activity || []).map((data, sequence) => ({ data, sequence })),
  );
export function activityForReply(message, generation, liveSteps = []) {
  if (message.activity?.length) return savedActivity(message);
  // The old implementation retained only the most recent generation. Recover
  // that visible stream only when its text AND start time match this exact reply.
  const start = Date.parse(message.startedAt),
    generationStart = Date.parse(generation?.startedAt);
  let reply = generation?.reply;
  if (typeof reply === "string") {
    try {
      reply = JSON.parse(reply);
    } catch {
      return [];
    }
  }
  if (
    generation?.status === "completed" &&
    Number.isFinite(start) &&
    Number.isFinite(generationStart) &&
    Math.abs(start - generationStart) < 5000 &&
    replyText(message) === reply?.message
  )
    return liveSteps;
  return [];
}
export function activityFace(step) {
  if (step.kind === "thinking")
    return { kind: "thinking", busy: "正在思考", done: "已思考" };
  if (step.kind === "status")
    return { kind: "status", busy: step.text, done: step.text };
  const tool = (step.tool || "")
    .replace(/^mcp__agentflow_session__/, "")
    .replace(/^agentflow__/, "");
  const name = tool.toLowerCase();
  if (name === "verification")
    return { kind: "verify", busy: "正在验证", done: "已验证" };
  if (/submit_gate_result/.test(name))
    return { kind: "verify", busy: "正在提交节点结果", done: "已提交节点结果" };
  if (/workbench_workspace_open/.test(name))
    return { kind: "workflow", busy: "正在打开工作区", done: "已打开工作区" };
  if (/workbench_workspace_submit_mr/.test(name))
    return { kind: "workflow", busy: "正在提交 MR", done: "已提交 MR" };
  if (/todo/.test(name))
    return { kind: "plan", busy: "正在更新计划", done: "已更新计划" };
  if (/^(task|agent)$/.test(name))
    return { kind: "role", busy: "正在委派子任务", done: "已委派子任务" };
  if (/toolsearch|search_tools|tools_search/.test(name))
    return { kind: "search", busy: "正在查找工具", done: "已查找工具" };
  if (/tool_schema|lookup_tool_param_schema/.test(name))
    return { kind: "search", busy: "正在查看工具说明", done: "已查看工具说明" };
  const platform = name.match(
    /^(workflows|agents|issues|runs|repositories|rules|skills)_(list|get|save|clone|publish)$/,
  );
  if (platform) {
    const [, type, action] = platform;
    const noun = {
      workflows: "SOP",
      agents: "角色",
      issues: "需求",
      runs: "运行",
      repositories: "仓库",
      rules: "规则",
      skills: "技能",
    }[type];
    const verb = {
      list: ["查看", `${noun}${type === "workflows" ? " " : ""}列表`],
      get: ["读取", noun],
      save: ["保存", noun],
      clone: ["复制", noun],
      publish: ["发布", noun],
    }[action];
    return {
      kind:
        type === "workflows"
          ? "workflow"
          : type === "agents"
            ? "role"
            : action === "get"
              ? "read"
              : action === "list"
                ? "search"
                : "tool",
      busy: `正在${verb[0]}${type === "workflows" ? " " : ""}${verb[1]}`,
      done: `已${verb[0]}${type === "workflows" ? " " : ""}${verb[1]}`,
    };
  }
  if (/bash|shell|command|terminal|^exec/.test(name))
    return { kind: "command", busy: "正在运行", done: "已运行" };
  if (/imagegen|generate.?image|生图|画图/.test(name))
    return { kind: "image", busy: "正在生成图片", done: "已生成图片" };
  if (/browser|fetch|web.?search|navigate/.test(name))
    return { kind: "web", busy: "正在浏览", done: "已浏览" };
  if (/write|edit|patch/.test(name))
    return { kind: "edit", busy: "正在编辑", done: "已编辑" };
  if (/read|attachment/.test(name))
    return { kind: "read", busy: "正在读取", done: "已读取" };
  if (/search|grep|glob|find/.test(name))
    return { kind: "search", busy: "正在搜索", done: "已搜索" };
  if (/workflow|sop/.test(name))
    return { kind: "workflow", busy: "正在处理 SOP", done: "已处理 SOP" };
  if (/list|tree/.test(name))
    return { kind: "search", busy: "正在查看", done: "已查看" };
  return {
    kind: "tool",
    busy: "正在调用工具",
    done: "已调用工具",
  };
}

// Keep the full payload in the expanded record; the row shows only its subject.
export function activityTarget(step) {
  if (step.kind === "thinking") return thoughtLine(step.text, !step.finishedAt);
  if (step.kind !== "tool" || !step.detail) return "";
  const tool = (step.tool || "").toLowerCase();
  if (
    /toolsearch|search_tools|tools_search|tool_schema|lookup_tool_param_schema/.test(
      tool,
    )
  )
    return "";
  let detail = step.detail.trim();
  if (/^[{[]/.test(detail)) {
    try {
      const value = JSON.parse(detail);
      const args = value.arguments || value;
      detail =
        args.file_path ||
        args.filePath ||
        args.command ||
        args.cmd ||
        args.query ||
        args.pattern ||
        args.path?.file ||
        (typeof args.path === "string" ? args.path : "") ||
        args.body?.name ||
        args.body?.title ||
        args.goal ||
        "";
    } catch {
      if (activityFace(step).kind !== "command") return "";
    }
  }
  if (typeof detail !== "string") return "";
  detail = detail.split("\n")[0].trim();
  if (/mcp__|agentflow__/.test(detail)) return "";
  const kind = activityFace(step).kind;
  if (["read", "edit"].includes(kind) && !/\s/.test(detail))
    return detail.split(/[\\/]/).filter(Boolean).at(-1) || detail;
  return detail;
}

function replyText(message) {
  try {
    return JSON.parse(message.content).message || message.content;
  } catch {
    return message.content;
  }
}

// One-line headline of a thought: its latest bold heading, otherwise the
// newest line while streaming and the opening line once finished.
export function thoughtLine(text = "", streaming = false) {
  const headings = [...text.matchAll(/\*\*([^*\n]{2,80})\*\*/g)];
  if (headings.length) return headings.at(-1)[1].trim();
  const lines = text
    .split("\n")
    .map((line) => line.replace(/[*#`>]+/g, "").trim())
    .filter(Boolean);
  return (streaming ? lines.at(-1) : lines[0]) || "";
}
