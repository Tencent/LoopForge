// CLIs that AI 对话 can run on. The list comes from GET /api/assistant/models
// (`engines`); models without `engine` run on CodeBuddy.
export const defaultEngine = "codebuddy";

// Main Bot can run on any configured agent CLI; CodeBuddy stays the default.
export const EXECUTORS = [
  { id: "codebuddy", icon: "sparkles", label: "CodeBuddy", detail: "Dùng model CodeBuddy và API Key cá nhân." },
  { id: "codex", icon: "terminal", label: "Codex", detail: "Dùng Codex CLI và Codex API Key cá nhân." },
  { id: "claude-code", icon: "brain", label: "Claude Code", detail: "Dùng Claude Code CLI và Claude Code API Key cá nhân." },
  { id: "opencode", icon: "terminal", label: "OpenCode", detail: "Dùng OpenCode và API Key cá nhân." },
  { id: "cursor", icon: "terminal", label: "Cursor", detail: "Dùng Cursor CLI và API Key cá nhân." },
  { id: "agy", icon: "sparkles", label: "Antigravity", detail: "Dùng Antigravity và API Key cá nhân." },
  { id: "kimi", icon: "sparkles", label: "Kimi Code", detail: "Dùng Kimi Code và API Key cá nhân." },
  { id: "pi", icon: "terminal", label: "Pi", detail: "Dùng Pi và API Key cá nhân." },
  { id: "dsh", icon: "terminal", label: "DeepSeek Harness", detail: "Dùng DeepSeek Harness và API Key cá nhân." },
  { id: "zcode", icon: "terminal", label: "ZCode", detail: "Dùng ZCode và API Key cá nhân." },
];

// Bundled logo per CLI; an unknown CLI falls back to its model's brand.
const brands = {
  codebuddy: "codebuddy", codex: "codex", "claude-code": "claude",
  opencode: "opencode", cursor: "cursor", agy: "antigravity", kimi: "kimi-code", pi: "pi", dsh: "deepseek", zcode: "zhipu",
};
export const engineBrand = (id) => Object.hasOwn(brands, id) ? brands[id] : "";

export const modelEngine = (model) => model?.engine || defaultEngine;

const modelEnginePrefix = (id) => /^([a-z][a-z0-9-]*):./.exec(id || "")?.[1];

/** The CLI an ID runs on: the catalog entry, else the "cli:model" prefix. */
export function engineOfModel(id, models = []) {
  const listed = models.find((m) => m.id === id);
  if (listed) return modelEngine(listed);
  return modelEnginePrefix(id) || defaultEngine;
}

export const concreteModel = (model, engine = modelEngine(model)) =>
  !!model?.id && model.id !== "default" && model.id !== `${engine}:default`;

export const engineModels = (models, engine) =>
  (models || []).filter((m) => modelEngine(m) === engine && concreteModel(m, engine));

// Mirrors assistantEngineMissingKey in internal/httpapi/assistant_engines.go;
// credentialHint turns it into a "去配置" button.
export function engineMissingKeyMessage(engine) {
  if (engine.id === defaultEngine) return "请先在个人配置中配置自己的 CodeBuddy API Key";
  return `Hãy cấu hình API Key ${engine.name} trong cài đặt cá nhân trước khi dùng model ${engine.name}`;
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
  if (!engine.configured) return `Chưa cấu hình API Key ${engine.name}; chọn xong hãy điền trong cài đặt cá nhân`;
  const count = engineModels(models, engine.id).length;
  return count ? `${count} model khả dụng` : "Chưa có model khả dụng";
}
