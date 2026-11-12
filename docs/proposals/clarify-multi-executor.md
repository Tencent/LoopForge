# 需求澄清开放多执行器（智能体框架）方案

状态：设计建议，尚未修改澄清执行链路。

## 意图与背景

需求澄清目前只能跑在 CodeBuddy 上：`internal/httpapi/clarify_session.go:231` 硬编码
`ExecutorDefinition{Kind:"codebuddy"}`，`internal/httpapi/stream.go:454` 只查 CodeBuddy
二进制，`stream.go:624-630` 只装配 CodeBuddy 凭据，`internal/httpapi/clarify_mcp.go:25-27`
的 `clarifyAllowedModel` 还会把任何非 CodeBuddy 目录里的模型 ID **静默改写**回 CodeBuddy
默认模型。

前端其实已经把全量模型目录（含 `codex:`、`claude-code:`、`opencode:` 等带引擎前缀的 ID）
列进了澄清选择器（`web/app/sync-schedule.js:141` 调 `renderList`），所以现状是「界面能选、
后端偷偷改回去」——用户无法自定义澄清用的框架，被绑死在 CodeBuddy 上。

诉求：澄清可以使用平台上**已配置的全部智能体框架**，凭据取个人设置里对应那一项。

平台里这套机制已经存在：AI 对话（助手）早已支持 CodeBuddy、Codex、Claude Code 与七个
agent CLI（opencode、cursor、agy、kimi、pi、dsh、zcode），包含原生 MCP 终端工具、按引擎的
命令行方言、输出协议翻译、续跑会话簿记与凭据绑定（`assistant_engine_cli.go`、
`assistant_engine_codex.go`、`assistant_engine_agentcli.go`、`runtime/provider_*.go`、
`runtime/agent_cli.go`）。本方案是**把澄清接到这套已有机制上**，不是新造一套。

## 目标

1. 澄清可选引擎 = **需求责任人**已配置的引擎全集：CodeBuddy、Codex、Claude Code，以及
   opencode / cursor / agy / kimi / pi / dsh / zcode。
2. 选择分两层：个人设置里存一份全局默认（框架 + 模型）；澄清入口可临时改，临时选择只存
   浏览器本地。优先级：本次临时选择 > 个人默认 > CodeBuddy。
3. 两处入口一致：新建需求向导（`#reqClarifyModelPicker`）与需求详情「澄清」页
   （`clarifyModelPickerHTML`）。
4. 选择器为单控件，菜单按引擎分组；不可用的引擎整组置灰并写明原因。
5. 修掉静默改写：非法或不可用的引擎/模型组合直接报错，不再悄悄换回默认。
6. 现有 CodeBuddy 用户行为不变（默认引擎、模型校验、会话续跑）。

## 非目标

- 不给澄清增加「思考强度（effort）」控件；澄清现在没有，且 kimi / dsh / zcode 本身不支持
  effort（`domain.ExecutorEffortLevels`、`ValidateAgentEffort`）。
- 不改通知 AI 汇总（仍用 CodeBuddy），不改 AI 定时任务（本来就用任务自己选的执行器）。
- 不做按项目的澄清默认，只有一份全局个人默认。
- 不把澄清派发到 runner 机器执行，仍在控制面本机跑。
- 不新增引擎种类，只开放目录里已有的。

## 反目标（技术上跑通也算失败）

- **静默回退**：用户选了 X，实际用 CodeBuddy 跑完还不说明。
- **列出跑不起来的组合**：菜单里能选，点下去才报 500，或报一句看不出原因的错。
- **换引擎丢结论**：切换框架导致已生成的澄清结论、已回答的问题、对话历史丢失。

## 约束

- 澄清始终在控制面本机执行（`runtime.ExecuteLocalCommand`，`stream.go:757`），且 agent CLI
  的凭据不会下发到 runner（`runtime.PrepareRemote` 不做凭据装配，
  `internal/domain/runner.go:374-378` 会清空），因此不存在「派到装了该 CLI 的机器上跑」这条
  退路。
