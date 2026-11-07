import { test } from "vitest";
import assert from "node:assert/strict";
import {
  CREDENTIAL_GUIDES,
  codeBuddyEnvironment,
  codeBuddyKeyURL,
  codeBuddyKeyWarning,
  credentialGuide,
  credentialHint,
} from "../src/credentials.js";

test("CodeBuddy key links follow the selected network environment", () => {
  assert.equal(codeBuddyKeyURL(""), "https://www.codebuddy.ai/profile/keys");
  assert.equal(codeBuddyKeyURL("internal"), "https://copilot.tencent.com/profile/");
  assert.equal(
    codeBuddyKeyURL(" iOA "),
    "https://tencent.sso.copilot.tencent.com/profile/keys",
  );
  assert.equal(codeBuddyKeyURL("typo"), "https://www.codebuddy.ai/profile/keys");
  assert.equal(codeBuddyEnvironment("china")?.value, "internal");
  assert.equal(codeBuddyEnvironment("public")?.value, "");
  assert.equal(codeBuddyEnvironment("private-net"), null);

  const intl = credentialGuide("codeBuddyApiKey", {});
  assert.equal(intl.url, "https://www.codebuddy.ai/profile/keys");
  const china = credentialGuide("codeBuddyApiKey", {
    codeBuddyInternetEnvironment: "internal",
  });
  assert.equal(china.url, "https://copilot.tencent.com/profile/");
  assert.match(china.link, /国内站/);
  assert.deepEqual(china.steps, CREDENTIAL_GUIDES.codeBuddyApiKey.steps);
  assert.equal(credentialGuide("gitToken", {}), CREDENTIAL_GUIDES.gitToken);
  assert.equal(credentialGuide("unknown", {}), null);
});

test("third-party model keys are flagged before CodeBuddy rejects them", () => {
  assert.equal(codeBuddyKeyWarning(""), "");
  assert.equal(codeBuddyKeyWarning("ck_abc"), "");
  assert.match(codeBuddyKeyWarning("sk-abc"), /第三方/);
  assert.match(codeBuddyKeyWarning("abc"), /ck_/);
});

test("credential errors from the platform link to the right setting", () => {
  const cases = [
    ["请先在个人配置中配置自己的 CodeBuddy API Key", "codeBuddyApiKey", false],
    ["请先配置个人 CodeBuddy API Key", "codeBuddyApiKey", false],
    [
      "CodeBuddy 拒绝了你的 API Key（401/403）：请在「个人配置 → CodeBuddy API Key」确认网络环境与创建 Key 的站点一致，且填的是 CodeBuddy 的 Key，而不是 DeepSeek、OpenAI 等第三方模型的 Key",
      "codeBuddyApiKey",
      false,
    ],
    [
      "当前责任人未配置 CodeBuddy API Key：请先在个人配置中配置",
      "codeBuddyApiKey",
      true,
    ],
    [
      "当前责任人未配置 Codex API Key：请先在个人配置中配置，或将执行机器认证方式切回本机登录",
      "codexApiKey",
      true,
    ],
    [
      "需求责任人未配置 Claude Code API Key，请在个人配置中配置",
      "claudeCodeApiKey",
      true,
    ],
    [
      "git clone https://github.com/a/b.git: exit status 128 (fatal: could not read Username for 'https://github.com': terminal prompts disabled)",
      "gitToken",
      false,
    ],
    [
      "项目责任人未配置工蜂 Git Token（未指定责任人时用项目 Owner），请让该账号到「个人配置 → 个人凭证」填写",
      "gitToken",
      true,
    ],
    [
      "git clone https://git.example.com/a/b.git: exit status 128 (fatal: could not read Username for 'https://git.example.com': terminal prompts disabled)",
      "gitToken",
      false,
    ],
    [
      "git clone: exit status 128: remote: HTTP Basic: Access denied\nfatal: Authentication failed for 'https://git.example.com/a/b.git/'",
      "gitToken",
      false,
    ],
    [
      "当前配置的是工蜂「私人令牌」，只能用于 API，不能克隆或推送仓库",
      "gitToken",
      false,
    ],
  ];
  for (const [message, field, owner] of cases) {
    const hint = credentialHint(message);
    assert.equal(hint?.field, field, message);
    assert.equal(hint.owner, owner, message);
  }
});

test("unknown credential errors do not point at a hidden credential", () => {
  assert.equal(credentialHint("当前责任人未配置个人令牌：请先在个人凭证中配置"), null);
});

test("MCP calls missing a GitHub token point at the Git token", () => {
  assert.equal(credentialHint("MCP GitHub 需要 GitHub Git Token：请先在个人凭证中配置")?.field, "gitToken");
});

test("the Git token guide opens the GitHub connection settings", () => {
  const hint = credentialHint("git clone: exit status 128: Authentication failed");
  assert.equal(hint.label, "GitHub Git Token");
  assert.equal(CREDENTIAL_GUIDES.gitToken.url, "/#/settings");
});

test("unrelated errors do not suggest personal credentials", () => {
  for (const message of [
    "",
    null,
    "连接暂时中断，正在重连…",
    "群协作暂不可用：服务端未配置 AGENTFLOW_PLATFORM_MCP_URL",
    "云端执行池未配置，无法为该运行分配执行机器",
    "git clone: fatal: unable to access 'https://git.example.com/': Could not resolve host",
    "TAPD 同步未配置",
    "executor reported an error: 401 Unauthorized",
  ])
    assert.equal(credentialHint(message), null, String(message));
});
