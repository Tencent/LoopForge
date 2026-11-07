export function nodeConversationItems(
  events,
  artifacts,
  node,
  { hasMore = false } = {},
) {
  const files = artifacts.filter((a) => a.nodeId === node.nodeId);
  const used = new Set();
  const items = [];
  const calls = new Map();
  let finalOutput = false;
  for (const event of events) {
    if (event.category === "artifact" && event.name === "artifact.published") {
      const id = event.attributes?.artifactId;
      const candidates = id
        ? files.filter((a) => a.id === id)
        : files.filter(
            (a) =>
              (a.name === event.message || a.path === event.message) &&
              (!event.context?.attempt || a.attempt === event.context.attempt),
          );
      if (candidates.length === 1 && !used.has(candidates[0].id)) {
        used.add(candidates[0].id);
        items.push({
          type: "artifact",
          artifact: candidates[0],
          key: `artifact-${candidates[0].id}`,
        });
      }
    } else if (event.category === "output" && event.message) {
      // Render agent replies in place, not as a second collapsed tool row.
      const same = event.message.trim() === node.output?.trim();
      if (!same || !finalOutput)
        items.push({
          type: "reply",
          message: event.message,
          at: event.timestamp,
          key: `reply-${event.sequence}`,
        });
      if (same) finalOutput = true;
    } else if (
      ["tool", "reasoning", "verification"].includes(event.category) &&
      event.message
    ) {
      // The hooks/metrics channels duplicate the executor's real tool calls.
      if (
        (event.source?.adapter === "agent-hooks-v1" ||
          event.source?.protocol === "otlp") &&
        event.category === "tool"
      ) {
        if (
          events.some(
            (e) =>
              e.category === "tool" &&
              ["anthropic-stream-v1", "codex-jsonl-v1"].includes(
                e.source?.adapter,
              ) &&
              e.tool?.callId,
          )
        )
          continue;
      }
      const call = event.category === "tool" && event.tool?.callId;
      if (call && calls.has(call)) {
        const item = calls.get(call);
        const previous = item.event;
        item.event = {
          ...previous,
          ...event,
          tool: { ...previous.tool, ...event.tool },
          request: previous.request || previous.message,
          startedAt: previous.startedAt || previous.timestamp,
        };
      } else {
        const item = {
          type: "activity",
          event,
          key: `event-${event.id || event.sequence}`,
        };
        items.push(item);
        if (call) calls.set(call, item);
      }
    }
  }
  if (node.output && !finalOutput && !hasMore)
    items.push({ type: "reply", message: node.output, key: "final-reply" });
  // Legacy runs without publication events still expose their files after the
  // saved reply; never infer a publication position from another node's name.
  for (const artifact of files)
    if (!hasMore && !used.has(artifact.id))
      items.push({
        type: "artifact",
        artifact,
        key: `artifact-${artifact.id}`,
      });
  return items;
}

const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// Adapt one executor event to the chatbot's activity step so SOP nodes share
// the Codex-style rows ("已运行 pwd", "已读取 SKILL.md", "已思考 …").
export function nodeActivityStep(event) {
  const id = `event-${event.id || event.sequence}`;
  const failed = event.tool?.success === false || event.level === "error";
  if (event.category === "reasoning")
    return { id, kind: "thinking", text: event.message, finishedAt: event.timestamp };
  if (event.category === "verification")
    return {
      id,
      kind: "tool",
      tool: "verification",
      detail: event.message,
      startedAt: event.timestamp,
      finishedAt: event.timestamp,
      failed,
    };
  const name =
    event.tool?.name || event.attributes?.tool || event.message.split(/\s/)[0];
  const call = (event.request || event.message || "").trim();
  const detail = call.startsWith(name) ? call.slice(name.length).trim() : call;
  const done = !!event.request || event.tool?.operation === "tool.result";
  const result = event.request ? (event.message || "").trim() : "";
  return {
    id,
    kind: "tool",
    tool: name,
    detail,
    output: new RegExp(`^${escapeRegExp(name)}\\s*(完成|done)?$`, "i").test(result)
      ? ""
      : result,
    startedAt: event.startedAt || event.timestamp,
    finishedAt: done ? event.timestamp : null,
    failed,
  };
}

// Fold consecutive executor activity between replies into one trace, like a
// Codex turn. Only the trailing trace of a running node stays live.
export function groupNodeActivity(items, { live = false } = {}) {
  const out = [];
  for (const item of items) {
    if (item.type !== "activity") {
      out.push(item);
      continue;
    }
    let trace = out.at(-1);
    if (trace?.type !== "trace") {
      trace = { type: "trace", key: `trace-${item.key}`, steps: [], events: [] };
      out.push(trace);
    }
    trace.steps.push(nodeActivityStep(item.event));
    trace.events.push(item.event);
  }
  out.forEach((item, index) => {
    if (item.type !== "trace") return;
    const first = item.events[0];
    const last = item.events.at(-1);
    item.startedAt = first.startedAt || first.timestamp;
    item.finishedAt = out[index + 1]?.at || last.timestamp;
    item.live = live && index === out.length - 1;
    delete item.events;
  });
  return out;
}
