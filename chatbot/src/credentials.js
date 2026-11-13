// Personal credentials configured under 个人配置 → 个人凭证. Field names match
// the inputs there so the parent page can scroll to and focus the right card.
// Keep the rules in sync with credentialHint in web/app/access.js.
export const CREDENTIALS = {
  codeBuddyApiKey: "CodeBuddy API Key",
  codexApiKey: "Codex API Key",
  claudeCodeApiKey: "Claude Code API Key",
  "agentCli.qoder": "Qoder CLI API Key",
  gitToken: "GitHub Git Token",
  gitSshKey: "Git SSH 密钥",
};

// A CodeBuddy key only works on the edition that issued it, and the CLI
// treats an unrecognized environment as 国际站. Keep in sync with
// CODEBUDDY_ENVIRONMENTS in web/app/workbench.js and
// domain.NormalizeCodeBuddyInternetEnvironment.
export const CODEBUDDY_ENVIRONMENTS = [
  {
    value: "",
    label: "国际站",
    site: "codebuddy.ai",
    keyUrl: "https://www.codebuddy.ai/profile/keys",
  },
  {
    value: "internal",
    label: "国内站",
    site: "copilot.tencent.com",
    keyUrl: "https://copilot.tencent.com/profile/",
  },
  {
    value: "ioa",
    label: "iOA",
    site: "tencent.sso.copilot.tencent.com",
    keyUrl: "https://tencent.sso.copilot.tencent.com/profile/keys",
  },
];

const CODEBUDDY_ENVIRONMENT_ALIASES = new Map([
  ["public", ""],
  ["intl", ""],
  ["international", ""],
  ["国际站", ""],
  ["china", "internal"],
  ["cn", "internal"],
  ["国内站", "internal"],
]);

// codeBuddyEnvironment returns null for a value the CLI would not recognize.
// The CLI receives the stored text as-is, so only the exact values count.
export function codeBuddyEnvironment(value) {
  const key = String(value || "").trim();
  return CODEBUDDY_ENVIRONMENTS.find((env) => env.value === key) || null;
}

// codeBuddyEnvironmentIntent guesses what an old free-text value meant
// (china → 国内站) so the fix can be preselected.
export function codeBuddyEnvironmentIntent(value) {
  const key = String(value || "").trim().toLowerCase();
  const normalized = CODEBUDDY_ENVIRONMENT_ALIASES.has(key)
    ? CODEBUDDY_ENVIRONMENT_ALIASES.get(key)
    : key;
  return CODEBUDDY_ENVIRONMENTS.find((env) => env.value === normalized) || null;
}

// codeBuddyEnvironmentLegacy is true for a stored value the CLI does not take
// as written; it connects to 国际站 until the value is saved again.
export function codeBuddyEnvironmentLegacy(value) {
  const raw = String(value || "").trim();
  return !!raw && !codeBuddyEnvironment(raw);
}

export function codeBuddyKeyURL(value) {
  return (codeBuddyEnvironmentIntent(value) || CODEBUDDY_ENVIRONMENTS[0])
    .keyUrl;
}

// Advisory only: a third-party model key is accepted but CodeBuddy answers it
// with 401.
export function codeBuddyKeyWarning(key) {
  const text = String(key || "").trim();
  if (!text || text.startsWith("ck_")) return "";
  return /^sk-/i.test(text)
    ? "这像是 DeepSeek、OpenAI 等第三方模型的 Key，CodeBuddy 会拒绝；第三方 Key 请在个人配置中改填到 Claude Code 或 Codex 凭证并设置 Base URL"
    : "CodeBuddy API Key 通常以 ck_ 开头，请确认复制的是 CodeBuddy 的 Key";
}

