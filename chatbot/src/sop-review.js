import { api } from "./api.js";

export const runPath = (runId) => `/api/runs/${encodeURIComponent(runId)}`;

export function openRunPage(runId, nodeId) {
  if (window.parent !== window) {
    window.parent.postMessage(
      { type: "agentflow-chat-open-view", view: "runs", runId, nodeId },
      location.origin,
    );
  } else {
    const node = nodeId ? `/${encodeURIComponent(nodeId)}` : "";
    location.href = `/#/runs/${encodeURIComponent(runId)}${node}`;
  }
}

export function defaultReviewNode(run) {
  const nodes = run.workflowSnapshot?.nodes || [];
  for (const status of ["waiting_approval", "failed", "running"]) {
    const active = nodes.find(({ id }) => run.nodes?.[id]?.status === status);
    if (active) return active.id;
  }
  const withFiles = new Set((run.artifacts || []).map((file) => file.nodeId));
  return (
    nodes.find(({ id }) => withFiles.has(id))?.id ||
    nodes.find(({ id }) => run.nodes?.[id]?.status === "succeeded")?.id ||
    nodes[0]?.id
  );
}

export function nodeExpectation(node) {
  return {
    status: node.status,
    attempt: node.attempt || 0,
    reworkCount: node.reworkCount || 0,
  };
}

export function canDiscussNode(node) {
  return [
    "succeeded",
    "failed",
    "waiting_approval",
    "skipped",
    "canceled",
    "cancelled",
  ].includes(node.status);
}

export function upstreamNodeIds(workflow, nodeId, recursive = false) {
  const edges = workflow?.edges || [];
  const nodes = workflow?.nodes || [];
  if (!edges.length) {
    const index = nodes.findIndex(({ id }) => id === nodeId);
    return index > 0 ? [nodes[index - 1].id] : [];
  }
  const seen = new Set([nodeId]);
  const queue = [nodeId];
  for (let i = 0; i < queue.length; i++) {
    for (const { source, target } of edges) {
      if (target !== queue[i] || seen.has(source)) continue;
      seen.add(source);
      queue.push(source);
    }
    if (!recursive) break;
  }
  return queue.slice(1);
}

export function canReplayNode(run, node) {
  return (
    canDiscussNode(node) &&
    ["waiting", "failed", "cancelled", "succeeded"].includes(run.status) &&
    !run.pauseRequested &&
    !Object.values(run.nodes || {}).some((item) =>
      ["running", "pending_input"].includes(item?.status),
    ) &&
    (!run.workflowSnapshot?.edges?.length ||
      upstreamNodeIds(run.workflowSnapshot, node.nodeId, true).every(
        (id) => run.nodes?.[id]?.status === "succeeded",
      ))
  );
}

export function nodeReviewArtifacts(run, node) {
  const own = (run.artifacts || []).filter(
    ({ nodeId }) => nodeId === node.nodeId,
  );
  const upstream =
    node.status === "waiting_approval"
      ? new Set(upstreamNodeIds(run.workflowSnapshot, node.nodeId, true))
      : new Set();
  const handoffs = (run.artifacts || []).filter(({ nodeId, attempt }) => {
    const source = run.nodes?.[nodeId];
    return (
      upstream.has(nodeId) &&
      source?.status === "succeeded" &&
      (!source.attempt || !attempt || attempt === source.attempt)
    );
  });
  return { own, handoffs };
}

export function runArtifact(run, artifact) {
  const name = artifact.path || artifact.name;
  const kind = /\.html?$/i.test(name)
    ? "html"
    : /\.svg$/i.test(name)
      ? "svg"
      : /\.(mmd|mermaid)$/i.test(name)
        ? "mermaid"
        : "attachment";
  return {
    ...artifact,
    name,
    kind,
    url: `${runPath(run.id)}/artifacts/${encodeURIComponent(artifact.id)}`,
    mimeType: artifact.mediaType,
    source: `节点产物 · 第 ${artifact.attempt || 1} 次执行`,
  };
}

function nodeConversationPath(run, node) {
  return `${runPath(run.id)}/nodes/${encodeURIComponent(node.nodeId)}/conversation`;
}

export function findNodeConversation(run, node) {
  const query = new URLSearchParams(nodeExpectation(node));
  return api(`${nodeConversationPath(run, node)}?${query}`);
}

export function openNodeConversation(run, node) {
  return api(nodeConversationPath(run, node), {
    method: "POST",
    body: JSON.stringify(nodeExpectation(node)),
  });
}

export async function decideRunNode(run, node, action, details, onRestoring) {
  const path = runPath(run.id);
  const headers =
    action === "retry" && run.executionContext?.projectId
      ? { Prefer: "respond-async" }
      : {};
  const options = {
    method: "POST",
    headers,
    body: JSON.stringify({
      nodeId: node.nodeId,
      expected: nodeExpectation(node),
      ...details,
    }),
  };
  let result = await api(`${path}/${action}`, options);
  if (result?.workspaceRestore?.status === "restoring") {
    onRestoring?.();
    let status = "restoring";
    while (status === "restoring") {
      await new Promise((resolve) => setTimeout(resolve, 1000));
      const restore = await api(`${path}/workspace/restore`);
      status = restore.status;
      if (status !== "restoring" && status !== "ready")
        throw new Error(
          restore.error || "工作区恢复状态已变化，请重新发起操作",
        );
    }
    result = await api(`${path}/${action}`, options);
    if (result?.workspaceRestore?.status === "restoring")
      throw new Error("工作区仍在恢复，请稍后重试");
  }
  return result;
}
