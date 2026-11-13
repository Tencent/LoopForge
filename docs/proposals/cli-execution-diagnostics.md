# CLI 执行诊断与故障定位（v2）

日期：2026-11-13。基线：`43265cd`。

原问题是 CLI 卡住或报错后，多数错误被统一归并为“AI 助手生成失败”，stderr 与工具异常只留在服务端日志，且下一轮重试会覆盖按 session 保存的 generation 记录。

本方案分两步演进到 `version: 2` 离线自解释诊断档案：既在界面给出明确、安全的错误分类与建议，又让用户导出的单份 JSON 文件包含**环境版本、进程生命周期、平台 MCP 网关工具记录、终止原因、时间线与证据边界评估**，实现“拿到导出文件即可定界并定位问题”。按用户要求，运行中界面不增加“已运行多久 / 最近多久无进展”等静默提醒，不影响长耗时任务。

## 用户体验

- **具体错误卡片**：失败回复显示原因、稳定错误码、处理建议，并提供“查看详情 / 复制诊断 / 下载诊断”。即使 CLI 零输出失败，对话中仍保留该条失败卡片。
- **零输出停止可追溯**：运行中和成功回复提供按需打开的紧凑“诊断信息”；若因长时间零输出由用户点击“停止”，即使未写入空白聊天消息，页面当前轮次下方仍保留“诊断信息”入口供复制或下载快照。
- **结构化详情视图**：展开详情后可直接查看自动诊断结论（`assessment`）、证据与未知边界、结束原因（`termination`）、运行实例与版本（`environment`）、每次调用的 CLI 版本/端点/进程状态/信号/输出行数与最近输出时间、工具与网关异常（`tools`）以及可折叠的执行时间线（`timeline`）和起始/尾部 stderr。
- **重试不丢现场**：每轮诊断按 `generationId` 独立持久化，重试或继续对话后仍可随时导出历史失败轮次的诊断文件。

## 诊断文件结构（Version 2）

```text
GET /api/assistant/sessions/:id/generations/:generationId/diagnostics
GET /api/assistant/sessions/:id/generations/:generationId/diagnostics?download=1
```

单份导出的 JSON 包含以下核心维度：

1. **环境与实例（`environment` + 调用级配置）**
   - 平台侧：`platformVersion`、`revision`、`modified`、`goVersion`、`os`、`arch`、`instanceId`、`instanceStartedAt`。服务端关键诊断日志统一携带 `diagnostic=<generationId> instance=<instanceId>`，多副本部署可秒级关联后台日志。
   - 调用侧（`attempts[]`）：`cliVersion`、`versionSource`（`managed_inventory` / `package_metadata` / `native_handshake` / `not_reported`）、`binarySource`（`explicit_override` / `managed_inventory` / `discovered`）、只保留 scheme 与 host 的 `endpoint`（不含路径、凭据和查询参数）、`protocol`、`effort`、`nativeSessionId`、`nativeTurnId`。
2. **CLI 进程现场（`attempts[].process`）**
   - 记录 `pid`、`state`（`not_started` / `running` / `start_failed` / `exited` / `unknown_after_interruption`）、`startedAt`、`finishedAt`、`signal`（捕获 SIGKILL/SIGTERM 等系统信号）、`userCpuMs`、`systemCpuMs`、`stdoutLines`、`stderrLines`、`firstStdoutAt`、`lastStdoutAt`、`firstStderrAt`、`lastStderrAt`。
   - 结合时间线的 `cli.initialized`、`model.turn_started`、`model.first_output_delta`、`protocol.result_received`，无需采集正文即可区分“卡在进程启动”、“卡在握手/MCP 列表”、“卡在等待模型首包”或“流式中途断流”。
3. **工具与平台 MCP 网关调用（`tools[]`）**
   - 同时覆盖 CLI 协议上报（`source: "cli"`）与平台会话 MCP 网关实收请求（`source: "gateway"`），自动关联当前 `attemptId`。
   - 记录 `mcp.gateway_initialized`、`mcp.tools_listed`（含工具数与耗时）及 `mcp.upstream_catalog_failed`（外部 TAPD/工蜂 MCP 拉取工具目录失败不再静默吞掉）。
   - 仅在工具失败时提取脱敏错误片段、`statusCode`、`requestId`、`traceId`；成功工具绝不保留输入参数与返回正文。未结束的工具在中断后标记为 `unfinished`，可直接定位卡死在哪个外部工具调用。
4. **抗冲刷首错保留（`firstError`、`firstStderr`、`dropped`）**
   - 保留前 8 行 `firstStderr` 与最近 20 行 `stderr`，防止后续重试冲掉初始报错。
   - 自动解析结构化 stderr 中的 `sampling_error`（如首轮重试时的上游 `401`/`429` 及 `request id`），过滤 Codex 流式顺序无害日志（如 `without active item`），并将泛化占位重连提示（`Reconnecting...`）升级为首个实质性错误事件存入 `firstError`。
   - 所有截断项按类别计入 `dropped`。
5. **终止归因与离线自解释评估（`termination`、`lastCheckpointAt`、`assessment`、`coverage`）**
   - 明确区分 `USER_CANCELLED`（用户主动停止；若停止前已捕获 `firstError` 会主动提示）、`USER_STEER`（中途转向）、`WORKSPACE_LEASE_LOST`（工作区续租失败）、`CHECKPOINT_FAILED`、`GENERATION_SUPERSEDED`、`SERVER_PANIC` 以及 `CHECKPOINT_LOST`（超过 45 秒未收到运行实例心跳，附带 `lastCheckpointAt`）。
   - `domain.AnalyzeAssistantDiagnostics` 纯确定性离线运行，明确分离**已观测证据（`evidence`）**、**受限于边界无法仅凭该文件判定的原因（`unknowns`）**与**下一步排查动作（`nextSteps`）**，绝不捏造根因或伪造分布式 `traceId`。

## 存储与隐私安全边界

- 新增手工迁移脚本 `migrations/029_assistant_diagnostics.sql`（遵循仓库规则，不在启动代码做 seed 或数据回填）。
- 权限校验与会话非正文附件一致：验证登录态与项目/会话访问范围，公开项目会话只读链接仅暴露固定文案摘要，不暴露详细诊断片段。
- 脱敏覆盖用户与平台注入的各类 API Key、Agent CLI Key、Git Token、Refresh Token、Authorization 头、常见密码/密钥键值、PEM 私钥、URL 查询参数/凭证段（含 `/mcp/` 会话令牌路径）、本机绝对路径及 ANSI 控制序列。不采集提示词、思考正文、工具参数、成功工具输出、环境变量或完整 stdout。
