# 对话工作区与项目沙箱

普通对话中的 Main Bot 保持本地对话进程，首次需要修改仓库或运行命令时才调用 `workbench_workspace_open` 创建 Docker 工作区。项目不再配置默认对话沙箱：工具描述列出本项目和共享的 Docker 沙箱及项目仓库，由 Main Bot 按要改的代码选择 `sandboxProfileId` 和 `repositories`。不选环境时使用平台 `generic-docker`，不选仓库时检出项目全部仓库（默认仓库在前）；任何情况下都不使用宿主机。所选环境不可用（被删除、非 Docker、跨项目私有）时返回可操作错误，由 Main Bot 改选，不会被静默替换。

用户可在「高级设置 → 显示运行环境选择」中为会话指定环境。会话 `sandboxProfileId` 非空时，它优先于 Main Bot 的选择，工具描述也会告诉 Main Bot 不要再选。仓库不单独提供用户配置：Main Bot 按需求选择，拿不准时全部检出。

项目会话在入口统一校验项目访问权限。所有具备访问权限的成员（包括 viewer）都拥有同一会话的完整操作权限，可以继续讨论、编辑用户消息、停止任意成员发起的回复、选择运行环境、使用工作区和浏览器工具，以及归档或删除普通项目会话。内部不再按会话创建者、本轮发起人或项目编辑角色二次限制。会话的存储归属保持稳定，工作区绑定和历史对所有参与者一致；模型调用、Git 推送与 MR 仍使用当前操作人的个人凭据。

一个对话同一时间只绑定一个工作区（会话 `workspaceRunId`），用哪个由 Main Bot 按用户当前实际要处理的需求决定，@ 只是线索。`workbench_workspace_open` 的 `issueId`、`workspaceRunId`、`new` 最多传一个：都不传继续用当前绑定的工作区，还没有就新开；`issueId` 切到该需求最近一次顶层 SOP 运行的工作区，不论它在执行、暂停、成功、失败还是取消，需求必须跑过 SOP；`workspaceRunId` 切回本对话之前开过的工作区（会话 `ownWorkspaceRunIds`）；`new` 新开一个对话工作区。没跑过 SOP 的新需求是否沿用当前工作区也由 Main Bot 判断：仓库和环境合适就沿用，不合适就新开。每轮上下文列出当前绑定的工作区、之前开过的工作区，以及本条消息 @ 的需求和关联主需求各自有没有跑过 SOP。

切换时旧工作区解绑（`run_workspace_checkpoints.released_at`），空闲回收不再等一小时：下一次扫描保存快照后回收；切回或在 IDE 中再次使用会取消解绑。切回已回收的工作区时先恢复快照。SOP 仍在执行或暂停时不会被回收，它的生命周期仍由 SOP 自己决定；离开一个暂停中的 SOP 时，工具会提醒 Main Bot 它还没有继续。删除对话时，对话开过的所有工作区一并删除，借用的 SOP 工作区不受影响。

会话尚未关联需求时，消息里 @ 的需求（优先取跑过 SOP 的）自动成为关联主需求；在对话里派发需求（模型调用 `issues_dispatch`、自动草稿，或用户确认草稿，后者由聊天页通过 `X-AgentFlow-Chat-Session` 请求头带上会话）也会把该需求关联到会话。之后不 @ 需求的消息也会把它列给 Main Bot。SOP 总是准备自己的工作区，不再借用对话工作区。

使用 SOP 工作区时读写规则与任务工作区相同：执行中只读，暂停或结束后可写。Main Bot 通过 `workbench_workspace_sop` 直接控制这次 SOP，不经确认草稿（帮我批准模式仍经独立审查），请求按用户本人的权限走运行控制端点：`pause` 在当前节点停下供 Main Bot 改代码；`resume` 带着 Main Bot 的改动说明继续 SOP，由交付节点交付；`takeover` 取消执行中的 SOP，之后直接在它的工作区改。SOP 结束后（含成功交付），`workbench_workspace_submit_mr` 推送到它的工作分支并发起或更新 MR；暂停期间不能提交，以免与之后恢复的交付节点冲突。

对话工作区创建时冻结完整沙箱、Dockerfile、环境定义和检出的仓库，之后不再改变环境或增加仓库；返回中的 `missingRepositories` 或环境不符的提示让 Main Bot 用 `new` 新开一个工作区。每个仓库在各自目录下新建工作分支，`workbench_workspace_submit_mr` 只推送和发起相对基线分支有改动的仓库，没有任何改动时返回错误。

