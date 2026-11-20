# LoopForge 知识库用户与运维指南（源码核实版）

> 本文档所有菜单入口、按钮名称、环境变量、命令及错误排查均严格核实自 `LoopForge-knowledge-mcp/` 当前源码。
> 若您使用的是未合入知识库前端的 `LoopForge/` 主仓，控制台暂未挂载「知识中心」与 Chatbot 知识选择器（仅角色编辑器中存在「接入团队知识库」开关）。

---

## 1. 用户怎么用（控制台与对话交互）

### 1.1 进入知识中心

1. 登录 LoopForge 控制台，在左侧边栏 **「Agent 能力」** 分组下点击 **「知识中心」**（`data-view="knowledge"`，对应源码 `web/index.html` 与 `web/app/knowledge.js`）。
2. 页面顶部提供 4 项实时指标：**接入状态**、**项目可用知识库（已授权 / 总数）**、**待审核沉淀**、**已启用角色**，以及 4 个功能页签：
   - **知识库与文档**（`sources`）
   - **知识沉淀**（`distill`）
   - **接入配置与用量**（`settings`）
   - **排障调试**（`search`）

### 1.2 新增、测试与配置知识库

进入 **「知识中心 -> 接入配置与用量」**（需管理员权限）：

- **方式一：一键接入 WeKnora（推荐）**
  - **内置实例（`managed`）**：选择「内置实例」，点击一键启动与接入，平台自动拉起 WeKnora 容器组并初始化 `full_access` API Key 写入配置。
  - **外部实例（`external`）**：填写已有 WeKnora 服务地址（如 `http://host:18080`），填写 API Key **或** WeKnora 登录邮箱与密码，点击接入后系统自动完成连通性校验并保存为一条独立连接。
- **方式二：通用 / LightCloud MCP 接入**
  - 在配置表单中选择引擎（`weknora` 或 `lightcloud`），填写服务地址（LightCloud 需为 `http(s)://host/mcp` 形式）与 Token/API Key。
  - 勾选 **「启用知识中心」** 与按需勾选 **「允许审核后写回」**（`writeEnabled`）。
  - 可配置默认授权来源（`defaultSources`）或按项目覆盖授权来源（`projectSources`）。
  - 点击 **「测试连接」**（调用 `POST /api/knowledge/check`）验证连通性与可见来源数量，确认无误后点击 **「保存配置」**。
- **管理文档与排障测试**：
  - 在 **「知识库与文档」** 页签可浏览已授权知识库、搜索文档、点击预览分块正文；如需上传/管理 WeKnora 文档，可点击 **「管理此知识库 ↗」** 跳转至 WeKnora 控制台（默认 `http://<host>:18081`）。
  - 在 **「排障调试」** 页签输入关键词并选择目标来源，点击检索即可直接验证召回片段与原文打开是否正常。

### 1.3 绑定角色、SOP 节点与对话触发

1. **角色级全局绑定（传统开关）**：
   - 进入角色编辑面板（`web/app/flows.js`），在平台能力区打开 **「接入团队知识库」**（`knowledgeBase`）开关并保存。该角色在运行时将自动获得 `agentflow-knowledge` 只读 MCP 与「知识库使用规范」规则。
2. **SOP 节点级精细绑定**：
   - 在 SOP 流程设计器中选中任意 **Agent 节点**，在右侧属性栏点击 **「关联知识库」**（`#nodeKnowledgeRefs`）。
   - 在弹出的 `editKnowledgeRefs` 弹窗中勾选目标知识服务，选择范围模式：**全部已授权来源（`all`）** 或 **指定来源白名单（`allowlist`）**，通过校验后保存工作流。
3. **Chatbot 对话触发与效果查看**：
   - 在对话顶部栏或输入框上下文栏点击 **知识库选择器胶囊（`.knowledge-picker`）**，为当前会话绑定或调整知识库范围（持久化至 `PUT /api/assistant/sessions/:id/knowledge`）。
   - 提问涉及团队规范、私有接口或项目事实时，模型会按策略先调用只读知识工具检索；在助手回复气泡下方会展示可折叠的 **「知识库调用 · N」**（`.knowledge-reads`），可查看调用工具名、查询词、命中状态、告警信息与实际被引用的证据文档。
   - 在 SOP 运行详情的遥测面板（`web/app/runs.js`）中，同样会渲染 `knowledge.call` 事件及产物中引用的 `[[knowledge:<id>]]` 标记。

