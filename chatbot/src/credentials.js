// Personal credentials configured under 个人配置 → 个人凭证. Field names match
// the inputs there so the parent page can scroll to and focus the right card.
// Keep the rules in sync with credentialHint in web/app/access.js.
export const CREDENTIALS = {
  codeBuddyApiKey: "CodeBuddy API Key",
  codexApiKey: "Codex API Key",
  claudeCodeApiKey: "Claude Code API Key",
  gitToken: "GitHub Git Token",
  gitSshKey: "Khóa Git SSH",
  gitHttpCredentials: "Token truy cập Git",
};

// A CodeBuddy key only works on the edition that issued it, and the CLI
// treats an unrecognized environment as 国际站. Keep in sync with
// CODEBUDDY_ENVIRONMENTS in web/app/workbench.js and
// domain.NormalizeCodeBuddyInternetEnvironment.
export const CODEBUDDY_ENVIRONMENTS = [
  {
    value: "",
    label: "Trang quốc tế",
    site: "codebuddy.ai",
    keyUrl: "https://www.codebuddy.ai/profile/keys",
  },
  {
    value: "internal",
    label: "Trang nội địa",
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
    ? "Đây giống key của model bên thứ ba như DeepSeek hoặc OpenAI, CodeBuddy sẽ từ chối; hãy điền key bên thứ ba vào thông tin xác thực Claude Code hoặc Codex trong cài đặt cá nhân và đặt Base URL"
    : "CodeBuddy API Key thường bắt đầu bằng ck_, hãy chắc bạn đã sao chép đúng key của CodeBuddy";
}

// Where and how to get each credential. Keep in sync with
// assistantCredentialRules in internal/httpapi/assistant_run_view.go.
export const CREDENTIAL_GUIDES = {
  gitSshKey: {
    steps: [
      "Thêm khóa công chuyên dụng vào tài khoản hosting và cấp quyền ghi repository khi cần",
      "Vào “Cài đặt cá nhân → Khóa Git SSH” để thêm khóa riêng không mật khẩu cho host này; các repository trên cùng host sẽ dùng chung. Quản trị viên nền tảng cần đăng ký khóa công của host trong “Quản trị nền tảng → Tin cậy host Git SSH”",
      "Quay lại đây và thử lại",
    ],
  },
  gitHttpCredentials: {
    steps: [
      "Tạo token truy cập cá nhân phạm vi repository, quyền tối thiểu trên nền tảng hosting",
      "Vào “Cài đặt cá nhân → Token truy cập Git” để thêm token cho host HTTPS này, rồi bật “Cung cấp cho workspace”",
      "Quay lại đây và thử lại",
    ],
  },
  gitToken: {
    url: "/#/settings",
    link: "Kết nối GitHub",
    steps: [
      "Vào “Cài đặt cá nhân”, bấm “Kết nối GitHub”, sao chép mã thiết bị rồi sang GitHub để ủy quyền",
      "Khi thêm repository, bấm “Chọn repository GitHub của tôi”, rồi chọn repository và nhánh chính",
      "Nếu quản trị viên chưa cấu hình ủy quyền GitHub, bạn cũng có thể tự nhập PAT có quyền đọc/ghi Contents và Pull requests",
      "Quay lại đây và bấm “Chạy lại bước”",
    ],
  },
  codeBuddyApiKey: {
    url: CODEBUDDY_ENVIRONMENTS[0].keyUrl,
    link: "Mở CodeBuddy để tạo Key",
    steps: [
      "Tạo API Key trên trang của tài khoản CodeBuddy (trang quốc tế, trang nội địa hoặc iOA) rồi sao chép",
      "Vào “Cài đặt cá nhân → CodeBuddy API Key”, chọn cùng môi trường mạng, dán Key rồi lưu; key của model bên thứ ba như DeepSeek không dùng được ở đây",
      "Quay lại đây và bấm “Chạy lại bước”",
    ],
  },
  codexApiKey: {
    steps: [
      "Vào “Cài đặt cá nhân → Codex API Key”, điền Key (và Base URL) rồi lưu, hoặc chuyển cách xác thực của máy thực thi về đăng nhập máy cục bộ",
      "Quay lại đây và bấm “Chạy lại bước”",
    ],
  },
  claudeCodeApiKey: {
    steps: [
      "Vào “Cài đặt cá nhân → Claude Code API Key”, điền Key (và Base URL) rồi lưu, hoặc chuyển cách xác thực của máy thực thi về đăng nhập máy cục bộ",
      "Quay lại đây và bấm “Chạy lại bước”",
    ],
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
  return { ...guide, url: env.keyUrl, link: `Mở CodeBuddy ${env.label} để tạo Key` };
}

const RULES = [
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
