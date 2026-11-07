import { test } from "vitest";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { mentionAt, insertMention } from "../src/mentions.js";
import {
  operationValidation,
  reviewedOperation,
  operationSummary,
  operationError,
  repositoryIds,
} from "../src/operations.js";
import { actionName } from "../src/platform.js";

test("mentions replace only the active token and retain the surrounding user text", () => {
  const text = "请参考@排版继续分析";
  const range = mentionAt(text, 6);
  assert.deepEqual(range, { start: 3, end: 6, query: "排版" });
  assert.deepEqual(insertMention(text, range, { name: "排版 SOP" }), {
    text: "请参考@排版 SOP 继续分析",
    caret: 11,
  });
  assert.equal(mentionAt("mail@example.com", 16), null);
  assert.equal(mentionAt("@排版 SOP 后续", 10, [{ name: "排版 SOP" }]), null);
  assert.deepEqual(
    mentionAt("@排版 SOP 后续 @角", 13, [{ name: "排版 SOP" }]),
    { start: 11, end: 13, query: "角" },
  );
});
const draft = {
  tool: "issues_import_tapd",
  body: {
    projectId: "p",
    url: "https://tapd.example/story/1",
    repositoryIds: [],
  },
};
test("TAPD import requires the target project, link and an explicit repository", () => {
  assert.match(operationValidation(draft), /代码仓库/);
  assert.match(
    operationValidation({ ...draft, body: { ...draft.body, url: 123 } }),
    /链接/,
  );
  assert.match(
    operationValidation({ ...draft, body: { url: "url" } }),
    /目标项目/,
  );
  assert.equal(
    operationValidation({
      ...draft,
      body: { ...draft.body, repositoryId: "r" },
    }),
    "",
  );
  assert.deepEqual(
    repositoryIds({ repositoryId: " r ", repositoryIds: ["r", "", null] }),
    ["r"],
  );
});
test("confirmation applies only the reviewed repository change, leaving the original draft untouched", () => {
  const reviewed = reviewedOperation(draft, {
    expected: {
      body: { url: draft.body.url, repositoryIds: [], projectId: "p" },
      tool: draft.tool,
    },
    bodyPatch: { repositoryIds: ["r"], projectId: "another" },
  });
  assert.equal(reviewed.body.projectId, "p");
  assert.deepEqual(reviewed.body.repositoryIds, ["r"]);
  assert.deepEqual(draft.body.repositoryIds, []);
  assert.throws(() => reviewedOperation(draft), /代码仓库/);
  assert.throws(
    () =>
      reviewedOperation(
        { ...draft, body: { ...draft.body, projectId: "changed" } },
        { expected: draft, bodyPatch: { repositoryIds: ["r"] } },
      ),
    /已更新/,
  );
});
test("operation summaries identify the action, real project and target instead of a generic platform operation", () => {
  const summary = operationSummary(
    { ...draft, body: { ...draft.body, repositoryIds: ["r"] } },
    {
      projects: [{ id: "p", name: "Payments" }],
      references: [{ type: "repository", id: "r", name: "后台仓库" }],
    },
  );
  assert.equal(summary.title, "导入 TAPD 需求");
  assert.deepEqual(summary.fields.slice(0, 3), [
    ["项目", "Payments"],
    ["TAPD 链接", draft.body.url],
    ["代码仓库", "后台仓库"],
  ]);
  assert.equal(actionName("mcp_servers_save"), "保存MCP 服务");
  assert.equal(actionName("unknown_tool"), "执行 unknown_tool");
  assert.equal(
    operationError('{"error":"请至少选择一个代码仓库"}'),
    "请至少选择一个代码仓库",
  );
  assert.equal(operationError("连接失败"), "连接失败");
});
test("a new project drafted inside another project's chat is not shown as belonging to it", () => {
  const projects = [{ id: "skillhub", name: "SkillHub" }];
  // Drafts stored before the fix still carry the session project in the body.
  for (const body of [
    { id: "okr", name: "OKR系统" },
    { id: "okr", name: "OKR系统", projectId: "skillhub" },
  ]) {
    const summary = operationSummary(
      { tool: "projects_save", body },
      { projects, projectId: "skillhub" },
    );
    assert.equal(
      summary.fields.find(([label]) => label === "项目"),
      undefined,
    );
    assert.deepEqual(summary.fields.slice(0, 2), [
      ["名称", "OKR系统"],
      ["目标", "okr"],
    ]);
    assert.match(summary.effect, /新建一个独立项目/);
  }
  assert.match(
    operationSummary(
      { tool: "projects_save", body: { id: "skillhub", name: "SkillHub" } },
      { projects, projectId: "skillhub" },
    ).effect,
    /修改这个项目/,
  );
  // A new project is bound to the generic backend SOP unless one is chosen;
  // the card says so, so the reply cannot claim "not bound".
  const sopOf = (body, references = []) =>
    operationSummary({ tool: "projects_save", body }, { projects, references, projectId: "skillhub" })
      .fields.find(([label]) => label === "SOP")?.[1];
  assert.equal(sopOf({ id: "okr", name: "OKR系统" }), "通用后端交付 SOP（默认）");
  assert.equal(
    sopOf({ id: "okr", name: "OKR系统" }, [{ type: "sop", id: "generic-backend-delivery", name: "通用后端交付 SOP（LoopForge）" }]),
    "通用后端交付 SOP（LoopForge）（默认）",
  );
  assert.equal(
    sopOf({ id: "okr", name: "OKR系统", workflowId: "generic-frontend-delivery" }, [{ type: "sop", id: "generic-frontend-delivery", name: "通用前端交付 SOP" }]),
    "通用前端交付 SOP",
  );
  assert.equal(sopOf({ id: "skillhub", name: "SkillHub" }), undefined);
  // Project-scoped resources still show the session project.
  assert.deepEqual(
    operationSummary(
      { tool: "agents_save", body: { id: "a", name: "A" } },
      { projects, projectId: "skillhub" },
    ).fields[0],
    ["项目", "SkillHub"],
  );
});
test("every write tool's card shows the chat's project only for project resources", () => {
  // The scope table lives with the backend guard test, so a tool classified
  // there (and a new global tool) cannot drift from what the card shows.
  const goTest = readFileSync(
    resolve(process.cwd(), "../internal/httpapi/assistant_draft_scope_test.go"),
    "utf8",
  );
  const scopes = [...goTest.matchAll(/^\s*"([a-z_]+)":\s*"(global|project|none)",$/gm)];
  assert.ok(scopes.length > 10, "scope table not found");
  const projects = [{ id: "skillhub", name: "SkillHub" }];
  for (const [, tool, scope] of scopes) {
    if (scope === "none") continue;
    for (const body of [
      { id: "x", name: "X" },
      { id: "x", name: "X", projectId: "skillhub" },
    ]) {
      const row = operationSummary(
        { tool, body },
        { projects, projectId: "skillhub" },
      ).fields.find(([label]) => label === "项目");
      if (scope === "global") assert.equal(row, undefined, tool);
      else assert.deepEqual(row, ["项目", "SkillHub"], tool);
    }
  }
});
