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
    querySelectorAll: () => [], querySelector: () => null, focus() {},
  };
}
const elements = {};
for (const id of [
  "reqRepository", "reqBaseBranch", "reqBaseBranchHint", "requirementModal", "reqRepoChip", "reqRepoChipLabel",
  "chatInput", "reqComposerHint", "reqWorkflow",
  "importTAPDModal", "importTAPDURL", "importTAPDWorkflow",
  "importTAPDRepository", "importTAPDBaseBranch", "importTAPDBaseBranchHint", "importTAPDRepositoryBranches", "confirmImportTAPD",
]) elements["#" + id] = element();
elements["#reqRepository"].dataset.ids = '["r1"]';
elements["#reqWorkflow"].value = "sop";
elements["#importTAPDWorkflow"].value = "sop";
const storage = new Map(), requests = [], notices = [], opened = [], chats = [];
const ctx = {
  state: {
    projectFilter: "p", currentUser: {id: 1},
    data: {repositories: [
      {id: "r1", projectId: "p", name: "API", defaultBranch: "release/2026.10"},
      {id: "r2", projectId: "p", name: "Web", defaultBranch: "main"},
    ], issues: [], tasks: []},
  },
  $: selector => elements[selector],
  currentProject: () => ({id: "p", defaultRepositoryId: "r1"}),
  escapeHTML: s => String(s).replace(/[&<>"]/g, c => ({"&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;"}[c])),
  requirementComposerOpen: () => true, requirementComposerBusy: () => false, requirementComposerDirty: () => true,
  selectedReqWorkflowId: () => elements["#reqWorkflow"].value, selectedReqPlaybookId: () => "", reqUsesSOP: () => true,
  sessionStorage: {getItem: key => storage.get(key), setItem: (key, value) => storage.set(key, value), removeItem: key => storage.delete(key)},
  renderReqFooter: () => {}, updateReqCharCount: () => {}, renderReqTarget: () => {}, renderReqDelivery: () => {}, resetRequirementComposer: () => {},
  fillReqWorkflow: value => {elements["#reqWorkflow"].value = value || "sop"},
  renderReqContext: () => {
    elements["#reqRepository"].dataset.ids = '["r1","r2"]';
    ctx.renderReqRepositoryPicker();
  },
  projectTAPDReady: () => true,
  renderIssues: () => {}, openIssueDetail: (id, tab) => opened.push([id, tab || ""]), openIssueInChat: issue => chats.push(issue.id),
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
  "renderReqRepositoryPicker", "syncReqBaseBranch", "setReqRepositoryIds",
  "reqRepoNames", "renderReqRepoChip", "reqDescription", "collectRequirementDraft",
  "persistRequirementComposer", "applyRequirementComposerDraft", "pushExistingIssueToTAPD",
  "createIssueFromChat", "rememberIssue", "clearRequirementComposer", "selectedImportTAPDRepositoryIds", "importTAPDStory",
  "selectedIssueRepositoryBranches", "renderIssueRepositoryBranchInputs", "renderImportTAPDRepositoryPicker", "setImportTAPDRepositoryIds",
]);
const input = elements["#reqBaseBranch"], hint = elements["#reqBaseBranchHint"];
ctx.renderReqRepositoryPicker();
assert.equal(input.disabled, false);
assert.match(input.placeholder, /release\/2026\.10/);
assert.equal(hint.hidden, true, "a single repository needs no inline branch hint");
assert.match(input.title, /继续该已有分支/, "the long branch explanation moves into the input title");
assert.match(elements["#reqRepoChipLabel"].innerHTML, /仓库 <b>API<\/b>/);
assert.ok(!elements["#reqRepository"].innerHTML.includes("data-remove-req-repo"), "a lone repository cannot be removed");
assert.ok(elements["#reqRepository"].innerHTML.includes("＋ 添加仓库"));
assert.ok(!elements["#reqRepository"].innerHTML.includes("已全部选择"));
input.value = "feature/continue";
ctx.renderReqRepositoryPicker();
assert.equal(input.value, "feature/continue", "rerendering the same repository must preserve an explicit branch");
assert.match(elements["#reqRepoChipLabel"].innerHTML, /API<\/b> · feature\/continue/);

ctx.setReqRepositoryIds(["r2"]);
assert.equal(input.value, "", "changing repository must clear a branch from the previous repository");
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
elements["#chatInput"].value = "继续开发已有分支";
ctx.state.reqTarget = "tapd";
ctx.persistRequirementComposer();
const draft = JSON.parse(storage.get("agentflow.requirementComposer"));
assert.equal(draft.baseBranch, "feature/continue");
assert.equal(draft.input, "继续开发已有分支");
assert.equal(draft.target, "tapd");
for (const gone of ["step", "history", "answers", "clarifyResult", "jobId"]) assert.ok(!(gone in draft), `the draft must not keep ${gone}`);
input.value = "";
elements["#chatInput"].value = "";
ctx.state.reqTarget = "local";
ctx.applyRequirementComposerDraft(draft);
assert.equal(input.value, "feature/continue");
assert.equal(ctx.selectedReqRepositoryId(), "r1");
assert.equal(ctx.state.reqTarget, "tapd");
assert.equal(elements["#chatInput"].value, "继续开发已有分支");

(async () => {
  input.value = "feature/continue";
  elements["#chatInput"].value = "个人主页增加徽章\n兼容现有成就体系";
  ctx.state.reqTarget = "local";
  await ctx.createIssueFromChat();
  const created = requests.at(-1);
  assert.equal(created.url, "/api/issues");
  assert.equal(created.body.title, "个人主页增加徽章", "the title comes from the first line of the description");
  assert.equal(created.body.description, "个人主页增加徽章\n兼容现有成就体系");
  assert.equal(created.body.baseBranch, "feature/continue");
  assert.equal(created.body.clarification, undefined, "creating no longer carries a clarification");
  assert.deepEqual(opened.at(-1), ["created", ""], "plain creation opens the issue detail without dispatching");
  assert.equal(chats.length, 0);

  const before = requests.length;
  elements["#chatInput"].value = "要和 AI 聊清楚的需求";
  await ctx.createIssueFromChat(undefined, "chat");
  assert.equal(requests.length, before + 1, "creating for the chat only creates the issue, it does not dispatch");
  assert.equal(requests.at(-1).url, "/api/issues");
  assert.deepEqual(chats, ["created"], "creating for the chat opens the issue in the chat");
  assert.equal(opened.length, 1, "creating for the chat does not open the issue detail");
  assert.equal(ctx.state.reqCreating, "");

  elements["#chatInput"].value = "   ";
  const blank = requests.length;
  await ctx.createIssueFromChat(undefined, "chat");
  assert.equal(requests.length, blank, "a blank description creates nothing");
  assert.equal(notices.at(-1), "先写一句需求");

  input.value = "";
  elements["#chatInput"].value = "空分支";
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
