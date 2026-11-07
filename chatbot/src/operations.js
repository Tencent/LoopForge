import { actionName } from "./platform.js";

const canonical = (value) =>
  Array.isArray(value)
    ? value.map(canonical)
    : value && typeof value === "object"
      ? Object.fromEntries(
          Object.keys(value)
            .sort()
            .filter((key) => value[key] !== undefined)
            .map((key) => [key, canonical(value[key])]),
        )
      : value;
export function reviewedOperation(draft, review = {}) {
  const actual = {
    tool: draft.tool,
    path: draft.path,
    query: draft.query,
    body: draft.body,
  };
  if (
    review.expected &&
    JSON.stringify(canonical(actual)) !==
      JSON.stringify(canonical(review.expected))
  )
    throw Object.assign(Error("操作内容已更新，请重新核对后再确认。"), {
      draftChanged: true,
    });
  const result =
    review.bodyPatch && draft.tool === "issues_import_tapd"
      ? {
          ...draft,
          body: {
            ...draft.body,
            repositoryIds: review.bodyPatch.repositoryIds,
            repositoryId: "",
          },
        }
      : draft;
  const invalid = operationValidation(result);
  if (invalid) throw Error(invalid);
  return result;
}

export function operationValidation(draft) {
  if (!draft?.tool) return "缺少操作名称，无法执行。";
  if (draft.tool === "issues_import_tapd") {
    if (
      typeof draft.body?.projectId !== "string" ||
      !draft.body.projectId.trim()
    )
      return "请先指定导入的目标项目。";
    if (typeof draft.body?.url !== "string" || !draft.body.url.trim())
      return "缺少 TAPD 需求链接。";
    if (!repositoryIds(draft.body).length)
      return "请至少选择一个代码仓库后再确认导入。";
  }
  return "";
}
export function repositoryIds(body) {
  return [
    ...new Set(
      [
        ...(Array.isArray(body?.repositoryIds) ? body.repositoryIds : []),
        body?.repositoryId,
      ]
        .filter((id) => typeof id === "string" && id.trim())
        .map((id) => id.trim()),
    ),
  ];
}
// Platform-level resources belong to no project; the session's project must not
// be shown as theirs (a new project would read "项目：<current project>").
const GLOBAL_SAVE_TOOLS = new Set([
  "projects_save",
  "business_domains_save",
  "sync_config_save",
]);
// repo.GenericBackendDeliveryWorkflowID, bound to new projects by default.
const DEFAULT_PROJECT_WORKFLOW = "generic-backend-delivery";
export function operationSummary(
  draft,
  { projects = [], references = [], projectId = "" } = {},
) {
  const body = draft.body || {};
  const project = GLOBAL_SAVE_TOOLS.has(draft.tool)
    ? ""
    : body.projectId || projectId;
  const named = (type, id) =>
    references.find((r) => r.type === type && r.id === id)?.name || id;
  const target = draft.path?.id || body.id;
  const existingProject =
    draft.tool === "projects_save" && projects.some((p) => p.id === target);
  // Mirrors saveProject: a new project without a workflowId is bound to the
  // generic backend SOP, so the card says so instead of showing nothing.
  const sop = body.workflowId
    ? named("sop", body.workflowId)
    : draft.tool === "projects_save" && !existingProject
      ? `${references.find((r) => r.type === "sop" && r.id === DEFAULT_PROJECT_WORKFLOW)?.name || "通用后端交付 SOP"}（默认）`
      : "";
  const fields = [
    [
      "项目",
      project ? projects.find((p) => p.id === project)?.name || project : "",
    ],
    ["名称", body.name || body.title],
    ["目标", draft.path?.id || body.id],
    ["TAPD 链接", body.url],
    [
      "代码仓库",
      repositoryIds(body)
        .map((id) => named("repository", id))
        .join("、"),
    ],
    ["SOP", sop],
    ["节点", body.nodeId],
    ["说明", body.description || body.reason],
  ].filter(
    ([, value]) => value !== undefined && value !== null && value !== "",
  );
  const effect =
    {
      issues_import_tapd:
        "从 TAPD 导入需求并关联所选仓库，导入后继续当前对话。",
      issues_dispatch: "为这条需求派发 SOP，创建新的运行。",
      runs_start: "启动一次新的 SOP 运行。",
      runs_approve: "批准此节点，流程将继续执行后续节点。",
      runs_reject: "退回此节点，流程将按 SOP 的退回路径继续。",
      issues_clarification_save: "保存澄清结果，并回写需求的外部来源。",
      projects_save: existingProject
        ? "修改这个项目的设置。"
        : "新建一个独立项目，不归属当前对话所在的项目。",
    }[draft.tool] || `${actionName(draft.tool)}，使用下方列出的目标与参数。`;
  return { title: actionName(draft.tool), fields, effect };
}
export function operationError(value) {
  const text =
    value instanceof Error ? value.message : String(value || "操作失败");
  try {
    const data = JSON.parse(text);
    if (typeof data.error === "string") return data.error;
    if (typeof data.error?.message === "string") return data.error.message;
  } catch {
    /* Plain text errors need no conversion. */
  }
  return text;
}