// Where and how to get each credential. Keep in sync with
// assistantCredentialRules in internal/httpapi/assistant_run_view.go.
export const CREDENTIAL_GUIDES = {
  gitSshKey: {
    steps: [
      "把专用公钥添加到代码托管账号，并按需授予仓库写权限",
      "到「个人配置 → Git SSH 密钥」为该主机添加无口令私钥，同一主机的仓库自动共用；请管理员信任该 SSH 主机",
      "回到这里重试",
    ],
  },
  gitToken: {
    url: "/#/settings",
    link: "连接 GitHub",
    steps: [
      "到「个人配置」点击「连接 GitHub」，复制设备码后前往 GitHub 授权",
      "添加仓库时点击「选择我的 GitHub 仓库」，选择仓库与主分支",
      "管理员尚未配置 GitHub 授权时，也可以手动填写有 Contents 和 Pull requests 读写权限的 PAT",
      "回到这里点「重试节点」",
    ],
  },
  codeBuddyApiKey: {
    url: CODEBUDDY_ENVIRONMENTS[0].keyUrl,
    link: "打开 CodeBuddy 创建 Key",
    steps: [
      "在 CodeBuddy 账号所在站点（国际站、国内站或 iOA）创建 API Key 并复制",
      "到「个人配置 → CodeBuddy API Key」选择同一网络环境，粘贴 Key 并保存；DeepSeek 等第三方模型的 Key 不能用在这里",
      "回到这里点「重试节点」",
    ],
  },
  codexApiKey: {
    steps: [
      "到「个人配置 → Codex API Key」填写 Key（及 Base URL）并保存，或把执行机器的认证方式切回本机登录",
      "回到这里点「重试节点」",
    ],
  },
  claudeCodeApiKey: {
    steps: [
      "到「个人配置 → Claude Code API Key」填写 Key（及 Base URL）并保存，或把执行机器的认证方式切回本机登录",
      "回到这里点「重试节点」",
    ],
  },
  "agentCli.qoder": {
    url: "https://qoder.com/account/integrations",
    link: "打开 Qoder 创建令牌",
    steps: ["在 Qoder 账号中创建个人访问令牌（PAT）", "到「个人配置 → Qoder CLI」保存令牌", "回到这里点「重试节点」"],
  },
};

// credentialGuide points the CodeBuddy link at the key page of the edition
// the credential owner has selected.
export function credentialGuide(field, owner) {
  const guide = CREDENTIAL_GUIDES[field];
  if (!guide || field !== "codeBuddyApiKey") return guide || null;
  const env =
    codeBuddyEnvironmentIntent(owner?.codeBuddyInternetEnvironment) ||
    CODEBUDDY_ENVIRONMENTS[0];
  return { ...guide, url: env.keyUrl, link: `打开 CodeBuddy ${env.label}创建 Key` };
}

const RULES = [
  ["agentCli.qoder", /Qoder CLI[^。\n]{0,20}(API Key|凭据)|QODER_PERSONAL_ACCESS_TOKEN[^\n]*rejected/i],
  ["codexApiKey", /Codex[^。\n]{0,16}API Key/i],
  ["claudeCodeApiKey", /Claude Code[^。\n]{0,16}API Key/i],
  [
    "codeBuddyApiKey",
    /CodeBuddy[^。\n]{0,20}(API Key|Key|凭据)|拒绝了你的 API Key/i,
  ],
  [
    "gitSshKey",
    /Git SSH (?:密钥|私钥)/i,
  ],
  [
    "gitToken",
    /Git Token|工蜂[^。\n]{0,12}令牌|could not read Username|Authentication failed|HTTP Basic: Access denied|terminal prompts disabled|requested URL returned error: 40[13]/i,
  ],
];

// credentialHint recognizes errors caused by a missing or rejected personal
// credential. owner is true when the message says the credential belongs to
// the responsible user (SOP runs use their credentials, not the viewer's).
export function credentialHint(text) {
  const message = String(text || "");
  if (!message) return null;
  for (const [field, rule] of RULES)
    if (rule.test(message))
      return {
        field,
        label: CREDENTIALS[field],
        owner: /责任人|Owner|该账号/i.test(message),
      };
  return null;
}

export function openCredentialSettings(field, sshRemote) {
  const credential = CREDENTIALS[field] ? field : "";
  if (window.parent !== window)
    window.parent.postMessage(
      { type: "agentflow-chat-open-view", view: "settings", credential, ...(credential === "gitSshKey" && sshRemote ? { sshRemote } : {}) },
      location.origin,
    );
  else location.href = "/#/settings";
}
