# 能力注册表 API

认证和通用状态码见 [README](README.md)。本页所有 `GET` 列表均返回完整 JSON 数组，不分页；所有 `PUT` 都是按 `id` 保存/覆盖。

## 初始化快照

### `GET /api/bootstrap`

返回 Studio 初始化所需的聚合对象，`200`：

```json
{
  "projects":[],
  "repositories":[],
  "agents":[],
  "rules":[],
  "skills":[],
  "mcpServers":[],
  "executors":[],
  "dockerfiles":[],
  "environments":[],
  "sandboxes":[],
  "workflows":[],
  "syncConfig":{},
  "scheduleJobs":[],
  "schedulerEnabled":true,
  "storage":{"driver":"mysql","database":true,"note":"..."}
}
```

项目和仓库敏感 token 已脱敏。这里的 Agent 是目录摘要：省略 instructions、taskPrompt、interventions、artifactOutputs、verification 等大字段，只保留标识、描述摘要、执行器/模型、规则/Skill/MCP 引用和简化 loop；获取完整定义请调用 Agent 详情。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" "$AGENTFLOW_URL/api/bootstrap"
```

## Agent

`AgentDefinition` 核心字段包括：

```json
{
  "id":"agent-dev",
  "name":"研发 Agent",
  "version":1,
  "description":"实现研发任务",
  "instructions":"...",
  "taskPrompt":{"role":"开发者","objective":"...","actions":[],"deliverables":[],"acceptanceCriteria":[]},
  "rules":["rule-secure"],
  "skills":["skill-go"],
  "mcpServers":["gongfeng"],
  "executorId":"codebuddy",
  "model":"model-name",
  "loop":{"enabled":true,"maxIterations":3,"exitCondition":"all_gates_pass","onExhausted":"human"},
  "interventions":[],
  "artifactOutputs":[],
  "verification":[]
}
```

### `GET /api/agents`

返回完整 `AgentDefinition[]`，`200`。传入 `projectId` 时仅返回该项目角色及其他项目明确共享的角色；无归属的历史角色不会作为全局角色返回。

### `PUT /api/agents`

`id`、`name`、`projectId` 必填（更新已有角色时可沿用其现有 `projectId`），`version` 为 `0` 时改为 `1`。成功 `200` 返回保存后的 Agent；存储/版本错误 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"id":"agent-dev","name":"研发 Agent","projectId":"platform","version":1,"rules":[],"skills":[],"mcpServers":[],"loop":{"enabled":false},"verification":[]}' \
  "$AGENTFLOW_URL/api/agents"
```

### `GET /api/agents/{id}`

返回完整 Agent，`200`；不存在 `404`。

### `GET /api/agents/{id}/revisions`

返回该 Agent 的 `DefinitionRevision[]`，`200`；Agent 不存在 `404`。revision 含 `id`、`kind`、`entityId`、`version`、`revision`、`name`、`summary`、`payload`、`createdAt`。

### `GET /api/agents/{id}/revisions/{revisionId}`

返回一个属于该 Agent 的历史版本；不存在或版本不属于该 Agent 时返回 `404`。

### `POST /api/agents/{id}/revisions/{revisionId}/restore`

无请求体。成功 `200` 返回恢复后的 Agent；Agent 不存在 `404`；revision 无效或恢复失败通常 `400`。

### `DELETE /api/agents/{id}`

删除角色定义。前置条件：角色未被任何流程引用且不处于共享状态，否则返回 `409` 并说明原因；成功返回 `204`；角色不存在 `404`。

## 规则与 Skill

### `GET /api/rules`

返回 `RuleDefinition[]`，字段为 `id`、`name`、`projectId`、`shared`、`description`、`content`、`priority`、`updatedAt`，`200`。可用 `projectId` 查询参数只返回指定项目资产。

### `PUT /api/rules`

`id`、`name`、`projectId` 必填。`shared=true` 后其他项目可只读使用；仍被共享角色引用时不能取消共享。成功 `200 {"ok":true}`；失败 `400/404/409`。

### `DELETE /api/rules?id={id}`

删除规则。前置条件：未共享且未被任何角色引用，否则 `409`；成功 `204`；缺少 id `400`；不存在 `404`。

### `GET /api/skills`

返回 `SkillDefinition[]`，字段为 `id`、`name`、`projectId`、`shared`、`description`、`content`、`files`、`updatedAt`，`200`。可用 `projectId` 查询参数只返回指定项目资产。

### `PUT /api/skills`

`id`、`name`、`projectId` 必填；`files` 是相对路径到文件内容的映射。`shared=true` 后其他项目可只读使用；仍被共享角色引用时不能取消共享。成功 `200 {"ok":true}`；失败 `400/404/409`。

### `DELETE /api/skills?id={id}`

