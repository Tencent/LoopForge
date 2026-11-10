// CLIs that AI 对话 can run on. The list comes from GET /api/assistant/models
// (`engines`); models without `engine` run on CodeBuddy.
export const defaultEngine = "codebuddy";

// Bundled logo per CLI; an unknown CLI falls back to its model's brand.
const brands = {
  codebuddy: "codebuddy", codex: "codex", "claude-code": "claude",
  opencode: "opencode", cursor: "cursor", agy: "gemini", kimi: "kimi", pi: "pi", dsh: "deepseek", zcode: "zhipu",
};
export const engineBrand = (id) => brands[id] || "";

export const modelEngine = (model) => model?.engine || defaultEngine;

/** The CLI an ID runs on: the catalog entry, else the "cli:model" prefix. */
export function engineOfModel(id, models = []) {
  const listed = models.find((m) => m.id === id);
  if (listed) return modelEngine(listed);
  const prefix = /^([a-z][a-z0-9-]*):./.exec(id || "")?.[1];
  return prefix || defaultEngine;
}

export const engineModels = (models, engine) =>
  (models || []).filter((m) => modelEngine(m) === engine);

// Mirrors assistantEngineMissingKey in internal/httpapi/assistant_engines.go;
// credentialHint turns it into a "去配置" button.
export function engineMissingKeyMessage(engine) {
  if (engine.id === defaultEngine) return "请先在个人配置中配置自己的 CodeBuddy API Key";
  return `请先在个人配置中配置 ${engine.name} API Key 后再使用 ${engine.name} 模型`;
}

// CLIs a session Executor can name when the reply was saved before model prefixes.
const legacyEngines = new Set(["codex", "claude-code", "opencode", "cursor", "agy", "kimi", "pi", "dsh", "zcode"]);

function modelHasEnginePrefix(id) {
  const prefix = /^([a-z][a-z0-9-]*):./.exec(id || "")?.[1];
  return prefix === defaultEngine || legacyEngines.has(prefix);
}

function legacyEngine(id) {
  if (id === "claude") return "claude-code";
  return legacyEngines.has(id) ? id : "";
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
    if (modelHasEnginePrefix(m.model)) return engineOfModel(m.model, models);
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
