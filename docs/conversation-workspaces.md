# 对话工作区与项目沙箱

普通对话中的 Main Bot 保持本地对话进程，首次需要修改仓库或运行命令时才调用 `workbench_workspace_open` 创建 Docker 工作区。项目不再配置默认对话沙箱：工具描述列出本项目和共享的 Docker 沙箱及项目仓库，由 Main Bot 按要改的代码选择 `sandboxProfileId` 和 `repositories`。不选环境时使用平台 `generic-docker`，不选仓库时检出项目全部仓库（默认仓库在前）；任何情况下都不使用宿主机。所选环境不可用（被删除、非 Docker、跨项目私有）时返回可操作错误，由 Main Bot 改选，不会被静默替换。

用户可在「高级设置 → 显示运行环境选择」中为会话指定环境。会话 `sandboxProfileId` 非空时，它优先于 Main Bot 的选择，工具描述也会告诉 Main Bot 不要再选。仓库不单独提供用户配置：Main Bot 按需求选择，拿不准时全部检出。

项目会话在入口统一校验项目访问权限。所有具备访问权限的成员（包括 viewer）都拥有同一会话的完整操作权限，可以继续讨论、编辑用户消息、停止任意成员发起的回复、选择运行环境、使用工作区和浏览器工具，以及归档或删除普通项目会话。内部不再按会话创建者、本轮发起人或项目编辑角色二次限制。会话的存储归属保持稳定，工作区绑定和历史对所有参与者一致；模型调用、Git 推送与 MR 仍使用当前操作人的个人凭据。

一个对话同一时间只绑定一个工作区（会话 `workspaceRunId`），用哪个由 Main Bot 按用户当前实际要处理的需求决定，@ 只是线索。`workbench_workspace_open` 的 `issueId`、`workspaceRunId`、`new` 最多传一个：都不传继续用当前绑定的工作区，还没有就新开；`issueId` 切到该需求最近一次顶层 SOP 运行的工作区，不论它在执行、暂停、成功、失败还是取消，需求必须跑过 SOP；`workspaceRunId` 切回本对话之前开过的工作区（会话 `ownWorkspaceRunIds`）；`new` 新开一个对话工作区。没跑过 SOP 的新需求是否沿用当前工作区也由 Main Bot 判断：仓库和环境合适就沿用，不合适就新开。每轮上下文列出当前绑定的工作区、之前开过的工作区，以及本条消息 @ 的需求和关联主需求各自有没有跑过 SOP。

工作区只由 Main Bot 打开或切换：角色子 Agent 的 agent 文件用 `disallowedTools` 禁掉 `workbench_workspace_open`，网关在本轮还有 Agent 派发在跑时也会拒绝它。子 Agent 与 Main Bot 共用同一个网关和同一个已绑定的工作区，直接在其中读写、跑命令；需要另一个工作区时在汇报里说明，由 Main Bot 在派发结束后处理。

普通对话按轮次持有工作区租约：首次使用时取得，此后整轮工具调用复用并持续续租，不再逐条命令、文件操作或浏览器操作保存完整快照。正常流程只在两个时点保存：本轮结束（包括失败、取消、超时和异常收尾），或切换工作区前。切换与轮次结束会等待正在使用该工作区的工具完成；重新打开同一个工作区不触发保存。切换前保存失败（例如工作区超过快照上限或保存超时）不阻止切换：旧工作区照常解绑，文件原样保留，空闲回收在回收前会再次保存，保存成功之前不会回收；`workbench_workspace_open` 的结果用 `previousCheckpointError` 说明原因。轮次收尾时仍未解决的保存错误通过生成状态的 `workspaceWarning` 返回。续租遇到存储错误时在租约有效期内重试，只有租约已被他人取得或临近过期才中止本轮生成。部署的在途任务等待包含收尾保存；强杀或崩溃仍依赖保留的工作目录和既有空闲回收机制补偿。

切换完成后旧工作区解绑（`run_workspace_checkpoints.released_at`），空闲回收不再等一小时：下一次扫描保存兜底快照后回收，这次保存不在工具调用的返回路径上；切回或在 IDE 中再次使用会取消解绑。切回已回收的工作区时先恢复快照。SOP 仍在执行或暂停时不会被回收，它的生命周期仍由 SOP 自己决定；离开一个暂停中的 SOP 时，工具会提醒 Main Bot 它还没有继续。删除对话时，对话开过的所有工作区一并删除，借用的 SOP 工作区不受影响。