- 二进制可用性不一致：镜像预装 CodeBuddy 与 Codex（`Dockerfile:31`）；Claude Code 首次使用
  从 npm 安装（`internal/runtime/claude_install.go`，需外网）；七个 agent CLI **不在托管清单**
  内（`internal/managedcli/providers.go:24-39` 只覆盖 codebuddy/codex/claude-code），必须宿主
  机预装，平台只按 `AGENTFLOW_<KIND>_BIN` → PATH → 常见安装路径查找
  （`resolveAssistantEngineBin`，`assistant_engine_cli.go:61-95`）。
- dsh 的 MCP 依赖宿主机安装 `@deepseek-ai/dsh-mcp-client`（`runtime/agent_cli.go:181-190`）。
- 部分 CLI 必须填 Base URL 才可用：opencode、pi，以及 agy 的 chat 协议
  （`AgentCLINeedsBaseURL`，`agent_cli.go:52-63`）。
- 仓库规则（AGENTS.md）：新增用户字段归 `UsersRepo`，只允许 gorm AutoMigrate 增量补列，
  不写代码内迁移或 seed；无数据迁移需求，因此不需要 `migrations/` 脚本。澄清相关方法仍归
  `streamHandlers`，不新建 handler 领域。

## 已由需求方决定的部分

| 决定 | 内容 |
| --- | --- |
| 引擎范围 | 三个内置执行器 **加上** 其余 agent CLI，即已配置引擎全集 |
| 选择层级 | 个人设置默认 + 澄清时临时改，两者都要 |
| 选择器形态 | 单控件，菜单按引擎分组（mockup 方案 A），位置沿用现有工具条那一格 |
| 设置页位置 | 「个人配置 → 模型与执行」组顶部单独一行偏好（mockup 方案 A） |
| 不可用引擎 | 仍然列出来，整组置灰并写明原因，不隐藏 |
| 凭据与可用性归属 | 按**需求责任人**（无责任人则项目 Owner），不按点击者 |
| 验证范围 | 只要求 Codex、Claude Code 与一个 agent CLI 真机验证；其余按同一套机制镜像实现 |

## 实现方补充的决定（本文作者判断，非需求方原话）

- **默认引擎仍是 CodeBuddy**：未设个人默认、未临时选择时行为与今天完全一致。
- **一张需求记住自己澄清用的引擎**：CodeBuddy 的 session 无法被别的引擎续跑，所以引擎跟着
  这张单走；需求详情的选择器显示该单当前引擎。显式换引擎＝开新会话，把已有对话与已答问题
  重放给新引擎（复用现有 `resume=false` 分支，`stream.go:656-670`），已出结论不丢。浏览器里
  的临时选择只影响「新开始的澄清」，不改别人或别的浏览器已经在跑的单。
- **默认值失效不静默**：个人默认所指引擎的 Key 被清空或 CLI 不可用时，回退 CodeBuddy，但
  选择器与设置页都显式标注「已失效，当前用 CodeBuddy」；用户当场明确选中的引擎缺凭据则直接
  报错，不退回。
- **一个值表达引擎 + 模型**：沿用现有带前缀的模型 ID（`codex:gpt-5.5`、`opencode:…`，
  解析规则 `assistantModelEngine`，`assistant_engines.go:38-45`）。临时选择继续存
  `localStorage['agentflow.clarifyModel']`；个人默认存 users 新增列（空 = 用 CodeBuddy）。
- **置灰原因枚举**（菜单里必须能看懂为什么不能选）：未配置 API Key；需要 Base URL 但未填；
  控制面未安装该 CLI；dsh 缺少 MCP 扩展依赖；该引擎无任何可选模型。opencode / pi 内置模型
  目录为空但允许自定义模型（`customModel:true`），这种情况给一项「CLI 默认模型」，不算不可用。
