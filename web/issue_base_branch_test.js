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
  "reqRepository", "reqBaseBranch", "reqBaseBranchHint", "requirementModal", "reqRepoChip", "reqRepoChipLabel", "reqTitle",
  "chatInput", "reqClarifyPanel", "reqComposerHint", "reqWorkflow",
  "importTAPDModal", "importTAPDURL", "importTAPDWorkflow",
  "importTAPDRepository", "importTAPDBaseBranch", "importTAPDBaseBranchHint", "importTAPDRepositoryBranches", "confirmImportTAPD",
]) elements["#" + id] = element();
elements["#reqRepository"].dataset.ids = '["r1"]';
elements["#reqWorkflow"].value = "sop";
elements["#importTAPDWorkflow"].value = "sop";
const storage = new Map(), requests = [], notices = [], opened = [];
let changedAlerts = 0;
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
  collectChatReply: () => "",
  requirementComposerOpen: () => true, requirementComposerBusy: () => false, requirementComposerDirty: () => true,
  selectedReqWorkflowId: () => elements["#reqWorkflow"].value, selectedReqPlaybookId: () => "",
  sessionStorage: {getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)},
  clarifyThinkingReset: () => {}, renderReqFooter: () => {}, renderReqAnswerHistory: () => {}, updateReqCharCount: () => {},
  goReqStep: step => {ctx.state.reqStep = Number(step) || 1},
  renderReqRepoChangedAlert: () => {changedAlerts++},
  paintRequirementComposerChrome: () => {}, renderClarifyPanel: () => {},
  fillReqWorkflow: value => {elements["#reqWorkflow"].value = value || "sop"},
  renderReqContext: () => {
    elements["#reqRepository"].dataset.ids = '["r1","r2"]';
    ctx.renderReqRepositoryPicker();
  },
  projectTAPDReady: () => true, usableClarifySummary: s => s,
  renderIssues: () => {}, openIssueDetail: (id, tab) => opened.push([id, tab || ""]), openRunFromId: () => {},
  toast: message => notices.push(message),
  api: async (url, init) => {
    const body = JSON.parse(init.body);
    requests.push({url, body});
    return url.endsWith("/dispatch") ? {id: "task"} : {...body, id: "created", title: body.title || "TAPD 任务"};
  },
};
vm.createContext(ctx);
load(ctx, [
  "updateIssueBaseBranchInput", "reqProjectRepositories", "selectedReqRepositoryIds", "selectedReqRepositoryId", "selectedReqBaseBranch",
  "renderReqRepositoryPicker", "syncReqBaseBranch", "setReqRepositoryIds", "reqCurrentRepoSnapshot", "reqRepoSnapshotChanged",
  "reqRepoNames", "renderReqRepoChip", "reqDescription", "collectRequirementDraft",
  "persistRequirementComposer", "applyRequirementComposerDraft", "reqCreateDirect", "reqCreateAccepted",
  "createIssueFromChat", "rememberIssue", "clearRequirementComposer", "selectedImportTAPDRepositoryIds", "importTAPDStory",
  "selectedIssueRepositoryBranches", "renderIssueRepositoryBranchInputs", "renderImportTAPDRepositoryPicker", "setImportTAPDRepositoryIds",
]);
const input = elements["#reqBaseBranch"], hint = elements["#reqBaseBranchHint"];
ctx.renderReqRepositoryPicker();
assert.equal(input.disabled, false);
assert.match(input.placeholder, /release\/2026\.10/);
assert.equal(hint.hidden, true, "a single repository needs no inline branch hint");
assert.match(input.title, /继续该已有分支/, "the long branch explanation moves into the input title");
assert.match(elements["#reqRepoChipLabel"].innerHTML, /分析仓库 <b>API<\/b>/);
assert.ok(!elements["#reqRepository"].innerHTML.includes("data-remove-req-repo"), "a lone repository cannot be removed");
assert.ok(elements["#reqRepository"].innerHTML.includes("＋ 添加仓库"));
assert.ok(!elements["#reqRepository"].innerHTML.includes("已全部选择"));
input.value = "feature/continue";
ctx.renderReqRepositoryPicker();
assert.equal(input.value, "feature/continue", "rerendering the same repository must preserve an explicit branch");
assert.match(elements["#reqRepoChipLabel"].innerHTML, /API<\/b> · feature\/continue/);