会话尚未关联需求时，消息里 @ 的需求（优先取跑过 SOP 的）自动成为关联主需求；在对话里派发需求（模型调用 `issues_dispatch`、自动草稿，或用户确认草稿，后者由聊天页通过 `X-AgentFlow-Chat-Session` 请求头带上会话）也会把该需求关联到会话。之后不 @ 需求的消息也会把它列给 Main Bot。SOP 总是准备自己的工作区，不再借用对话工作区。

使用 SOP 工作区时读写规则与任务工作区相同：执行中只读，暂停或结束后可写。Main Bot 通过 `workbench_workspace_sop` 直接控制这次 SOP，不经确认草稿（帮我批准模式仍经独立审查），请求按用户本人的权限走运行控制端点：`pause` 在当前节点停下供 Main Bot 改代码；`resume` 带着 Main Bot 的改动说明继续 SOP，由交付节点交付；`takeover` 取消执行中的 SOP，之后直接在它的工作区改。SOP 结束后（含成功交付），`workbench_workspace_submit_mr` 推送到它的工作分支并发起或更新 MR；暂停期间不能提交，以免与之后恢复的交付节点冲突。

对话工作区创建时冻结完整沙箱、Dockerfile、环境定义和检出的仓库，之后不再改变环境或增加仓库；返回中的 `missingRepositories` 或环境不符的提示让 Main Bot 用 `new` 新开一个工作区。`repositories` 不填检出项目全部仓库；显式传空数组 `[]` 开一个不检出任何仓库的空工作区，只跑命令、起服务或开浏览器，项目没配仓库时也能用，它没有可提交的内容。每个仓库在各自目录下新建工作分支，`workbench_workspace_submit_mr` 只推送和发起相对基线分支有改动的仓库，没有任何改动时返回错误。

新开时每个仓库的分支由三件事决定：模式、分支名和起点。

- 模式 `branchMode`：`auto`（默认）由平台按需求编号或时间戳自动命名新分支；`continue` 继续指定的已有远端分支，以 `RequireExistingWorkBranch` 检出，不新建、不改名；`create` 按调用方给的名字新建分支，以 `CreateNewWorkBranch` 在检出时用 `ls-remote` 确认远端没有同名分支，已存在则报错并提示改用 `continue`，绝不静默继续（跑在 runner 上时要求 runner ≥ 0.9.1）；`readonly` 只读检出指定分支（可以是 preview、master 这类共享分支），仓库以 `ReadOnly` 记录、`BaseBranch` 即该分支、没有工作分支，能读、构建、跑测试、复现问题，`submit_mr` 不会提交它，工作区里全是只读仓库时提交直接报错。
- 起点 `baseBranch`：工作分支从它拉出，它同时是 `submit_mr` 的默认 MR 目标和「有没有改动」的比较基线；不填用仓库配置的主分支。MR 的内容相对起点计算，所以 hotfix 打 release、堆叠 MR 打父分支时，开工作区就要把 `baseBranch` 设成目标分支；`submit_mr` 显式传的 `targetBranch` 与起点不同时照常发起，但结果带 `warning` 说明 MR 会混入两个分支之间的差异提交。工作分支不能等于起点、主分支、集成分支或 `master`/`main`。
- 分支名：单仓库用 `branch`；多仓库用 `repositoryBranches`（按仓库名或 id）。值写字符串时使用顶层 `branchMode`/`baseBranch`；写对象 `{branch, branchMode, baseBranch}` 时该仓库用自己的模式和起点，所以一次调用就能「A 仓库 continue 别人的分支做评审、B 仓库 create 一个命名分支」。只给 `baseBranch` 不给 `branch` 的对象按 `auto` 从该起点新建；未列出的仓库按 `auto` 从顶层 `baseBranch`（或主分支）新建，不会沿用其他仓库显式 continue/create 的分支名。

省略 `branchMode` 但传了分支时按 `continue` 处理，兼容老的调用方；`auto` 与分支名同时出现、`continue`/`create`/`readonly` 缺分支名、`readonly` 另给 `baseBranch`（包括顶层 `baseBranch` 没有任何非只读仓库用得上）、空工作区带分支参数，都会在参数校验阶段报错。起点或只读分支在远端不存在时，报错会点名是哪个仓库的哪个分支。`create` 的名字若是本对话之前开过、仍可切回的工作区用过的分支，直接拒绝并给出 `workspaceRunId`：那个工作区里可能有没推送的提交，重新从起点新建会静默丢掉它们。分支选择只在创建时生效，复用已有工作区时传入会被拒绝；`continue` 的分支不存在或不可读时按 runner 的既有规则直接报错。准备代码失败时，本次新建的工作区会被取消并解绑，会话回到之前的工作区，不会卡在一个打不开的工作区上。