- **结论落地机制统一**：所有引擎都走原生 MCP 终端工具 `submit_clarification`
  （`agent_tool_terminal.go:26`，网关是引擎无关的 HTTP MCP）；某个 CLI 没调用该工具时，沿用
  现有文本 JSON 兜底与重试（`recoverNativeClarification`、`extractClarifyResultJSON`、
  `nativeShouldRetryTerminal` + `SetTerminalOnly`）。
- **SubmitOnly 重试降级**：重试时收窄工具的做法（追加 `--disallowedTools Read,Grep,Glob`，
  `clarify_session.go:227-247`）在 opencode / cursor / pi 上没有 1:1 等价（它们只有整体
  ReadOnly 档），这些引擎退化为「保持 ReadOnly，不逐工具收窄」，并在重试提示词里明确要求直接
  提交结论。
- **权限档直接复用 AI 对话的只读档**：今天的澄清就已禁掉 `WebFetch` / `WebSearch`
  （`agent_tool_native.go:19` 的 `nativeDisallowedTools`），外部原单与文档只能走 MCP 读取，
  因此 chat 的 ReadOnly 档（`AgentCLIMCPOptions{ReadOnly:true}`，`agent_cli.go:117-121`）
  就是澄清要的档位，澄清专属差异只有上面那条 SubmitOnly 收窄。
- **可用性按责任人判定，并向同项目成员暴露布尔状态**：选择器的置灰/可用来自需求责任人的凭据
  配置与控制面的 CLI 安装状态，界面写明「将用责任人 XXX 的凭据」。代价是同项目成员能看到
  「责任人有没有配某个框架的 Key」这一布尔值（不暴露 Key 本身）。新建需求向导里草稿责任人即
  当前用户（`assigneeUserId` 取 `state.currentUser.id`），该入口两者一致。
- **未在真机验证的引擎照实标注**：未被选中的 agent CLI（agy / kimi / pi / dsh / zcode 等）按
  同一套映射实现，CHANGELOG 与本文的验证记录写明哪些没有真机跑过；它们的 SubmitOnly 行为
  一律取保守降级（保持整体只读，不逐工具收窄）。

## 需要改动的地方（要点，不是实现步骤）

1. **命令构造**：`prepareClarifyCommandWith`（`clarify_session.go:205-247`）改为引擎感知——
   CodeBuddy / Claude Code 方言、Codex（MCP 走 `-c mcp_servers…` 或私有 `config.toml`，续跑是
   `exec resume <threadID>` 子命令，护栏是 PreToolUse 脚本而非 `--disallowedTools`）、agent CLI
   走 `runtime.AgentCLICommand(kind,…)`；权限档复用 chat 的 ReadOnly 档，澄清专属差异只有
   SubmitOnly 重试时的工具收窄（见上）。
2. **二进制解析**：`resolveCodeBuddyBin`（`stream.go:454`）→ 按引擎用
   `resolveAssistantEngineBin`，缺失时用 `assistantCLIMissingError` 那类可读错误。
3. **凭据装配**：`stream.go:622-625` → 复用 `assistantProviderCredentials`
   （`assistant_provider.go:59-63`），含 `RunCredentials.AgentCLI`；`BindAgentCLI` 需要私有
   HOME（`runtime/command_binding.go:112-131`），澄清已有 per-user CLI home
   （`clarifyCLIHome`），要确认两者不打架。
4. **模型校验**：`clarifyAllowedModel`（`clarify_mcp.go:25-27`）→ 换成引擎感知校验（参考
   `assistantAllowedUserModel`，`assistant_engines.go:281-294`），非法返回 400，绝不静默改写。
5. **MCP 配置文件**：`writeAgentToolFiles`（`agent_tool_native.go:51-92`）目前写的是含
   CodeBuddy 专属 `defer_loading` / `tools` 键的配置；按引擎输出各自格式（Claude 要删 `tools`
   键、Codex 转 `-c mcp_servers`、agent CLI 用 `WriteAgentCLIMCPConfig`），剥离逻辑参考
   `prepareAssistantAgentCLIInvocation`（`assistant_engine_agentcli.go:22-32`）。
