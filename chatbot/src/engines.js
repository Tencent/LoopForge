// CLIs that AI 对话 can run on. The list comes from GET /api/assistant/models
// (`engines`); models without `engine` run on CodeBuddy.
export const defaultEngine = "codebuddy";

// Main Bot can run on any configured agent CLI.
export const EXECUTORS = [
  { id: "codebuddy", icon: "sparkles", label: "CodeBuddy", detail: "使用 CodeBuddy 模型与个人 API Key。" },
  { id: "codex", icon: "terminal", label: "Codex", detail: "使用 Codex CLI 与个人 Codex API Key。" },
  { id: "claude-code", icon: "brain", label: "Claude Code", detail: "使用 Claude Code CLI 与个人 Claude Code API Key。" },
  { id: "opencode", icon: "terminal", label: "OpenCode", detail: "使用 OpenCode 与个人 API Key。" },
  { id: "cursor", icon: "terminal", label: "Cursor", detail: "使用 Cursor CLI 与个人 API Key。" },
  { id: "agy", icon: "sparkles", label: "Antigravity", detail: "使用 Antigravity 与个人 API Key。" },
  { id: "kimi", icon: "sparkles", label: "Kimi Code", detail: "使用 Kimi Code 与个人 API Key。" },
  { id: "pi", icon: "terminal", label: "Pi", detail: "使用 Pi 与个人 API Key。" },
  { id: "dsh", icon: "terminal", label: "DeepSeek Harness", detail: "使用 DeepSeek Harness 与个人 API Key。" },
  { id: "zcode", icon: "terminal", label: "ZCode", detail: "使用 ZCode 与个人 API Key。" },
];

// Bundled logo per CLI; an unknown CLI falls back to its model's brand.
const brands = {
  codebuddy: "codebuddy", codex: "codex", "claude-code": "claude",
  opencode: "opencode", cursor: "cursor", agy: "antigravity", kimi: "kimi-code", pi: "pi", dsh: "deepseek", zcode: "zhipu",
};
export const engineBrand = (id) => Object.hasOwn(brands, id) ? brands[id] : "";

export function personalEngineConfigured(user, engine) {
  switch (engine) {
    case "codebuddy": return Boolean(user?.codeBuddyApiKeyConfigured);
    case "codex": return Boolean(user?.codexApiKeyConfigured);
    case "claude-code": return Boolean(user?.claudeCodeApiKeyConfigured);
    default: return Boolean(user?.agentCliCredentials?.[engine]?.apiKeyConfigured);
  }
}

export const modelEngine = (model) => model?.engine || defaultEngine;

const modelEnginePrefix = (id) => /^([a-z][a-z0-9-]*):./.exec(id || "")?.[1];

/** The CLI an ID runs on: the catalog entry, else the "cli:model" prefix. */
export function engineOfModel(id, models = []) {
  const listed = models.find((m) => m.id === id);
  if (listed) return modelEngine(listed);
  return modelEnginePrefix(id) || defaultEngine;
}

export const concreteModel = (model, engine = modelEngine(model)) =>
  !!model?.id && model.id !== "default" && model.id !== `${engine}:default` &&
  (engine !== "codebuddy" || (model.id !== "default-model" && model.id !== "codebuddy:default-model"));

export const engineModels = (models, engine) =>
  (models || []).filter((m) => modelEngine(m) === engine && concreteModel(m, engine));

// Mirrors assistantEngineMissingKey in internal/httpapi/assistant_engines.go;
// credentialHint turns it into a "去配置" button.
export function engineMissingKeyMessage(engine) {
  if (engine.id === defaultEngine) return "请先在个人配置中配置自己的 CodeBuddy API Key";
  return `请先在个人配置中配置 ${engine.name} API Key 后再使用 ${engine.name} 模型`;
}

const executorLabels = new Map(EXECUTORS.map(({ id, label }) => [id, label]));

function legacyEngine(id) {
  id = String(id ?? "").trim().toLowerCase();
  if (id === "claude") return "claude-code";
  return id !== defaultEngine && executorLabels.has(id) ? id : "";
}

/** CLI label for a saved reply, including messages from before model prefixes. */
export function replyEngineLabel(model, legacyExecutor, speakerAgentId) {
  const label = executorLabels.get(modelEnginePrefix(model));
  if (label) return label;
  if (!model || speakerAgentId) return "";
  return executorLabels.get(legacyEngine(legacyExecutor) || defaultEngine);
}

/**
 * The CLI that wrote a conversation's latest own reply, "" before the first.
 * Mirrors assistantConversationEngine: a conversation stays on that CLI,
 * because its native session holds tool calls another CLI cannot resume.
 * An unprefixed model falls back to the session's stored executor.
 */
export function conversationEngine(messages, models = [], legacyExecutor = "") {
  for (let i = (messages?.length || 0) - 1; i >= 0; i--) {
    const m = messages[i];
    if (m?.role !== "assistant" || m.speakerAgentId || !m.model) continue;
    if (executorLabels.has(modelEnginePrefix(m.model))) return engineOfModel(m.model, models);
    const legacy = legacyEngine(legacyExecutor);
    if (legacy) return legacy;
    return engineOfModel(m.model, models);
  }
  return "";
}

export function engineDetail(engine, models) {
  if (!engine.configured) return `未配置 ${engine.name} API Key，选择后去个人配置填写`;
  const count = engineModels(models, engine.id).length;
  return count ? `${count} 个可用模型` : "暂无可用模型";
}