### 1.4 知识沉淀审核与写回

1. 启用了知识库的角色在完成任务时，可将可复用经验写入工作区根目录的 `knowledge.md`（支持 YAML front matter 指定 `title`、`source`、`path`）。
2. 在 **「知识中心 -> 知识沉淀」** 的候选列表中点击 **「发起审核」**，条目进入 `pending` 队列。
3. 管理员点击条目打开审核抽屉，可编辑标题、目标来源与内容，执行 **「驳回」**（记录 `reviewNote`）或 **「发布写回」**（需开启 `writeEnabled`，成功后状态变为 `published` 并记录 `commit`）。

---

## 2. 怎么配与怎么验（环境变量与最小命令）

### 2.1 核心环境变量（核实自 `compose.weknora.yaml`、`token.go`、`platform_mcp.go`）

| 环境变量 | 作用与默认值 | 适用场景 |
|---|---|---|
| `AGENTFLOW_SESSION_SECRET` / `SESSION_SECRET` | 签发 `afkb1.` 代理令牌的 HMAC-SHA256 密钥（未设置时退化为连接配置派生密钥） | 生产环境强烈建议配置 |
| `AGENTFLOW_PLATFORM_MCP_URL` | 平台对外基准地址（如 `https://loop.example`），用于将 `/runners/knowledge/mcp` 重写为远程 Runner 可达地址 | 使用远程 CLI Runner 或云端沙箱时必填 |
| `LOOPFORGE_WEKNORA_URL` | 平台容器访问 WeKnora 后端地址（默认 `http://weknora-app:8080`） | 启用 `--with-weknora` 时自动注入 |
| `LOOPFORGE_WEKNORA_UI_URL` | 控制台跳转 WeKnora UI 的基准地址（默认 `http://localhost:18081`） | 启用 `--with-weknora` 时自动注入 |
| `AGENTFLOW_DOCKER_NETWORK` | 内置模式（`managed`）创建/加入的 Docker 网络名（默认 `loopforge_default`） | 运行时一键拉起内置 WeKnora 时使用 |
| `LOOPFORGE_WEKNORA_ADMIN_PASSWORD` | 内置 WeKnora 默认管理员初始密码（未设则自动生成并落库） | 可选覆盖 |
| `WEKNORA_VERSION` / `WEKNORA_APP_PORT` / `WEKNORA_UI_PORT` | WeKnora 镜像版本（默认 `latest`）、后端端口（`18080`）、UI 端口（`18081`） | `compose.weknora.yaml` |
| `WEKNORA_DB_PASSWORD` / `WEKNORA_REDIS_PASSWORD` / `WEKNORA_JWT_SECRET` / `WEKNORA_SYSTEM_AES_KEY` | WeKnora 依赖的 Postgres/Redis 密码与加密密钥（由 `scripts/configure.sh` 在 `LOOPFORGE_WITH_WEKNORA=1` 时生成） | `--with-weknora` 部署必需 |

### 2.2 最小启动与验证命令

以下所有命令均需在 `LoopForge-knowledge-mcp/` 目录下执行：

1. **源码构建并连同内置 WeKnora 栈启动**：
   ```bash
   ./start.sh dev --with-weknora
   ```
2. **仅启动后台服务（使用预构建镜像 + WeKnora）**：
   ```bash
   ./start.sh --with-weknora
   ```
3. **查看服务与 WeKnora 容器日志（强制截断）**：
   ```bash
   docker compose -f compose.yaml -f compose.weknora.yaml logs --tail 80 platform weknora-app
   ```
4. **后端知识库与 HTTP/Engine 定向单元测试**：
   ```bash
   go test -count=1 ./internal/knowledge ./internal/httpapi -run TestKnowledge | tail -n 60
   go test -count=1 ./internal/engine -run "TestResolve.*Knowledge|TestKnowledge|TestMarkKnowledge" | tail -n 60
   ```
5. **前端嵌入式资源与 Chatbot 构建测试**：
   ```bash
   go test -count=1 ./web | tail -n 60
   bash scripts/build-chatbot.sh | tail -n 60
   ```