工作分支以工作区记录的 `workBranch` 为准。`submit_mr` 推送前检查每个有改动的仓库确实检出在这个分支上、没有进行中的 merge/rebase/cherry-pick；在终端里手工 `git checkout` 到别的分支后提交会被拒绝并说明当前分支，而不是把记录的分支报告为已推送（本地执行与 runner ≥ 0.9.2 生效，更旧的 runner 仍按旧逻辑跳过）。要换分支就 `new` 新开工作区。

`workbench_workspace_submit_mr` 默认推送工作分支并发起或更新 MR；传 `mode=direct` 时不发 MR，而是把工作分支 `merge --no-ff` 进 `targetBranch` 后直推，用于 preview/test 这类共享分支的合并流程。直推要求目标分支已在远端存在、且不是可写仓库的基线分支（只读仓库不参与这项检查）；合并在临时 worktree 里完成，冲突时中止且不推送，运行的工作区始终留在自己的工作分支上；推送前用 merge-base 预检加 lease，别人先推过就拒绝覆盖。直推不产生 MR，登记的需求需要人工确认收尾。

`PUT /api/assistant/sessions/{id}/sandbox` 接受 `{ "sandboxProfileId": "id 或空字符串" }`，具备项目访问权限的会话参与者均可调用；生成期间和任务会话拒绝变更。已有工作区不受影响，新选择只用于之后新开的工作区。普通会话创建时也可以提交 `sandboxProfileId`，后续普通会话保存不能覆盖已保存的选择或伪造工作区绑定。

对话工作区使用独立的 `Run.kind=workspace` 和 `conversation-workspace` 标识，不参与 SOP 调度或占用 SOP 配额；继续复用 Runner、沙箱、Git、终端、快照、租约和空闲回收。API 与 Runner 需一起升级，旧 Runner 对新工作区请求会收到明确的版本要求。

旧群协作的邀请、角色派发、群 MCP、清单、自动跟进及切回 SOP 写入口退出，对话只有一种状态。手动 SQL 把会话 workspaceRunId 指向的历史临时工作区转为 workspace；其余未结束的群运行连同节点与 Runner 任务一并置为 cancelled，其群会话成为已结束任务的普通任务会话，历史消息与交接材料保留。已取消的群记录没有可续跑的 SOP 图，run 控制接口拒绝对它重试或恢复。

部署时先停旧 API/Worker，手动执行 `migrations/020_conversation_workspaces.sql`，然后启动新版 API/Worker/Runner。不删除历史数据，也无需为项目配置对话环境。生产启动不执行数据迁移或 seed。

## 需求关联与统计

对话里直接改代码也要落到需求上。Main Bot 识别出明确需求时，在 `workbench_workspace_open` 的 `requirement { title, description }` 中提交；`workbench_workspace_submit_mr` 提交 MR 时若工作区仍未关联需求，优先复用会话已关联的未完成需求，否则以 MR 标题和说明创建。新建需求为本地 `requirement`、`in_progress`、`automationState=ignored`（不会被自动派发 SOP），负责人为当前用户，记录来源会话、工作区仓库和创建操作，并同时关联工作区与会话。已关联需求的工作区不再改关联；SOP 运行的需求只在派发时确定。

提交的 MR 写入该需求的交付 MR 并登记合入跟踪，需求转为 `in_review`；MR 合入默认分支后由 MR 轮询把需求标为完成。每轮对话的模型用量按回复模型定价累加到当前对话工作区。

数据统计中，对话工作区不是 Run：不计入运行数、成功率、耗时和「平均 Token / Run」，但其 Token 与成本计入总量、趋势、成员、项目和来源，并在 `summary.conversation*` 与趋势点 `conversationTokens/conversationCost*` 中单列。只经对话改代码的需求（`directDelivery`）在交付明细中按 MR 状态显示「对话改代码中 / 对话 · MR 待合入 / 对话 · MR 已关闭」，MR 合入默认分支后算已交付并显示「对话直接交付」；同一需求有 SOP 运行时仍按 SOP 口径。对话工作区不出现在「运行记录」列表、运行概览和全局搜索中，也不计入 SOP 页「进行中 / 待审批」：它没有节点，且在回收前一直是 waiting，列出来只会变成看不懂的「已回收」记录。关联了需求的对话工作区仍在需求列表「关联 SOP」中显示为「对话工作区」。入口是对话本身（侧栏工作区）和关联需求；从对话直接打开 `#/runs/{id}` 仍可查看工作区详情。

## 界面与样式

