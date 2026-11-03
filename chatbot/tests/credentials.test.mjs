import { test } from "vitest";
import assert from "node:assert/strict";
import { CREDENTIAL_GUIDES, credentialHint } from "../src/credentials.js";

test("credential errors from the platform link to the right setting", () => {
  const cases = [
    ["请先在个人配置中配置自己的 CodeBuddy API Key", "codeBuddyApiKey", false],
    ["请先配置个人 CodeBuddy API Key", "codeBuddyApiKey", false],
    [
      "CodeBuddy 拒绝了你的 API Key（401/403），请在「设置」里检查 CodeBuddy API Key 和网络环境",
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

test("legacy Taihu errors do not point at a hidden credential", () => {
  for (const message of [
    "当前责任人未配置太湖个人令牌：请先在个人凭证中配置",
    "MCP gongfeng 需要太湖个人令牌：请先在个人凭证中配置",
  ])
    assert.equal(credentialHint(message)?.field === "taihuPat", false, message);
});

test("the Git token guide points at GitHub", () => {
  const hint = credentialHint("git clone: exit status 128: Authentication failed");
  assert.equal(hint.label, "GitHub Git Token");
  assert.equal(CREDENTIAL_GUIDES.gitToken.url, "https://github.com/settings/tokens");
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
