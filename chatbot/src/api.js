import { mergeActivity } from "./activity.js";
import { actionName } from "./platform.js";
export async function api(path, options = {}) {
  const headers = { accept: "application/json", ...options.headers };
  if (options.body && !(options.body instanceof FormData))
    headers["content-type"] = "application/json";
  const response = await fetch(path, {
    credentials: "same-origin",
    ...options,
    headers,
  });
  const data =
    response.status === 204 ? null : await response.json().catch(() => null);
  if (!response.ok)
    throw Object.assign(
      new Error(data?.error || `请求失败（${response.status}）`),
      { status: response.status },
    );
  return data;
}
export const write = (path, body, method = "POST") =>
  api(path, { method, body: JSON.stringify(body) });
export const sessionPath = (id) =>
  `/api/assistant/sessions/${encodeURIComponent(id)}`;
export function elapsedSeconds(startedAt, finishedAt) {
  const start = Date.parse(startedAt);
  const end = Date.parse(finishedAt);
  if (
    !Number.isFinite(start) ||
    !Number.isFinite(end) ||
    start <= 0 ||
    end < start
  )
    return null;
  return Math.floor((end - start) / 1000);
}
export function formatElapsed(seconds) {
  if (seconds < 1) return "不到1秒";
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const rest = seconds % 60;
  return `${hours ? `${hours}小时` : ""}${minutes ? `${minutes}分钟` : ""}${rest || (!hours && !minutes) ? `${rest}秒` : ""}`;
}
export const attachmentURL = (attachment, sessionId) =>
  attachment.id?.startsWith("local-attachment-") &&
  /^data:[^,]*;base64,/.test(attachment.localDataUrl || "")
    ? attachment.localDataUrl
    : `/api/assistant/attachments/${encodeURIComponent(attachment.id)}?sessionId=${encodeURIComponent(sessionId)}`;