环境选择默认隐藏，普通用户由 Main Bot 自动选择环境。技术人员通过「＋ → 高级设置 → 显示运行环境选择」开启；显示偏好按用户保存在当前浏览器，隐藏入口不改变已选环境或已有工作区。开启后，环境选择与项目选择并排，复用同一个 `SelectMenu`，使用相同的搜索、选中态和弹出方向规则。菜单底部的「新增沙箱」打开当前项目的创建页，并默认选择 Docker；返回工作台后重新读取环境目录。普通对话随时可以改选环境，只影响之后新开的工作区；任务会话只展示任务环境。

旧协作样式中的全站主题、顶栏和页面框架规则归入 `web/app/frame.css`；仍由 SOP、角色等页面使用的模型控件样式归入 `web/assistant-models.css`。移除旧 UI 不改变其他页面的背景、搜索框或导航。

## 本地验证

- 原生对话前端 178 项测试通过；后端 HTTP、Engine、Runner、Repo、MCP 与 Web/OpenAPI 契约回归通过。
- 独立 Docker MySQL 验证了会话环境与生成启动的并发事务、SOP 配额，以及迁移脚本的作用范围与幂等性。
- 真实 CodeBuddy 对话分别选择项目默认和会话覆盖 Docker 环境，命令读取到 `business-default` 与 `conversation-override`，文件写入与验证命令均返回 0。新增沙箱入口也已通过实际界面创建验证。
- 全量测试尚有独立于本次修改的既有问题：MySQL 分析统计覆盖用例在未修改的 master 同样失败；macOS 的 Linux 专用 seatbelt 断言及缺少 flock/Unix socket 路径限制影响全量执行。CMD 与 deploy 用例已另在 Linux Docker 中通过；运行时整包的 Docker 回收/发现测试失败也已在未修改的 master 复现。

### 会话与协作 Token 统计

「统计 → 用量 → Vibe Coding 与协作」展示会话 Token 和协作会话 Token。数据来自普通会话中已经保存的助手回复，不要求创建工作区；SOP 讨论会话不计入。协作沿用当前会话中至少两位用户实际发言的口径，协作 Token 是会话 Token 的子集。

Token 按回复时间归入日、周、月，独立于会话数的创建时间。没有时间的旧回复只计入累计用量。总量沿用 `usage.totalTokens`，未提供总量时按输入 + 输出计算，缓存读写分别展示；缺少 `usage` 的回复标为未采集，不视为零消耗。会话分支复制的历史前缀不重复计费。

个人范围显示本人创建或参与的会话整体用量，不代表个人账单。此会话用量视图与已有工作区用量可能重叠，不再加入 Run 总投入。统计基于当前保留的回复，已删除或改写的历史、未保存的失败生成不推算用量。

## 需求列表中的对话需求

只在对话工作区里做、没有派发过 SOP 的需求（没有 Task，关联运行全是对话工作区；工作区记录已不在时以需求的 `conversationSessionId` 为准）不按 SOP 节点算状态，而按交付推进：未提 MR 为「对话开发中」，MR 未合入为「MR 待合入」，直推共享分支（无 MR、`in_review`）为「已直推 · 待验收」，MR 全部关闭为「MR 已关闭」，合入后与 SOP 需求一致。进度列显示阶段文字而不是节点百分比；工作区被取消（会话删除后）不算失败。「查看」打开需求详情的「执行进展」，展示对话交付：交付 MR 与状态、工作区检出的仓库与工作分支、「打开会话」。「打开会话」对有权限的项目成员都显示（非创建者以只读打开），会话删除后显示「对话已删除」；是否已删除由 `GET /api/issues/{id}/requirement` 的 `conversation { sessionId, available, branches }` 给出。需求详情不提供进入对话工作区运行、日志或 SOP 操作的入口。

## 删除会话

删除会话（手动删除、定时会话过期清理、删除定时任务或新一次定时运行替换旧会话）都经 `assistantHandlers.deleteConversation`，按它打开过的工作区和关联的需求逐个处理（一个会话可以做多个需求）：

- 已交付的需求（登记过 MR，无论开启、合入还是关闭；直推过共享分支而处于 `in_review`；或已完成）保留。它的对话工作区只结束（取消并回收沙箱），记录保留用于统计对话用量与交付；它不出现在任何列表，需求详情也不链到它。
- 未交付的需求：对话工作区照旧取消并删除；若需求由本会话创建（`conversationSessionId`）且从未派发过 SOP（没有 Task，关联运行全是对话工作区），需求一并删除。
- TAPD、需求列表新建或派发过 SOP 的需求不会被删除；仍有任务在执行、已暂停或排队的工作区留给运行页处理。
