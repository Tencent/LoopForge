// Personal credentials configured under 个人配置 → 个人凭证. Field names match
// the inputs there so the parent page can scroll to and focus the right card.
// Keep the rules in sync with credentialHint in web/app/access.js.
export const CREDENTIALS = {
  codeBuddyApiKey: "CodeBuddy API Key",
  codexApiKey: "Codex API Key",
  claudeCodeApiKey: "Claude Code API Key",
  gitToken: "GitHub Git Token",
};

// Where and how to get each credential. Keep in sync with
// assistantCredentialRules in internal/httpapi/assistant_run_view.go.
export const CREDENTIAL_GUIDES = {
  gitToken: {
    url: "https://github.com/settings/tokens",
    link: "打开 GitHub 申请令牌",
    steps: [
      "打开 GitHub「Settings → Developer settings → Personal access tokens」新建令牌",
      "为目标仓库授予 Contents 和 Pull requests 的读写权限，创建后立即复制 token",
      "到「个人配置 → GitHub Git Token」粘贴，点「校验并保存」",
      "回到这里点「重试节点」",
    ],
  },
  codeBuddyApiKey: {
    url: "https://www.codebuddy.ai/",
    link: "打开 CodeBuddy 创建 Key",
    steps: [
      "打开 CodeBuddy 个人中心创建 API Key 并复制",
      "到「个人配置 → CodeBuddy API Key」粘贴并保存",
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
};

const RULES = [
  ["codexApiKey", /Codex[^。\n]{0,16}API Key/i],
  ["claudeCodeApiKey", /Claude Code[^。\n]{0,16}API Key/i],
  [
    "codeBuddyApiKey",
    /CodeBuddy[^。\n]{0,20}(API Key|Key|凭据)|拒绝了你的 API Key/i,
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

export function openCredentialSettings(field) {
  const credential = CREDENTIALS[field] ? field : "";
  if (window.parent !== window)
    window.parent.postMessage(
      { type: "agentflow-chat-open-view", view: "settings", credential },
      location.origin,
    );
  else location.href = "/#/settings";
}