删除技能及其 `skill_files` 二进制文件。前置条件：未共享且未被任何角色引用，否则 `409`；成功 `204`；缺少 id `400`；不存在 `404`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"id":"rule-secure","name":"安全规范","content":"不得提交密钥","priority":100}' \
  "$AGENTFLOW_URL/api/rules"
```

## MCP Server

`MCPServerDefinition` 字段为 `id`、`name`、`description`、`transport`、`command`、`args`、`workingDirectory`、`installScript`、`fileRefs`、`url`、`environment`、`headers`、`authType`、`timeoutSec`、`updatedAt`。`authType=taihu-pat` 表示运行时注入需求责任人的太湖个人令牌，资产本身不得保存个人 Token。stdio 上传文件的内容保存在 `mcp_files`，payload 只保存 `fileRefs` 清单。

### `GET /api/mcp-servers`

返回 `MCPServerDefinition[]`，`200`。

### `PUT /api/mcp-servers`

`id` 必填。`transport=http` 时要求 `http://` 或 `https://` URL；`transport=stdio` 或空时要求 `command`；`timeoutSec` 范围为 0–600。成功 `200 {"ok":true}`，并重新加载同步配置；校验失败 `400`。

### `POST /api/mcp-servers/:id/files`

上传 stdio MCP 文件夹或 ZIP，multipart 文件字段为 `files`，文件夹相对路径通过同序 `paths` 字段传递。上传批次替换该 MCP 的完整文件树，内容存入 MySQL；单文件 1MB、总量 64MB、最多 500 个文件。ZIP 路径穿越、绝对路径和符号链接会被拒绝。

### `GET|DELETE /api/mcp-servers/:id/files/:path`

下载或删除一个上传文件。删除后同步刷新 MCP payload 的 `fileRefs`。

### `POST /api/mcp-servers/import`

批量导入。请求可为：

- 单个 MCP 对象；
- MCP 对象数组；
- `{"mcpServers": ...}` 或 `{"servers": ...}`；
- 以 server id 为 key 的对象映射。

所有条目逐项校验、保存；成功 `200`：

```json
{"imported":2,"servers":[{"id":"gongfeng","name":"工蜂","transport":"http","url":"https://..."}]}
```

无效 JSON/空列表/条目校验失败返回 `400`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X POST \
  -H 'Content-Type: application/json' \
  -d '[{"id":"docs","name":"文档 MCP","transport":"http","url":"https://mcp.example.com","timeoutSec":30}]' \
  "$AGENTFLOW_URL/api/mcp-servers/import"
```

## 执行器

### `GET /api/executors`

返回只读的三个内置 CLI：CodeBuddy、Codex、Claude Code。CLI 不允许通过 API 新增或修改；模型在 Agent 中设置，API Key/Base URL 使用需求责任人（未指定时为项目 Owner）的个人配置。

## Dockerfile、Environment 与 Sandbox Profile

### `GET /api/dockerfiles`

返回 `DockerfileDefinition[]`，`200`。字段为 `id`、`name`、`description`、`content`、`files`、`updatedAt`。

### `PUT /api/dockerfiles`

`id`、`name`、`content` 必填；`files` 的 key 必须是安全的相对路径。成功 `200 {"ok":true}`；校验失败 `400`。

### `DELETE /api/dockerfiles?id={id}`

删除 Dockerfile。前置条件：未共享且未被任何沙箱引用，否则 `409`；成功 `204`。

### `GET /api/environments`

返回 `EnvironmentDefinition[]`，`200`。字段为 `id`、`name`、`description`、`variables`、`pathEntries`、`installScript`、`updatedAt`。

### `PUT /api/environments`

`id`、`name` 必填；变量名不得含 `=`/NUL，`pathEntries` 必须是安全相对路径。成功 `200 {"ok":true}`；校验失败 `400`。

### `DELETE /api/environments?id={id}`

删除环境安装脚本。前置条件：未共享且未被任何沙箱引用，否则 `409`；成功 `204`。

### `GET /api/sandboxes`

返回 `SandboxProfile[]`，`200`。字段为 `id`、`name`、`driver`（`local|seatbelt|bwrap|docker`）、`dockerfileId`、`environmentId`、`readOnlyRootFS`、`environment`。

### `PUT /api/sandboxes`

`id` 必填，成功 `200 {"ok":true}`；校验/存储失败 `400`。

### `DELETE /api/sandboxes?id={id}`

删除沙箱配置。前置条件：不是内置 `local`、未共享、且未被任何 SOP 引用，否则 `409`；成功 `204`。

```bash
curl -u "$AGENTFLOW_USER:$AGENTFLOW_PASS" -X PUT \
  -H 'Content-Type: application/json' \
  -d '{"id":"sandbox-go","name":"Go 隔离环境","driver":"docker","dockerfileId":"go-1.23","readOnlyRootFS":false}' \
  "$AGENTFLOW_URL/api/sandboxes"
```

`/api/sandboxes` 仅管理供 SOP 引用的配置，不创建、查询或回收运行时沙箱；沙箱回收不是公共 API。