`PUT /api/assistant/sessions/{id}/sandbox` 接受 `{ "sandboxProfileId": "id 或空字符串" }`，具备项目访问权限的会话参与者均可调用；生成期间和任务会话拒绝变更。已有工作区不受影响，新选择只用于之后新开的工作区。普通会话创建时也可以提交 `sandboxProfileId`，后续普通会话保存不能覆盖已保存的选择或伪造工作区绑定。

对话工作区使用独立的 `Run.kind=workspace` 和 `conversation-workspace` 标识，不参与 SOP 调度或占用 SOP 配额；继续复用 Runner、沙箱、Git、终端、快照、租约和空闲回收。API 与 Runner 需一起升级，旧 Runner 对新工作区请求会收到明确的版本要求。

旧群协作的邀请、角色派发、群 MCP、清单、自动跟进及切回 SOP 写入口退出，对话只有一种状态。手动 SQL 把会话 workspaceRunId 指向的历史临时工作区转为 workspace；其余未结束的群运行连同节点与 Runner 任务一并置为 cancelled，其群会话成为已结束任务的普通任务会话，历史消息与交接材料保留。已取消的群记录没有可续跑的 SOP 图，run 控制接口拒绝对它重试或恢复。

部署时先停旧 API/Worker，手动执行 `migrations/020_conversation_workspaces.sql`，然后启动新版 API/Worker/Runner。不删除历史数据，也无需为项目配置对话环境。生产启动不执行数据迁移或 seed。

## 需求关联与统计

对话里直接改代码也要落到需求上。Main Bot 识别出明确需求时，在 `workbench_workspace_open` 的 `requirement { title, description }` 中提交；`workbench_workspace_submit_mr` 提交 MR 时若工作区仍未关联需求，优先复用会话已关联的未完成需求，否则以 MR 标题和说明创建。新建需求为本地 `requirement`、`in_progress`、`automationState=ignored`（不会被自动派发 SOP），负责人为当前用户，记录来源会话、工作区仓库和创建操作，并同时关联工作区与会话。已关联需求的工作区不再改关联；SOP 运行的需求只在派发时确定。

提交的 MR 写入该需求的交付 MR 并登记合入跟踪，需求转为 `in_review`；MR 合入默认分支后由 MR 轮询把需求标为完成。每轮对话的模型用量按回复模型定价累加到当前对话工作区。

数据统计中，对话工作区不是 Run：不计入运行数、成功率、耗时和「平均 Token / Run」，但其 Token 与成本计入总量、趋势、成员、项目和来源，并在 `summary.conversation*` 与趋势点 `conversationTokens/conversationCost*` 中单列。只经对话改代码的需求（`directDelivery`）在交付明细中按 MR 状态显示「对话改代码中 / 对话 · MR 待合入 / 对话 · MR 已关闭」，MR 合入默认分支后算已交付并显示「对话直接交付」；同一需求有 SOP 运行时仍按 SOP 口径。运行列表中对话工作区按关联需求或目标展示，不再显示为「自由协作」。

## 界面与样式

环境选择默认隐藏，普通用户由 Main Bot 自动选择环境。技术人员通过「＋ → 高级设置 → 显示运行环境选择」开启；显示偏好按用户保存在当前浏览器，隐藏入口不改变已选环境或已有工作区。开启后，环境选择与项目选择并排，复用同一个 `SelectMenu`，使用相同的搜索、选中态和弹出方向规则。菜单底部的「新增沙箱」打开当前项目的创建页，并默认选择 Docker；返回工作台后重新读取环境目录。普通对话随时可以改选环境，只影响之后新开的工作区；任务会话只展示任务环境。

旧协作样式中的全站主题、顶栏和页面框架规则归入 `web/app/frame.css`；仍由 SOP、角色等页面使用的模型控件样式归入 `web/assistant-models.css`。移除旧 UI 不改变其他页面的背景、搜索框或导航。

## 本地验证

- 原生对话前端 178 项测试通过；后端 HTTP、Engine、Runner、Repo、MCP 与 Web/OpenAPI 契约回归通过。
- 独立 Docker MySQL 验证了会话环境与生成启动的并发事务、SOP 配额，以及迁移脚本的作用范围与幂等性。
- 真实 CodeBuddy 对话分别选择项目默认和会话覆盖 Docker 环境，命令读取到 `business-default` 与 `conversation-override`，文件写入与验证命令均返回 0。新增沙箱入口也已通过实际界面创建验证。
- 全量测试尚有独立于本次修改的既有问题：MySQL 分析统计覆盖用例在未修改的 master 同样失败；macOS 的 Linux 专用 seatbelt 断言及缺少 flock/Unix socket 路径限制影响全量执行。CMD 与 deploy 用例已另在 Linux Docker 中通过；运行时整包的 Docker 回收/发现测试失败也已在未修改的 master 复现。