ctx.chatHistory = [{role: "user", content: "原始需求"}, {role: "assistant", content: "已经分析"}];
ctx.chatClarifySessionId = "old-session";
ctx.state.chatClarifyResult = {ready: true};
ctx.state.reqClarifyRepoSnapshot = ctx.reqCurrentRepoSnapshot();
ctx.state.reqStep = 3;
elements["#chatInput"].value = "原始需求";
ctx.setReqRepositoryIds(["r1"]);
assert.equal(ctx.reqRepoSnapshotChanged(), false, "unchanged repository selection keeps the analysis");
ctx.setReqRepositoryIds(["r2"]);
assert.equal(input.value, "", "changing repository must clear a branch from the previous repository");
assert.equal(ctx.chatClarifySessionId, "old-session", "changing repository keeps the clarification session");
assert.equal(ctx.state.chatClarifyResult.ready, true, "changing repository keeps the conclusion");
assert.equal(ctx.reqRepoSnapshotChanged(), true, "the confirm page must flag that the conclusion used other repositories");
assert.ok(changedAlerts > 0, "the confirm page re-renders its repository alert");
assert.equal(elements["#chatInput"].value, "原始需求");
input.value = "feature/web";
ctx.setReqRepositoryIds(["r1", "r2"]);
assert.equal(input.disabled, true);
assert.equal(input.value, "");
assert.equal(hint.hidden, false);
assert.match(hint.textContent, /只选一个仓库时可指定分支/);
assert.match(input.title, /请选择一个仓库/);
assert.match(elements["#reqRepoChipLabel"].innerHTML, /API 等 2 个/);
assert.ok(elements["#reqRepository"].innerHTML.includes("data-remove-req-repo"));
assert.ok(!elements["#reqRepository"].innerHTML.includes("＋ 添加仓库"), "no add control once every repository is picked");
ctx.setReqRepositoryIds([]);
assert.equal(ctx.selectedReqRepositoryIds().length, 0, "removing every repository must not silently restore the default");
assert.equal(input.disabled, true);
assert.match(elements["#reqRepoChipLabel"].innerHTML, /未选择/);

ctx.setReqRepositoryIds(["r1"]);
input.value = " feature/continue ";
ctx.chatHistory = [{role: "user", content: "继续开发已有分支"}];
ctx.chatClarifySessionId = "branch-session";
ctx.state.chatClarifyResult = {ready: true, summary: "该分支分析通过"};
Object.assign(ctx.state, {reqStep: 2, reqReached: 3, reqQuestionIndex: 1, reqAnswers: {0: {selected: [1], other: ""}}, reqTarget: "tapd", reqClarifyRepoSnapshot: {ids: ["r1"], baseBranch: "feature/continue"}});
elements["#reqTitle"].value = "继续开发";
ctx.persistRequirementComposer();
const draft = JSON.parse(storage.get("agentflow.requirementComposer"));
assert.equal(draft.baseBranch, "feature/continue");
assert.equal(draft.step, 2);
assert.equal(draft.questionIndex, 1);
assert.deepEqual(draft.answers, {0: {selected: [1], other: ""}});
assert.equal(draft.target, "tapd");
assert.equal(draft.title, "继续开发");
input.value = "";
elements["#reqTitle"].value = "";
Object.assign(ctx.state, {reqStep: 1, reqQuestionIndex: 0, reqAnswers: {}, reqTarget: "local"});
ctx.applyRequirementComposerDraft(draft);
assert.equal(input.value, "feature/continue");
assert.equal(ctx.selectedReqRepositoryId(), "r1");
assert.equal(ctx.chatClarifySessionId, "branch-session");
assert.equal(ctx.state.chatClarifyResult.ready, true, "restoring the same saved context must keep its analysis");
assert.equal(ctx.state.reqStep, 2);
assert.equal(ctx.state.reqQuestionIndex, 1);
assert.equal(ctx.state.reqTarget, "tapd");
assert.equal(elements["#reqTitle"].value, "继续开发");
assert.equal(ctx.reqRepoSnapshotChanged(), false);
input.value = "feature/other";
assert.equal(ctx.reqRepoSnapshotChanged(), true, "editing the base branch is flagged instead of discarding the result");
assert.equal(ctx.state.chatClarifyResult.ready, true);