6. **输出解析**：`runClarifyTurn`（`stream.go:735-757`）现在把原始 stdout 直接喂给
   `clarifyStreamEvents`；agent CLI 必须先过 `NewAgentCLITranslator(kind)`
   （`agent_cli_stream.go:162-180`）翻成 stream-json，Codex 走 jsonl。
7. **续跑判断**：`canResumeClarifySession` glob `~/.codebuddy/projects/*/<id>.jsonl`
   （`clarify_session.go:31-49`）→ 按引擎分流：Codex thread（`codex-threads.json`）、Claude
   的 UUID 映射（`assistantClaudeSessionID`）、agent CLI 的 `agent-cli-sessions.json`；引擎与
   已存会话不一致时不复用。
8. **工具词汇**：澄清提示词与思考流里的工具名（Read/Grep/Glob、`clarifyToolDetail`）是
   CodeBuddy 词汇；其他引擎有自己的读工具名，提示词与步骤标签需按引擎适配，否则会指挥模型去
   调用不存在的工具。
9. **仓库物化假设**：`materializeClarifyRepo` 的理由是「CodeBuddy 的 Read/Grep/Glob 不跟随
   out-of-tree symlink」（`clarify_session.go:104-113`）；需复核其他引擎的读工具行为，决定是
   保留全量拷贝还是按引擎放宽。
10. **前端**：分组菜单（`renderOptions` 目前平铺，`assistant-models.js:89-121`）、置灰原因、
    设置页那一行偏好、两处入口联动、单张需求记住引擎的展示。
11. **API 与持久化**：`chatClarifyRequest.model` 的语义扩展为带引擎前缀的 ID；Issue 的
    `Clarification` 需要记录本轮澄清所用引擎（与已有 `SessionID`/`SessionUserID` 同层）；
    个人默认存 users 新列（归 `UsersRepo`，gorm AutoMigrate 补列）。另需一个「按需求或项目
    解析责任人引擎可用性」的查询端点，返回每个引擎的可用状态与不可用原因供选择器置灰；它读的
    是责任人的凭据配置与控制面 CLI 安装状态，绝不返回 Key 本身。
12. **文档**：`README.zh-CN.md:103`（「需求澄清和通知 AI 汇总目前使用 CodeBuddy」）、
    `README.md` 对应段、`CHANGELOG.md`、`design.md`、`docs/api/collaboration.md` 的澄清章节、
    `docs/api/openapi.yaml` 的 `ChatClarifyRequest`。

## 验收标准

- 在新建需求向导里选一个 Codex 模型并开始澄清：实际执行的是 codex 二进制（argv 可证），
  结论通过 `submit_clarification` 正常落地，思考过程在前端正常滚动，多轮续跑正常。
- 同样验证一个 agent CLI（至少 opencode 或 cursor 之一）与 Claude Code。
- 未配 Key、未装 CLI、缺 Base URL 的引擎在菜单里置灰，并能看到具体原因；点不动。
- 个人设置的默认在「新建需求向导」和「需求详情澄清页」两处都生效；在澄清入口临时改不会写回
  设置。
- 一张已用 X 引擎澄清过的需求，继续澄清仍是 X；显式换成 Y 后，历史对话、已答问题、已出结论
  都还在。
- 未设默认、未临时选择的部署，澄清行为与改动前一致（CodeBuddy + 原模型校验 + 原续跑）。
- 缺凭据时明确选中的引擎报错，错误里说清缺什么，且不出现「实际用 CodeBuddy 跑完」的情况。
- 责任人与点击者不是同一人时：选择器展示的是责任人的可用性，界面标明用谁的凭据；责任人未配
  该引擎 Key 时该项置灰并写明原因，不会让人先选中、跑起来才失败。
- 真机验证覆盖 Codex、Claude Code 与一个 agent CLI（opencode 或 cursor）；其余引擎按同一套
  映射交付，未验证的事实在 CHANGELOG 与本文如实标注。
