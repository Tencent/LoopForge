// Troubleshooting notes copied from a chat message (issue #221). The trace ID
// is the generation that sent the question and wrote its reply; operators look
// it up in assistant_generations. Everything here is read on demand so copying
// never slows down sending or rendering.
import { api } from "./api.js";
import { NOT_REPORTED, fullTime } from "./message-info.js";

const clean = (value) => String(value ?? "").trim();

/** The trace ID of a message, available as soon as the question is sent. */
export function messageTraceId(messages, index, generation) {
  const message = messages?.[index];
  if (!message) return "";
  const own = clean(message.traceId);
  if (own) return own;
  if (message.role !== "user") return "";
  const reply = messages[index + 1];
  if (reply?.role === "assistant" && clean(reply.traceId))
    return clean(reply.traceId);
  // The question still on screen before the stored transcript comes back.
  if (generation?.id && Number(generation.messageCount) === index + 1)
    return clean(generation.id);
  return "";
}

let commit = "";
let commitRequest = null;

/** Starts reading the platform build once; later copies reuse it. */
export function loadPlatformCommit(read = api) {
  if (!commitRequest)
    commitRequest = Promise.resolve()
      .then(() => read("/api/health"))
      .then((health) => {
        commit = clean(health?.commit);
        return commit;
      })
      .catch(() => {
        commitRequest = null;
        return "";
      });
  return commitRequest;
}

export const platformCommit = () => commit;

export function resetPlatformCommit() {
  commit = "";
  commitRequest = null;
}

function networkText(nav) {
  if (!nav) return NOT_REPORTED;
  const parts = [nav.onLine === false ? "离线" : "在线"];
  const link = nav.connection;
  if (link?.effectiveType) parts.push(link.effectiveType);
  if (Number.isFinite(link?.rtt)) parts.push(`RTT ${link.rtt}ms`);
  if (Number.isFinite(link?.downlink) && link.downlink > 0)
    parts.push(`下行 ${link.downlink}Mbps`);
  return parts.join(" · ");
}

/** What the browser can tell about where the message was sent from. */
export function browserEnvironment(scope = globalThis) {
  const nav = scope.navigator;
  const loc = scope.location;
  let zone = "";
  try {
    zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {}
  return {
    // Origin and path only: query strings can carry one-off tokens.
    page: loc?.origin && loc.origin !== "null" ? `${loc.origin}${loc.pathname || ""}` : "",
    network: networkText(nav),
    browser: clean(nav?.userAgent),
    locale: [zone, clean(nav?.language)].filter(Boolean).join(" · "),
    version: platformCommit(),
  };
}

/** Plain-text notes for one message; missing values read 「未上报」. */
export function traceDiagnostics({
  messages,
  index,
  generation,
  session,
  environment = browserEnvironment(),
}) {
  const message = messages?.[index] || {};
  const user = message.role === "user";
  const reply = user ? null : message;
  const or = (value) => clean(value) || NOT_REPORTED;
  const lines = [
    ["Trace ID", or(messageTraceId(messages, index, generation))],
    ["会话 ID", or(session?.id)],
    ["消息", `第 ${index + 1} 条 · ${user ? "提问" : "回复"}`],
    ["时间", fullTime(message.createdAt || message.startedAt)],
    ["Agent", clean(reply?.speakerName) || "主 Bot"],
    ["执行器", or(session?.executor)],
    ["模型", or(reply ? reply.model : session?.model)],
  ];
  if (session?.projectId) lines.push(["项目", clean(session.projectId)]);
  if (reply?.runIds?.length) lines.push(["关联运行", reply.runIds.join(", ")]);
  if (reply?.unfinished) lines.push(["状态", clean(reply.unfinished)]);
  lines.push(
    ["平台版本", or(environment.version)],
    ["页面", or(environment.page)],
    ["网络", or(environment.network)],
    ["浏览器", or(environment.browser)],
    ["时区/语言", or(environment.locale)],
  );
  return ["LoopForge 排障信息", ...lines.map(([k, v]) => `${k}: ${v}`)].join("\n");
}
