"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const source = fs.readFileSync(process.argv[2], "utf8");
const page = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
assert.match(page, /<textarea id="importTAPDURL"[^>]*rows="3"/);
for (const id of ["reqBaseBranch", "importTAPDBaseBranch", "issueBaseBranch"]) {
  assert.ok(page.includes(`id="${id}"`), id);
}

function load(ctx, names) {
  for (const name of names) {
    const start = source.search(new RegExp("^(?:async )?function " + name + "\\(", "m"));
    assert.ok(start >= 0, name);
    const rest = source.slice(start), next = /\n(?:async )?function \w+\(/.exec(rest);
    vm.runInContext(next ? rest.slice(0, next.index) : rest, ctx);
  }
}
function element(value = "") {
  const classes = new Set();
  return {
    value, dataset: {}, hidden: false, disabled: false, textContent: "", innerHTML: "",
    classList: {contains: v => classes.has(v), add: v => classes.add(v), remove: v => classes.delete(v)},
    querySelectorAll: () => [], querySelector: () => null,
  };
}
const elements = {};
for (const id of [
  "reqRepository", "reqBaseBranch", "reqBaseBranchHint", "requirementModal",
  "chatInput", "reqClarifyPanel", "reqComposerHint", "reqDestinations", "chatSend", "reqWorkflow",
  "chatCreateLocal", "chatCreateTAPD", "importTAPDModal", "importTAPDURL", "importTAPDWorkflow",
  "importTAPDRepository", "importTAPDBaseBranch", "confirmImportTAPD",
]) elements["#" + id] = element();
elements["#reqRepository"].dataset.ids = '["r1"]';
elements["#reqWorkflow"].value = "sop";
elements["#importTAPDWorkflow"].value = "sop";
const storage = new Map(), requests = [], notices = [];
const ctx = {
  state: {
    projectFilter: "p", currentUser: {id: 1},
    data: {repositories: [
      {id: "r1", projectId: "p", name: "API", defaultBranch: "release/2026.10"},
      {id: "r2", projectId: "p", name: "Web", defaultBranch: "main"},
    ], issues: [], tasks: []},
  },
  chatHistory: [], chatClarifySessionId: "", chatClarifyJobId: "", chatAbortController: null,
  $: selector => elements[selector],
  currentProject: () => ({id: "p", defaultRepositoryId: "r1"}),
  escapeHTML: s => String(s).replace(/[&<>"]/g, c => ({"&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;"}[c])),
  collectChatReply: () => elements["#chatInput"].value.trim(),
  requirementComposerOpen: () => true, requirementComposerBusy: () => false,
  selectedReqWorkflowId: () => elements["#reqWorkflow"].value, selectedReqPlaybookId: () => "",
  sessionStorage: {getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)},
  setChatCreateEnabled: on => {ctx.createEnabled = on},
  syncComposerReplyState: () => {}, syncReqComposerLayout: () => {}, clarifyThinkingReset: () => {},
  paintRequirementComposerChrome: () => {}, renderClarifyPanel: () => {},
  fillReqWorkflow: value => {elements["#reqWorkflow"].value = value || "sop"},
  renderReqContext: () => {
    elements["#reqRepository"].dataset.ids = '["r1","r2"]';
    ctx.renderReqRepositoryPicker();
  },
  reqDestButton: () => null, projectTAPDReady: () => true, usableClarifySummary: s => s,
  renderIssues: () => {}, openIssueDetail: () => {}, requirementNeedsClarification: () => false,
  toast: message => notices.push(message),
  api: async (url, init) => {
    const body = JSON.parse(init.body);
    requests.push({url, body});
    return url.endsWith("/dispatch") ? {id: "task"} : {...body, id: "created", title: body.title || "TAPD 任务"};
  },
};
vm.createContext(ctx);
load(ctx, [
  "updateIssueBaseBranchInput", "selectedReqRepositoryIds", "selectedReqRepositoryId", "selectedReqBaseBranch",
  "renderReqRepositoryPicker", "setReqRepositoryIds", "invalidateRequirementAnalysis", "collectRequirementDraft",
  "persistRequirementComposer", "applyRequirementComposerDraft", "reqCreateDirect", "composerCreateReady",
  "createIssueFromChat", "rememberIssue", "clearRequirementComposer", "selectedImportTAPDRepositoryIds", "importTAPDStory",
]);
const input = elements["#reqBaseBranch"], hint = elements["#reqBaseBranchHint"];
ctx.renderReqRepositoryPicker();
assert.equal(input.disabled, false);
assert.match(input.placeholder, /release\/2026\.10/);
input.value = "feature/continue";
ctx.renderReqRepositoryPicker();
assert.equal(input.value, "feature/continue", "rerendering the same repository must preserve an explicit branch");

ctx.chatHistory = [{role: "user", content: "原始需求"}, {role: "assistant", content: "已经分析"}];
ctx.chatClarifySessionId = "old-session";
ctx.state.chatClarifyResult = {ready: true};
ctx.state.reqUserAccepted = true;
elements["#chatInput"].value = "补充说明";
ctx.setReqRepositoryIds(["r1"]);
assert.equal(ctx.state.chatClarifyResult.ready, true, "unchanged repository selection keeps the analysis");
ctx.setReqRepositoryIds(["r2"]);
assert.equal(input.value, "", "changing repository must clear a branch from the previous repository");
assert.equal(ctx.chatClarifySessionId, "");
assert.equal(ctx.state.chatClarifyResult, null);
assert.equal(ctx.state.reqUserAccepted, false);
assert.equal(ctx.createEnabled, false);
assert.match(elements["#chatInput"].value, /原始需求/);
assert.match(elements["#chatInput"].value, /补充说明/);
assert.match(elements["#reqComposerHint"].textContent, /重新分析/);
input.value = "feature/web";
ctx.setReqRepositoryIds(["r1", "r2"]);
assert.equal(input.disabled, true);
assert.equal(input.value, "");
assert.match(hint.textContent, /请选择一个仓库/);
ctx.setReqRepositoryIds([]);
assert.equal(ctx.selectedReqRepositoryIds().length, 0, "removing every repository must not silently restore the default");
assert.equal(input.disabled, true);

ctx.setReqRepositoryIds(["r1"]);
input.value = " feature/continue ";
ctx.chatHistory = [{role: "user", content: "继续开发已有分支"}];
ctx.chatClarifySessionId = "branch-session";
ctx.state.chatClarifyResult = {ready: true, summary: "该分支分析通过"};
ctx.persistRequirementComposer();
const draft = JSON.parse(storage.get("agentflow.requirementComposer"));
assert.equal(draft.baseBranch, "feature/continue");
input.value = "";
ctx.applyRequirementComposerDraft(draft);
assert.equal(input.value, "feature/continue");
assert.equal(ctx.selectedReqRepositoryId(), "r1");
assert.equal(ctx.chatClarifySessionId, "branch-session");
assert.equal(ctx.state.chatClarifyResult.ready, true, "restoring the same saved context must keep its analysis");
ctx.invalidateRequirementAnalysis();
assert.equal(ctx.chatClarifySessionId, "");
assert.equal(ctx.state.chatClarifyResult, null, "editing the base branch must discard the prior ready result");
assert.match(elements["#chatInput"].value, /继续开发已有分支/);

(async () => {
  input.value = "feature/continue";
  ctx.state.reqCreateMode = "direct";
  ctx.state.chatClarifyResult = null;
  await ctx.createIssueFromChat("local");
  assert.equal(requests.at(-1).url, "/api/issues");
  assert.equal(requests.at(-1).body.baseBranch, "feature/continue");

  ctx.state.reqCreateMode = "analyzed";
  ctx.state.chatClarifyResult = {ready: true, suggestedTitle: "继续开发", summary: "分析通过", assessment: {}};
  await ctx.createIssueFromChat("local");
  const analyzed = requests.filter(request => request.url === "/api/issues").at(-1);
  assert.equal(analyzed.body.baseBranch, "feature/continue");
  assert.equal(requests.at(-1).url, "/api/issues/created/dispatch");

  input.value = "";
  ctx.state.reqCreateMode = "direct";
  await ctx.createIssueFromChat("local");
  assert.equal(requests.at(-1).body.baseBranch, "", "blank branch preserves server-side repository defaults");

  const share = "【关闭套餐后清理付费队列】\nhttps://tapd.example.com/tapd_fe/10000000/task/detail/1010000000100001554";
  elements["#importTAPDURL"].value = share;
  elements["#importTAPDRepository"].dataset.ids = '["r1"]';
  elements["#importTAPDBaseBranch"].value = " feature/continue ";
  await ctx.importTAPDStory({preventDefault() {}});
  assert.equal(requests.at(-1).url, "/api/issues/import-tapd");
  assert.equal(requests.at(-1).body.url, share, "TAPD share text must reach the backend with its link intact");
  assert.equal(requests.at(-1).body.baseBranch, "feature/continue");
  assert.equal(elements["#confirmImportTAPD"].disabled, false);
  assert.ok(!notices.some(message => /not defined|is not a function/.test(message)), notices.join("\n"));
  console.log("issue base branch composer behavior passed");
})().catch(error => {console.error(error); process.exitCode = 1});
