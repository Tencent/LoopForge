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
    // A subagent's own steps and report nest under the Agent call that
    // dispatched it, so Main Bot's process stays its own.
    if (event.parent) {
      let host = steps.find((step) => step.id === event.parent);
      if (!host) {
        host = { id: event.parent, kind: "tool", tool: "Agent", detail: "", text: "", startedAt: at };
        steps.push(host);
      }
      const child = { ...event, parent: undefined };
      if (child.kind === "agentText") child.kind = "text";
      host.children = mergeActivity(host.children || [], [{ data: child, sequence }]);
      continue;
    }
    if (event.kind === "answerReset") {
      for (let i = steps.length - 1; i >= 0; i--) {
        if (steps[i].kind === "text") steps.splice(i, 1);
      }
    }
    if ((event.kind === "answer" || event.kind === "text") && event.text) {
      const incoming = event.text;
      const trimmed = incoming.trim();
      // A finished assistant block repeats a paragraph that already streamed
      // before the tool. Keep the original position.
      if (
        event.full &&
        trimmed &&
        steps.some((step) => step.kind === "text" && step.text.includes(trimmed))
      ) {
        continue;
      }
      let last = steps.at(-1);
      const same =
        last &&
        last.kind === "text" &&
        !last.finishedAt &&
        (!event.id || event.id === last.id);
      if (!same) {
        closeThinking(at);
        last = {
          id: event.id || `text-${sequence ?? steps.length}`,
          kind: "text",
          text: "",
          startedAt: at,
        };
        steps.push(last);
      }
      if (event.full && trimmed && last.text.includes(trimmed)) {
        // The complete block repeated text already streamed.
      } else if (
        event.full &&
        last.text.trim() &&
        trimmed.startsWith(last.text.trim())
      ) {
        last.text = incoming;
      } else last.text += incoming;
    }
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
        if (event.subagent) existing.subagent = event.subagent;
        if (event.role) existing.role = event.role;
        if (event.input) existing.input = event.input;
        // A resumed dispatch names its agent before it finishes. Keep the
        // target apart: the CLI may still finish it under a new agent.
        if (event.agentId) existing.agentId = existing.resumed = event.agentId;
      } else
        steps.push({
          id,
          kind: "tool",
          tool: event.tool,
          detail: event.detail || "",
          text: event.text || "",
          startedAt: at,
          ...(event.subagent ? { subagent: event.subagent } : {}),
          ...(event.role ? { role: event.role } : {}),
          ...(event.input ? { input: event.input } : {}),
          ...(event.agentId ? { agentId: event.agentId, resumed: event.agentId } : {}),
        });
    }
    if (event.kind === "toolDone") {
      const step = steps.find((step) => step.id === event.id);
      if (step) {
        step.finishedAt = at || "done";
        step.failed = !!event.failed;
        if (event.agentId) step.agentId = event.agentId;
        if (event.output) step.output = event.output;
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
// Paragraphs stay where the model wrote them, with the tools that followed.
export function replyBlocks(steps = []) {
  const blocks = [];
  for (const step of steps) {
    if (step.kind === "text") {
      if (!step.text?.trim()) continue;
      blocks.push({ type: "text", id: step.id, text: step.text });
      continue;
    }
    const last = blocks.at(-1);
    if (last?.type === "activity") last.steps.push(step);
    else
      blocks.push({
        type: "activity",
        id: step.id || `activity-${blocks.length}`,
        steps: [step],
      });
  }
  return blocks;
}
export function hasTextSegments(steps = []) {
  return steps.some((step) => step.kind === "text" && step.text?.trim());
}
function replyFold(text) {
  return String(text || "").replace(/\s+/g, "");
}
// A finished reply sometimes stores the last paragraph twice: once from the
// stream and again from the CLI result. Distinct paragraphs stay.
function dropRepeatedReply(text) {
  let parts = String(text || "")
    .trim()
    .split(/\n{2,}/)
    .map((part) => part.trim())
    .filter(Boolean);
  while (parts.length >= 2 && replyFold(parts.at(-1)) === replyFold(parts.at(-2))) {
    parts.pop();
  }
  if (parts.length >= 2 && parts.length % 2 === 0) {
    const mid = parts.length / 2;
    const left = replyFold(parts.slice(0, mid).join("\n\n"));
    const right = replyFold(parts.slice(mid).join("\n\n"));
    if (left && left === right) parts = parts.slice(0, mid);
  }
  return parts.join("\n\n");
}
function replyTail(text, head) {
  if (!text || !head || !text.startsWith(head)) return "";
  return text.slice(head.length).trim();
}
function lastParagraph(text) {
  const parts = String(text || "")
    .trim()
    .split(/\n{2,}/)
    .map((part) => part.trim())
    .filter(Boolean);
  return parts.at(-1) || "";
}
// Same letters with different spaces: keep the copy Markdown can still parse.
function richerFold(primary, alternate) {
  if (replyFold(primary) !== replyFold(alternate)) return primary;
  return alternate.length > primary.length ? alternate : primary;
}
// The answer under the trace is only the latest paragraph. Earlier paragraphs
// stay inside the expandable process, in the order they were written.
export function finalReply(full, steps = []) {
  const parts = steps.filter((step) => step.kind === "text" && step.text?.trim());
  const text = dropRepeatedReply(full || "");
  if (!parts.length) return text;
  const last = parts.at(-1).text.trim();
  if (!text) return last;
  const covered = parts.map((step) => step.text.trim()).join("\n\n");
  const same =
    replyFold(text) === replyFold(covered) || replyFold(text) === replyFold(last);
  if (text === covered || text.startsWith(`${covered}\n`) || same) {
    const suffix = replyTail(text, covered);
    if (
      !suffix ||
      replyFold(suffix) === replyFold(last) ||
      replyFold(suffix) === replyFold(covered)
    ) {
      if (replyFold(text) === replyFold(last)) return richerFold(last, text);
      const tail = lastParagraph(text);
      if (replyFold(tail) === replyFold(last)) return richerFold(last, tail);
      return last;
    }
    return dropRepeatedReply(`${last}\n\n${suffix}`);
  }
  if (text === last || text.startsWith(`${last}\n`)) {
    const suffix = replyTail(text, last);
    if (!suffix || replyFold(suffix) === replyFold(last)) return last;
    return dropRepeatedReply(text);
  }
  return last;
}
// Paragraphs and the tool rounds between them, without connection status and
// empty finished thoughts.
function visibleBlocks(steps, live) {
  return replyBlocks(steps)
    .map((block) => {
      if (block.type !== "activity") return block;
      const kept = block.steps.filter(
        (step) =>
          step.kind !== "status" &&
          (step.kind !== "thinking" ||
            step.text ||
            (live && !step.finishedAt)),
      );
      return kept.length ? { ...block, steps: kept } : null;
    })
    .filter(Boolean);
}
// processBlocks lists a subagent's whole process, its report included: unlike
// Main Bot's trace, no paragraph is lifted out as the answer.
export function processBlocks(steps = [], { live = false } = {}) {
  return visibleBlocks(steps, live);
}
export const isDispatch = (step) =>
  step?.kind === "tool" && /^(task|agent)$/i.test(step.tool || "");
// A resume that did not take: the CLI finished the dispatch under another
// agent than the one Main Bot asked to continue.
export const resumeMissed = (step) =>
  !!(step.resumed && step.finishedAt && step.agentId && step.agentId !== step.resumed);
// collectSubagents lists every subagent of a conversation, newest last. A
// dispatch that resumed an earlier agent joins it, so one entry holds every
// task that agent was given.
export function collectSubagents(messages = [], liveSteps = []) {
  const agents = [];
  // messageIndex is the saved reply that holds the dispatch; a live one has
  // none yet, and its collected files are not attachments until it is saved.
  const add = (step, messageIndex = null) => {
    const key = step.agentId || step.id;
    let agent = agents.find((item) => item.key === key || (step.agentId && item.agentId === step.agentId));
    if (!agent) {
      agent = { key, agentId: step.agentId || "", role: "", subagent: "", dispatches: [], published: [] };
      agents.push(agent);
    }
    if (step.agentId) agent.agentId = step.agentId;
    agent.role = step.role || agent.role;
    agent.subagent = step.subagent || agent.subagent;
    agent.dispatches.push(step);
    // What it published, and the role deliverables the platform collected
    // when it finished (a required one it did not write is marked missing).
    // A later dispatch that delivers the same file replaces the earlier row.
    for (const child of step.children || []) {
      if (child.kind !== "tool" || !child.detail) continue;
      let item = null;
      if (child.tool === "发布产物" && !child.failed) item = { title: child.detail, source: "已发布" };
      if (child.tool === "交付物")
        item = { title: child.detail, source: "交付物", missing: !!child.failed, messageIndex };
      if (!item) continue;
      agent.published = agent.published.filter((old) => old.source !== item.source || old.title !== item.title);
      agent.published.push(item);
    }
  };
  messages.forEach((message, index) => {
    if (message.role !== "assistant") return;
    savedActivity(message)
      .filter(isDispatch)
      .forEach((step) => add(step, index));
  });
  liveSteps.filter(isDispatch).forEach((step) => add(step));
  return agents;
}
// While streaming, the latest paragraph is the answer only once nothing has
// followed it; a paragraph followed by tool calls is a progress note.
export function streamingAnswerOpen(steps = []) {
  return visibleBlocks(steps, true).at(-1)?.type === "text";
}
// Drop the paragraph shown as the answer. What remains is the process:
// earlier paragraphs stay visible, each round's tool calls fold into a group.
// While a reply streams, the group nothing has followed yet is the active one
// and lists every step; it folds once the model writes or finishes.
export function traceBlocks(steps = [], { live = false } = {}) {
  const blocks = visibleBlocks(steps, live);
  blocks.forEach((block, index) => {
    if (block.type === "activity")
      block.active = live && index === blocks.length - 1;
  });
  if (!live || blocks.at(-1)?.type === "text") {
    const bodyFold = replyFold(finalReply("", steps));
    for (let i = blocks.length - 1; i >= 0; i--) {
      if (blocks[i].type === "text") {
        blocks.splice(i, 1);
        break;
      }
    }
    // The answer paragraph can also be stored earlier as a duplicate.
    for (let i = blocks.length - 1; i >= 0; i--) {
      if (blocks[i].type === "text" && replyFold(blocks[i].text) === bodyFold) {
        blocks.splice(i, 1);
      }
    }
  }
  return blocks;
}
// One folded line for a round of tool calls, like Codex: the kinds of work
// in order, without counts, e.g. "已运行命令、读取文件". Thinking is named only
// when the round did nothing else.
const groupNouns = {
  运行: "运行命令",
  读取: "读取文件",
  编辑: "编辑文件",
  搜索: "搜索代码",
  浏览: "浏览网页",
  查看: "查看文件",
};
export function activityGroupSummary(steps = [], { active = false } = {}) {
  const parts = [];
  for (const step of steps) {
    if (step.kind === "thinking" || step.kind === "status") continue;
    const label = activityFace(step).done.replace(/^已/, "");
    const part = groupNouns[label] || label;
    if (!parts.includes(part)) parts.push(part);
  }
  if (!parts.length) return active ? "正在思考" : "已思考";
  const text =
    parts.length > 3 ? `${parts.slice(0, 3).join("、")}等` : parts.join("、");
  return active ? `正在${text}` : `已${text}`;
}
// The icon of the most frequent kind in a group.
export function activityGroupKind(steps = []) {
  const counts = {};
  for (const step of steps) {
    if (step.kind === "thinking" || step.kind === "status") continue;
    const kind = activityFace(step).kind;
    counts[kind] = (counts[kind] || 0) + 1;
  }
  const [kind] = Object.entries(counts).sort((a, b) => b[1] - a[1])[0] || [
    "thinking",
  ];
  return kind;
}
// Text that the saved reply has but the segments do not, usually a final
// summary that arrived only with the completed result.
export function trailingReply(full, steps = []) {
  const text = (full || "").trim();
  const parts = steps
    .filter((step) => step.kind === "text" && step.text?.trim())
    .map((step) => step.text.trim());
  if (!text) return "";
  if (!parts.length) return text;
  const covered = parts.join("\n\n");
  if (covered.includes(text) || text === covered) return "";
  if (text.startsWith(covered)) return text.slice(covered.length).trim();
  return "";
}
export function generationStored(messages, generation) {
  const start = Date.parse(generation?.startedAt || "");
  if (!Number.isFinite(start)) return false;
  return (messages || []).some((message) => {
    if (message.role !== "assistant") return false;
    if (message.generationId && generation?.id)
      return message.generationId === generation.id;
    const at = Date.parse(message.startedAt || "");
    return Number.isFinite(at) && Math.abs(at - start) < 5000;
  });
}
export const savedActivity = (message) =>
  mergeActivity(
    [],
    (message.activity || []).map((data, sequence) => ({ data, sequence })),
  );
export function activityForReply(message, generation, liveSteps = []) {
  if (message.activity?.length) return savedActivity(message);
  if (message.generationId && message.generationId !== generation?.id) return [];
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
  if (/^(task|agent)$/.test(name)) {
    const who = step.role || step.subagent;
    return who
      ? { kind: "role", busy: `「${who}」正在处理`, done: `「${who}」已完成` }
      : { kind: "role", busy: "正在委派子任务", done: "已委派子任务" };
  }
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