---

## 3. 常见失败场景排查（基于源码真实错误与状态码）

| 现象 / 报错信息 | HTTP 状态码 (`apperr`) | 源码根因与校验逻辑 | 排查与处理方式 |
|---|---|---|---|
| `知识库未接入：请在「知识库 → 接入配置」填写地址和 Token 并启用` | `503 Unavailable` | `settings.Ready()` 为 `false`（未启用、或所有连接缺少 URL/Token）。 | 前往「知识中心 -> 接入配置与用量」，确认连接已启用且 Token/API Key 已保存。 |
| `知识库地址必须是 http(s)://host/mcp 形式` / `WeKnora 地址必须是 http(s)://host 形式` | `400 Invalid` | `Service.UpdateSettings` 校验 URL Scheme 必须为 `http`/`https` 且 Host 非空。 | 检查 URL 格式是否缺少协议头或包含非法字符。 |
| `启用前需要至少启用一个有效的知识库连接` | `400 Invalid` | 存在 `connections[]` 列表但没有任何连接同时满足 `enabled && url != "" && token != ""`。 | 至少保留并启用一个有效连接，或取消勾选「启用知识中心」。 |
| `当前运行环境未挂载 docker CLI，请使用 ./start.sh --with-weknora 启动内置实例或改用外部实例模式` | `502 Upstream` | 在「内置实例 (`managed`)」模式下容器内无 `docker` 命令且 `weknora-app:8080/health` 不通。 | 改用宿主机执行 `./start.sh --with-weknora` 启动整套栈，或切换为「外部实例」填入已有 WeKnora 地址。 |
| `该知识库来源不在当前项目的授权范围` | `403 Forbidden` | `SourceAllowed(settings.SourcesForProject(projectID), source)` 返回 `false`。 | 在「接入配置」的默认来源或项目来源白名单中放行该知识库 ID（多连接模式下注意 `<connID>::<rawID>` 前缀）。 |
| `仅 Agent 节点可关联知识库` / `全部范围不能同时指定来源` / `指定范围至少需要一个来源` | `400 Invalid` | `domain.ValidateWorkflowKnowledge` / `ValidateKnowledgeRefs` 校验失败。 | 仅在 `type == "agent"` 节点绑定 `knowledgeRefs`；`mode == "all"` 时清空 `sources`，`mode == "allowlist"` 时至少选 1 个来源。 |
| `角色开启了团队知识库，但未注入 agentflow-knowledge：知识中心未启用或未配置知识库地址/Token` | 运行时告警 | `engine.knowledgeSkippedWarning` 检测到角色开启了开关但平台知识库未就绪。 | 完成平台知识库接入并确保至少有一个连接处于启用状态。 |
| 远程/云端 Runner 告警：知识 MCP URL 仅含 `/runners/knowledge/mcp` 被跳过 | 运行时告警 | `runtime.PrepareMCPServers` 未检测到 `AGENTFLOW_PLATFORM_MCP_URL`，远程容器无法回连相对路径。 | 在平台环境变量中配置可被 Runner 访问的 `AGENTFLOW_PLATFORM_MCP_URL`。 |
| `平台未开启知识写回：请管理员在「接入配置」中打开「允许审核后写回」` | `403 Forbidden` | `Service.Publish` 校验 `settings.WriteEnabled == false`。 | 管理员在「接入配置与用量」开启写回开关后再发布。 |
| `写入路径必须是仓库内的 .md 相对路径，例如 guides/xxx.md` | `400 Invalid` | LightCloud 模式下 `NormalizeDocPath` 拒绝非 `.md` 后缀或越权路径（`../`）。 | 在审核抽屉中将目标路径修改为合法的 `.md` 相对路径。 |
| `知识库不可达：...` / `读取原文失败：...` / `写回知识库失败：...` | `502 Upstream` | 上游 LightCloud MCP 或 WeKnora REST 接口超时（单次 `CallTimeout = 30s`，上传/写回 `60s`）或返回非 2xx 错误。 | 使用「排障调试」或查看 `platform` 与 `weknora-app` 容器日志确认网络连通性与上游 Token 权限。 |
