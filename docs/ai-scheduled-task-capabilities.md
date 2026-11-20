# AI 定时任务执行权限

本分支基于 master `1909b172`（2026-11-04）。AI 定时任务入口已从需求页拆出；`/api/schedule-jobs` 仍以 `skipDispatch` 区分提示词任务与触发配置，现有记录无需迁移。

根据后续要求，AI 定时任务现在复用主页 AI 对话的「完全权限」策略，不再施加定时任务专有的 SOP 限制。

| 能力 | 当前行为 |
| --- | --- |
| 平台读写 | 与主页完全权限共用 `assistantExecModeForApproval(AssistantApprovalAuto)`，写入直接生效，无需逐项确认。 |
| SOP | 可按提示词启动、重试 SOP、发布流程，并管理和运行触发配置；不再设置 `DeferSOP`。原有项目访问校验、运行状态和并发规则继续由同一平台 API 执行。 |
| 工作区与浏览器 | 共用主页的 `AgentToolAssistant` 网关，提供工作区打开、读写、命令、提交 MR 和浏览器工具。执行前创建定时来源会话，工作区绑定可持久化；执行期间维护租约，结束时保存并释放。 |
| MCP | 使用执行账号在当前项目与个人安装的 HTTP / SSE MCP，工具发现与调用策略与主页一致；不接入 stdio MCP。 |
| Skill | 与主页现有能力一致，不额外禁用内置 Skill 工具。启动 SOP 后，节点会按原有逻辑装载角色绑定的 Skill。主页与定时对话均未独立提供平台 Skill 绑定和自动装载功能，本次不新增这套机制。 |
| 结果与产物 | 支持自然语言结果，以及主页相同的草稿提交回退；终止 CLI 后仍按完全权限落库并保留已提交结果。工作区附件和浏览器截图写入定时会话。 |

执行身份由最后保存任务的人决定。未绑定身份的旧任务按项目 Owner、独立任务记录的 Owner 的顺序查找个人凭据。Codex 和 Claude Code 优先使用执行账号的默认个人执行机器上的「账号登录」，需要 Runner 0.10.2 或以上版本，并启用对应 CLI。默认机器离线或未启用账号登录时，任务改用执行账号在个人配置中填写的 API Key，以所选 CLI 和模型在云端执行。Key 缺失或执行失败时记录具体原因。CodeBuddy 缺少个人 Key 时可以使用平台共享 Key，模型限于管理员允许的列表。保存时会校验可用的凭据。

工具访问范围由执行账号和项目决定。关闭页面后任务继续执行，直到 CLI 结束或在会话页停止。MCP 网关在本轮结束时关闭。

由于任务以保存人（或项目 Owner）身份、完全权限执行，`/api/schedule-jobs` 的新建、修改、立即运行和删除都要求项目写权限（Owner、平台管理员或非 viewer 成员），列表只返回调用者所在项目的任务；按 id 保存时既校验原项目也校验目标项目。执行身份只由服务端决定，请求里的 `ownerUserId`、`runAsUserId` 不生效。项目记录已不存在的任务只允许平台管理员或该任务原 Owner 管理。

## 实现与验证

- `internal/httpapi/assistant_schedule.go`：按所选模型的 CLI 选择执行账号，不再给这一轮单独设超时。
- `internal/httpapi/assistant_tasks.go`：定时任务打开一轮普通对话（`executeGeneration` → `POST /api/assistant/chat`），系统提示词、技能、流式和停止都走主页路径；跑完再把结果写回任务。
- `internal/httpapi/agent_tool_session.go`：工具网关跟当前轮次的 context 一起结束，没有 5 分钟或固定时长的轮次上限。
- `TestScheduledAssistantCanDispatchSOP`：通过定时执行链路调用真实平台派发接口，使用内存测试数据和仅含审批节点的 SOP。
- `TestScheduledAssistantNativeGatewayKeepsWorkspaceAndTerminalReply`：模拟 CLI 通过本地回环网关提交最终回复，验证执行前会话、租约控制器初始化、绑定和附件留存。

触发配置仍可用于按状态扫描需求、执行基线门禁后自动派发 SOP；AI 定时任务也可以按其提示词主动调用 SOP。执行权限调整不依赖数据库迁移或实际环境变更。