export function newId() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return (
    "chat-" + Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")
  );
}
export function parseReply(message) {
  try {
    const value = JSON.parse(message.content);
    return typeof value.message === "string"
      ? value
      : { message: message.content, drafts: [] };
  } catch {
    return { message: message.content, drafts: [] };
  }
}
export function visibleText(message) {
  let text = parseReply(message).message || "";
  if (message.attachments?.some((a) => a.mimeType.startsWith("image/"))) {
    text = text
      .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
      .replace(/!\[[^\]]*\]\)/g, "");
    text = text
      .split("\n")
      .filter(
        (line) =>
          !(
            /(?:本地路径|文件路径|文件位置)/.test(line) &&
            /(?:generated-images|\/data\/|\/root\/)/.test(line)
          ),
      )
      .join("\n");
  }
  return text;
}
export function collectArtifacts(session) {
  if (!session) return [];
  const artifacts = [];
  const seen = new Set();
  (session.messages || []).forEach((message, messageIndex) => {
    for (const attachment of message.attachments || []) {
      if (seen.has(attachment.id)) continue;
      seen.add(attachment.id);
      artifacts.push({
        ...attachment,
        kind: "attachment",
        source: message.role === "assistant" ? "AI 生成" : "上传的文件",
        url: attachmentURL(attachment, session.id),
        messageIndex,
      });
    }
    if (message.role !== "assistant") return;
    for (const asset of message.assets || []) {
      if (asset.kind === "run") continue;
      artifacts.push({
        id: `asset-${asset.kind}-${asset.id}`,
        name: asset.name || asset.id,
        kind: asset.kind,
        asset,
        resourceKey: `${asset.kind}:${asset.id}`,
        source: asset.status === "published" ? "已发布" : "已保存",
        mimeType: "application/json",
      });
    }
    const reply = parseReply(message);
    for (const [index, draft] of (reply.drafts || []).entries()) {
      const kind = draft.tool?.startsWith("workflows_")
        ? "sop"
        : draft.tool?.startsWith("agents_")
          ? "role"
          : "operation";
      const resource = draft.savedResult?.data || draft.savedResult;
      artifacts.push({
        id: `draft-${messageIndex}-${index}`,
        name: draft.body?.name || draft.body?.title || actionName(draft.tool),
        kind,
        resourceKey:
          draft.saved && resource?.id && ["sop", "role"].includes(kind)
            ? `${kind}:${resource.id}`
            : null,
        asset:
          draft.saved && resource?.id && ["sop", "role"].includes(kind)
            ? {
                kind,
                id: resource.id,
                name: resource.name,
                status: resource.status,
              }
            : null,
        mimeType: "application/json",
        content: JSON.stringify(draft.body || draft, null, 2),
        draft,
        messageIndex,
        draftIndex: index,
        source: draft.saved ? "已执行" : "待确认",
      });
    }
    // workbench_workspace_submit_mr only appears in the reply text (its tool
    // result is not persisted), so the artifact panel derives pull / merge
    // requests from those links instead of hiding them until the tool is re-run.
    const reviewLinks = [
      [/https:\/\/github\.com\/([A-Za-z0-9._-]+\/[A-Za-z0-9._-]+)\/pull\/(\d+)/g, "#", "PR"],
      [/https?:\/\/git\.example\.com\/([A-Za-z0-9._\-\/]+?)(?:\/-)?\/merge_requests\/(\d+)/g, "!", "MR"],
    ];
    for (const [pattern, marker, label] of reviewLinks)
      for (const match of (reply.message || "").matchAll(pattern)) {
        const [, repoPath, iid] = match;
        const key = `mr-${repoPath}/${iid}`.toLowerCase();
        if (seen.has(key)) continue;
        seen.add(key);
        const repo = repoPath.split("/").filter(Boolean).pop() || repoPath;
        artifacts.push({
          id: key,
          name: `${repo} ${marker}${iid}`,
          kind: "merge-request",
          url: match[0],
          external: true,
          source: `${label} · ${repoPath}`,
          mimeType: "text/html",
          messageIndex,
        });
      }
    const blocks = (reply.message || "").matchAll(
      /```([^\n]*)\n([\s\S]*?)```/g,
    );
    for (const [index, match] of [...blocks].entries()) {
      const label = match[1].trim();
      const language = label.split(/\s/)[0].toLowerCase();
      const type = ["mermaid", "svg", "html", "slides", "ppt"].includes(
        language,
      )
        ? language
        : "code";
      const extension =
        {
          mermaid: "mmd",
          svg: "svg",
          html: "html",
          javascript: "js",
          typescript: "ts",
          python: "py",
          slides: "pptx",
          ppt: "pptx",
          json: "json",
        }[language] ||
        language ||
        "txt";
      const named = label.match(/(?:filename=|title=)["']?([^\s"']+)/)?.[1];
      artifacts.push({
        id: `block-${messageIndex}-${index}`,
        name:
          named ||
          `${{ svg: "图像", mermaid: "流程图", html: "网页", slides: "演示文稿", ppt: "演示文稿" }[language] || "代码"}-${messageIndex + 1}-${index + 1}.${extension}`,
        kind: type,
        content: match[2],
        language,
        mimeType: type === "svg" ? "image/svg+xml" : "text/plain",
        messageIndex,
        source: "AI 生成",
      });
    }
  });
  const merged = [],
    byResource = new Map();
  for (const artifact of artifacts) {
    if (artifact.resourceKey && byResource.has(artifact.resourceKey)) {
      const old = byResource.get(artifact.resourceKey);
      if (artifact.draft || !old.draft) Object.assign(old, artifact);
      else if (artifact.asset) {
        old.asset = artifact.asset;
        old.name = artifact.name;
      }
    } else {
      merged.push(artifact);
      if (artifact.resourceKey) byResource.set(artifact.resourceKey, artifact);
    }
  }
  return merged;
}
export function projectChange(current, nextProjectId) {
  if (!current) return "draft";
  if ((current.projectId || "") === nextProjectId) return "same";
  return !current.projectId &&
    nextProjectId &&
    !current.shared &&
    !current.runId
    ? "bind"
    : "new";
}
// Mirrors workbenchProjectAllowsUser on the server: owners, members and
// platform admins may chat in a project; a project with neither an owner nor
// members is open to everyone.
export function canChatInProject(project, user) {
  if (!project) return true;
  if (!user) return false;
  if (user.isPlatformAdmin) return true;
  if (String(user.platformRole || "").toLowerCase() === "admin") return true;
  const members = project.members || [];
  if (!project.ownerId && !members.length) return true;
  return (
    project.ownerId === user.id || members.some((m) => m.userId === user.id)
  );
}
export const UNJOINED_PROJECTS_SECTION = "未加入的项目";
// Projects the user can chat in keep their original order; the rest follow
// under their own section without a per-row explanation.
export function projectPickerOptions(projects, user, session) {
  const available = [];
  const unjoined = [];
  const effect = {
    bind: "绑定当前会话到此项目",
    new: "在此项目新开对话",
  };
  for (const p of projects || []) {
    if (canChatInProject(p, user)) {
      available.push({
        id: p.id,
        label: p.name,
        icon: "folder",
        description: session ? effect[projectChange(session, p.id)] : undefined,
      });
    } else {
      unjoined.push({
        id: p.id,
        label: p.name,
        icon: "folder",
        section: UNJOINED_PROJECTS_SECTION,
        unavailable: true,
      });
    }
  }
  return [
    { id: "", label: "个人对话", description: "不绑定项目", icon: "user" },
    ...available,
    ...unjoined,
  ];
}
export function mergeEvents(previous, events) {
  let answer = previous.plainAnswer ?? previous.answer ?? "";
  let draftBuffer = previous.draftBuffer || "";
  let draftMessage = previous.draftMessage || "";
  let thinking = previous.thinking || "";
  const tools = (previous.tools || []).map((tool) => ({ ...tool }));
  const ids = new Set(tools.map((tool) => tool.id || tool.tool));
  for (const { data: event } of events || []) {
    if (event.kind === "answerReset") {
      answer = "";
      draftBuffer = "";
      draftMessage = "";
    }
    if (event.kind === "draftStart") {
      draftBuffer = "";
      draftMessage = "";
    }
    if (event.kind === "draftDelta") {
      draftBuffer += event.detail || "";
      draftMessage = partialReplyMessage(draftBuffer);
    }
    if (
      event.kind === "answer" &&
      event.text &&
      !(event.full && answer.includes(event.text.trim()))
    )
      answer += event.text;
    if (
      event.kind === "thinking" &&
      event.text &&
      !(event.full && thinking.includes(event.text.trim()))
    )
      thinking += event.text;
    if (event.kind === "tool" && !ids.has(event.id || event.tool)) {
      tools.push(event);
      ids.add(event.id || event.tool);
    }
    if (event.kind === "toolDone") {
      const tool = tools.find((item) => item.id === event.id);
      if (tool) {
        tool.done = true;
        tool.failed = !!event.failed;
      }
    }
  }
  return {
    answer: draftMessage || answer,
    plainAnswer: answer,
    draftBuffer,
    draftMessage,
    thinking,
    tools,
    activity: mergeActivity(previous.activity, events),
  };
}
export function partialReplyMessage(raw) {
  const match = /"message"\s*:\s*"/.exec(raw);
  if (!match) return "";
  const rest = raw.slice(match.index + match[0].length - 1);
  let safe = 1;
  for (let i = 1; i < rest.length; ) {
    if (rest[i] === '"') {
      try {
        return JSON.parse(rest.slice(0, i + 1));
      } catch {
        return "";
      }
    }
    if (rest[i] === "\\") {
      const count = rest[i + 1] === "u" ? 6 : 2;
      if (i + count > rest.length) break;
      i += count;
    } else i++;
    safe = i;
  }
  try {
    return JSON.parse(rest.slice(0, safe) + '"');
  } catch {
    return "";
  }
}
export function restoreStream(snapshot) {
  return {
    ...mergeEvents(
      {},
      (snapshot.activity || []).map((data, sequence) => ({ data, sequence })),
    ),
    answer: snapshot.draftMessage || snapshot.answer || "",
    plainAnswer: snapshot.answer || "",
    draftMessage: snapshot.draftMessage || "",
    draftBuffer: snapshot.draftJSON || "",
  };
}

// Keep the confirmation acknowledgement visible without presenting an internal
// model-resume instruction as something the user typed. Validate the preceding
// saved receipt; ordinary user text must never be hidden by a prefix match.
export function confirmationCaption(message, previous) {
  if (message?.role !== "user" || previous?.role !== "assistant") return "";
  const match =
    /^已确认并执行 ([a-z_]+)。请核对这条草稿的保存回执，继续完成原始请求；不要重复刚才的写入，下一项需要确认时提交草稿，全部完成时总结实际结果。$/.exec(
      message.content || "",
    );
  if (
    !match ||
    !parseReply(previous).drafts?.some((d) => d.saved && d.tool === match[1])
  )
    return "";
  return `已确认${actionName(match[1])}，继续处理`;
}