(async () => {
  input.value = "feature/continue";
  elements["#reqTitle"].value = "";
  elements["#chatInput"].value = "跳过澄清的需求";
  ctx.state.reqClarifySkipped = true;
  ctx.state.reqTarget = "local";
  await ctx.createIssueFromChat();
  assert.equal(requests.at(-1).url, "/api/issues");
  assert.equal(requests.at(-1).body.baseBranch, "feature/continue");
  assert.equal(requests.at(-1).body.description, "跳过澄清的需求");
  assert.deepEqual(opened.at(-1), ["created", ""], "a skipped clarification opens the issue detail without dispatching");

  ctx.state.reqClarifySkipped = false;
  ctx.state.chatClarifyResult = {ready: true, suggestedTitle: "继续开发", summary: "分析通过"};
  elements["#reqTitle"].value = "确认页标题";
  await ctx.createIssueFromChat("local");
  const analyzed = requests.filter(request => request.url === "/api/issues").at(-1);
  assert.equal(analyzed.body.baseBranch, "feature/continue");
  assert.equal(analyzed.body.title, "确认页标题");
  assert.equal(analyzed.body.clarification.status, "ready");
  assert.equal(analyzed.body.clarification.passed, undefined, "no score/pass flag is sent");
  assert.equal(requests.at(-1).url, "/api/issues/created/dispatch");

  ctx.state.chatClarifyResult = {ready: false, summary: "还差验收标准", questions: [{question: "验收？", options: []}]};
  const before = requests.length;
  await ctx.createIssueFromChat("local");
  assert.equal(requests.length, before + 1, "a not-ready conclusion creates the issue but does not dispatch");
  assert.equal(requests.at(-1).body.clarification.status, "in_progress");
  assert.deepEqual(opened.at(-1), ["created", "clarify"]);

  input.value = "";
  ctx.state.reqClarifySkipped = true;
  await ctx.createIssueFromChat("local");
  assert.equal(requests.at(-1).body.baseBranch, "", "blank branch preserves server-side repository defaults");

  const share = "【关闭套餐后清理付费队列】\nhttps://tapd.example.com/tapd_fe/10000000/task/detail/1010000000100001554";
  elements["#importTAPDURL"].value = share;
  elements["#importTAPDRepository"].dataset.ids = '["r1"]';
  ctx.renderImportTAPDRepositoryPicker();
  elements["#importTAPDBaseBranch"].value = " feature/continue ";
  await ctx.importTAPDStory({preventDefault() {}});
  assert.equal(requests.at(-1).url, "/api/issues/import-tapd");
  assert.equal(requests.at(-1).body.url, share, "TAPD share text must reach the backend with its link intact");
  assert.equal(requests.at(-1).body.baseBranch, "feature/continue");
  assert.equal(requests.at(-1).body.repositoryBranches.r1, "feature/continue");
  ctx.setImportTAPDRepositoryIds(["r1", "r2"]);
  assert.equal(elements["#importTAPDBaseBranch"].hidden, true);
  const branchHost = elements["#importTAPDRepositoryBranches"];
  assert.equal(branchHost.hidden, false);
  assert.match(branchHost.innerHTML, /data-repository-branch="r1"[^>]*value="feature\/continue"/);
  assert.match(branchHost.innerHTML, /data-repository-branch="r2"[^>]*value=""/);
  const branchInputs = [
    {dataset: {repositoryBranch: "r1"}, value: " feature/api "},
    {dataset: {repositoryBranch: "r2"}, value: " feature/web "},
  ];
  branchHost.querySelectorAll = () => branchInputs;
  await ctx.importTAPDStory({preventDefault() {}});
  assert.deepEqual(requests.at(-1).body.repositoryBranches, {r1: "feature/api", r2: "feature/web"});
  assert.equal(requests.at(-1).body.baseBranch, "");
  ctx.setImportTAPDRepositoryIds(["r2"]);
  assert.equal(elements["#importTAPDBaseBranch"].value, "feature/web", "removing another repository preserves this repository's branch");
  assert.equal(elements["#importTAPDBaseBranch"].hidden, false);
  assert.equal(branchHost.hidden, true);
  branchHost.querySelectorAll = () => [];
  ctx.setImportTAPDRepositoryIds(["r1", "r2"]);
  assert.match(branchHost.innerHTML, /data-repository-branch="r1"[^>]*value=""/, "readding a repository must not reuse its removed branch");
  assert.match(branchHost.innerHTML, /data-repository-branch="r2"[^>]*value="feature\/web"/);
  assert.equal(elements["#confirmImportTAPD"].disabled, false);
  assert.ok(!notices.some(message => /not defined|is not a function/.test(message)), notices.join("\n"));
  console.log("issue base branch composer behavior passed");
})().catch(error => {console.error(error); process.exitCode = 1});
