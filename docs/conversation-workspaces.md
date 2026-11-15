# 对话工作区与项目沙箱

普通对话中的 Main Bot 保持本地对话进程，需要看代码、改代码、跑命令、构建或访问 GitHub 时才调用 `workbench_workspace_open` 进入工作区模式。工作区是一个**运行环境**，不是一次检出：`workbench_workspace_open` 只接收 `sandboxProfileId`（其他参数一律报错），工具描述列出本项目和共享的可用沙箱（Docker，或执行机器的 native 轻量沙箱）以及项目登记的仓库地址供 clone 参考。不选环境时沿用当前工作区，还没有工作区时用会话设置或平台 `generic-docker`；任何情况下都不使用无隔离的宿主机（`local`）。所选环境不可用（被删除、`local`、跨项目私有、Dockerfile 缺失）时返回可操作错误，由 Main Bot 改选，不会被静默替换。

工作区新开时是空目录，平台不预先检出任何仓库、不建分支。交接给 CLI 后，Agent 用自己的 git `clone`、`checkout`、建分支、`commit`、`push`，需要时 `merge`，并自己开 PR（`gh` 或 `curl` 调 API；镜像里没有 `gh` 时由 Agent 自行安装，或在沙箱配置里预装）。推送到主分支、强推、合并 PR 只在用户明确要求时做。

用户可在「高级设置 → 显示运行环境选择」中为会话指定环境。会话 `sandboxProfileId` 非空时，它优先于 Main Bot 的选择，工具描述也会告诉 Main Bot 不要再选。

项目会话在入口统一校验项目访问权限。所有具备访问权限的成员（包括 viewer）都拥有同一会话的完整操作权限，可以继续讨论、编辑用户消息、停止任意成员发起的回复、选择运行环境、使用工作区和浏览器工具，以及归档或删除普通项目会话。内部不再按会话创建者、本轮发起人或项目编辑角色二次限制。会话的存储归属保持稳定，工作区绑定和历史对所有参与者一致；模型调用、Git 推送与 PR 仍使用**当前这一轮发言人**的个人凭据。

## Git 凭据

工作区模式下的 CLI 进程（`runtime.BindGitWorkspace`）在每轮启动时按本轮发言人注入：

- HTTPS：个人配置的 Git Token。`GIT_ASKPASS` 指向工作区 `.agentflow/runtime/git-askpass-hosts`，脚本本身不含令牌，从提示里解析出 URL 主机，只在它恰好是 `https://github.com` 时以 `x-access-token` 应答；`github.com.example`、`github.com@example`、`http://` 等其他地址都拿不到凭据。带 `oauth2:`/`private:` 前缀的令牌不是 GitHub 令牌，不会注入。`GIT_TERMINAL_PROMPT=0`，不会卡在交互输入上。
- `GH_TOKEN`：同一个令牌，供 `gh` 或 `curl` 调 GitHub API。平台进程自身的 `GH_TOKEN`/`GITHUB_TOKEN` 不会透传进沙箱。
- SSH：个人 SSH 密钥按 `host:port` 绑定（不再按项目登记的仓库地址），任何克隆地址只要主机端口匹配就能用。私钥只在平台进程内存中的临时 ssh-agent 里，沙箱只看到 agent socket、公钥与 ssh config；主机公钥必须固定：平台内置 GitHub 公布的 `github.com:22` 与 `ssh.github.com:443` 公钥（来自 api.github.com/meta），其他主机由管理员通过 `AGENTFLOW_GIT_SSH_KNOWN_HOSTS` 固定，两者合并生效（文件里的 `@revoked` 同样作用于内置公钥）；未固定的主机报错说明原因。
- 提交身份：`GIT_CONFIG_SYSTEM` 设为发言人的登录名，邮箱 `<login>@users.noreply.loopforge.invalid`；Agent 可以用 `git config --global/--local` 覆盖。

工具结果和回复都会对发言人的令牌（含去掉前缀后的裸令牌）做脱敏；提示词要求 Agent 不打印、不回显、不写入令牌。

## 工作区的切换与生命周期

一个对话同一时间只绑定一个工作区（会话 `workspaceRunId`）。工作区按运行环境区分：不传 `sandboxProfileId` 或传的与当前工作区相同时继续用当前工作区；传了别的环境就切换过去——本对话之前在那个环境开过、仍可用的工作区（会话 `ownWorkspaceRunIds`）直接接着用，文件和已克隆的仓库都在，没有就新开一个空工作区。每轮上下文列出当前绑定的工作区、之前在其他环境开过的工作区，以及本条消息 @ 的需求和关联主需求各自有没有跑过 SOP、SOP 的工作分支；要接着 SOP 的改动，Agent 进入工作区后自己 clone 并 checkout 那个分支。

工作区只由 Main Bot 打开或切换：角色子 Agent 的 agent 文件用 `disallowedTools` 禁掉 `workbench_workspace_open`，网关在本轮还有 Agent 派发在跑时也会拒绝它。子 Agent 与 Main Bot 共用同一个已绑定的工作区；需要另一个环境时在汇报里说明，由 Main Bot 在派发结束后处理。

普通对话按轮次持有工作区租约：首次使用时取得，此后整轮工具调用复用并持续续租，不再逐条命令、文件操作或浏览器操作保存完整快照。正常流程只在两个时点保存：本轮结束（包括失败、取消、超时和异常收尾），或切换工作区前。切换与轮次结束会等待正在使用该工作区的工具完成；重新打开同一个工作区不触发保存。切换前保存失败（例如工作区超过快照上限或保存超时）不阻止切换：旧工作区照常解绑，文件原样保留，空闲回收在回收前会再次保存，保存成功之前不会回收；`workbench_workspace_open` 的结果用 `previousCheckpointError` 说明原因。轮次收尾时仍未解决的保存错误通过生成状态的 `workspaceWarning` 返回。续租遇到存储错误时在租约有效期内重试，只有租约已被他人取得或临近过期才中止本轮生成。部署的在途任务等待包含收尾保存；强杀或崩溃仍依赖保留的工作目录和既有空闲回收机制补偿。

切换完成后旧工作区解绑（`run_workspace_checkpoints.released_at`），空闲回收不再等一小时：下一次扫描保存兜底快照后回收，这次保存不在工具调用的返回路径上；切回或在 IDE 中再次使用会取消解绑。切回已回收的工作区时先恢复快照。SOP 仍在执行或暂停时不会被回收，它的生命周期仍由 SOP 自己决定；离开一个暂停中的 SOP 时，工具会提醒 Main Bot 它还没有继续。删除对话时，对话开过的所有工作区一并删除，借用的 SOP 工作区不受影响。

会话尚未关联需求时，消息里 @ 的需求（优先取跑过 SOP 的）自动成为关联主需求；在对话里派发需求（模型调用 `issues_dispatch`、自动草稿，或用户确认草稿，后者由聊天页通过 `X-AgentFlow-Chat-Session` 请求头带上会话）也会把该需求关联到会话。之后不 @ 需求的消息也会把它列给 Main Bot。SOP 总是准备自己的工作区，不再借用对话工作区。


已关联 SOP 的任务会话里，SOP 执行中工作区只读；Main Bot 用 `runs_pause` 或 `runs_cancel` 停下 SOP 后才进入工作区模式（`workbench_workspace_open` 返回 `handoff=false` 时会提示这一点）。SOP 的交付由它自己的交付节点完成。

对话工作区创建时冻结完整沙箱、Dockerfile 和环境定义，之后不再改变环境；换环境就是切到另一个工作区。准备工作区失败时，本次新建的工作区会被取消并解绑，会话回到之前的工作区，不会卡在一个打不开的工作区上。

## Native 沙箱

会话工作区可以使用 native 沙箱（Linux 上解析为 bwrap）。bwrap 工作区与 Docker 布局一致：CLI 的 HOME 是 `<会话根>/home`，cwd 是 `<会话根>/ws`（挂的是运行工作区，工作区同时也挂在它在宿主上的路径），审批钩子文件只读挂载；宿主文件系统其余部分只读，网络与宿主相同。CLI 使用执行机器自己安装的版本。macOS seatbelt 暂不支持对话工作区，会返回明确错误。bwrap 需要创建 user/pid 命名空间并挂载 `/proc`，平台运行在默认权限的容器里（Compose 部署）时做不到：平台镜像不带 bwrap，即使装上，不开特权容器也建不了命名空间。Compose 部署请用 Docker 沙箱；要用 native 沙箱，在原生安装的平台或执行机器（Runner）上运行。

两种模式的 CLI 都在 `<会话根>/ws` 运行（工作区模式下这里挂的是运行工作区），按 cwd 存放原生会话的 CLI（如 Claude Code、CodeBuddy）进出工作区、换环境都能续接。原生日志确实不可用时，平台在当前阶段用完整对话上下文新建原生会话继续（`cli.resume_fallback`），每个阶段最多一次，并告诉新会话当前所处的模式。

## 工作区里的 Docker

工作区直接使用执行机器的 Docker 守护进程，与在这台机器上开发的人一样：`DOCKER_HOST` 指向宿主 socket（默认 `/var/run/docker.sock`；Docker 工作区里挂在 `/var/run/docker.sock`），`docker build/run/compose` 直接可用（Docker 工作区的镜像要自带 docker CLI）。工作区创建的容器、镜像、网络和卷都是普通 Docker 对象，平台不打标签、不限制、不随工作区回收；之后任何会话的工作区都能列出、复用或清理它们。

- 路径：CLI 的 cwd `<会话根>/ws` 在宿主上是平台模式的目录，宿主守护进程看不到其中挂的工作区。所以运行工作区在 Docker 与 bwrap 环境里还挂在它在宿主上的绝对路径，PATH 最前面放一个只读的 `docker` 包装脚本（`<会话根>/workspace-bin/docker`）：它把参数里以 `<会话根>/ws` 开头的路径（如 `-v "$PWD:/src"`、`--mount src=…`）换成宿主路径，并在对应的宿主路径目录下执行真正的 docker，compose 里的相对路径因此也指向工作区本身。CLI 的命令工具用登录 shell 起环境，系统 profile（如 Debian 的 `/etc/profile`）会重置 PATH，所以平台还在会话 HOME 的 `~/.profile`（以及已存在的 `~/.bash_profile`、`~/.bash_login`、`~/.zprofile`）追加一行，把包装脚本目录放回 PATH 最前面；平台模式下没有对应环境变量，这一行不生效。这只是便利，不是隔离边界：绕过包装脚本直接调用 Docker API 的程序（如 Docker SDK）需要自己使用宿主路径，环境变量 `AGENTFLOW_WORKSPACE_HOST` 给出这个路径。容器化部署的平台按同样原则把数据目录挂在相同绝对路径（`compose.yaml` 已配置）。
- socket 来源：Docker 工作区容器的 socket 由宿主守护进程挂载，容器化的平台用 `AGENTFLOW_DOCKER_SOCKET_HOST` 告诉它宿主上的路径（`compose.yaml` 设为 `LOOPFORGE_DOCKER_SOCKET`）；原生安装两者相同。服务以非 root 用户运行时，Docker 工作区额外加入 socket 的属组。
- 权限：能访问 Docker socket 就相当于守护进程所在机器的 root。任何能在对话里打开工作区的成员（以及被仓库内容诱导的 Agent）都能借此读取平台容器、数据库和其他成员的凭据，只把账号开给可信的人。`AGENTFLOW_WORKSPACE_DOCKER=off` 关闭：Docker 工作区不再挂 socket，bwrap 工作区用 `/dev/null` 遮住宿主的 Docker/containerd/podman socket（按解析后的真实路径，`/var/run` 是指向 `/run` 的软链时也生效）。
- 升级：按旧布局（工作区只挂在 `<会话根>/ws`，或 socket 挂载与当前开关不一致）创建的 Docker 工作区容器在下次启动时重建，工作区文件在 bind 上不受影响。

`PUT /api/assistant/sessions/{id}/sandbox` 接受 `{ "sandboxProfileId": "id 或空字符串" }`，具备项目访问权限的会话参与者均可调用；生成期间和任务会话拒绝变更。已有工作区不受影响，新选择只用于之后新开的工作区。普通会话创建时也可以提交 `sandboxProfileId`，后续普通会话保存不能覆盖已保存的选择或伪造工作区绑定。

对话工作区使用独立的 `Run.kind=workspace` 和 `conversation-workspace` 标识，不参与 SOP 调度或占用 SOP 配额；继续复用 Runner、沙箱、Git、终端、快照、租约和空闲回收。API 与 Runner 需一起升级，旧 Runner 对新工作区请求会收到明确的版本要求。

旧群协作的邀请、角色派发、群 MCP、清单、自动跟进及切回 SOP 写入口退出，对话只有一种状态。手动 SQL 把会话 workspaceRunId 指向的历史临时工作区转为 workspace；其余未结束的群运行连同节点与 Runner 任务一并置为 cancelled，其群会话成为已结束任务的普通任务会话，历史消息与交接材料保留。已取消的群记录没有可续跑的 SOP 图，run 控制接口拒绝对它重试或恢复。

部署时先停旧 API/Worker，手动执行 `migrations/020_conversation_workspaces.sql`，然后启动新版 API/Worker/Runner。不删除历史数据，也无需为项目配置对话环境。生产启动不执行数据迁移或 seed。

## 需求关联与统计

要交给用户的文件（HTML demo、报告、JSON/CSV 等文本文件）用 `workbench_publish_artifact { path }` 发布：附在本轮回复里，HTML 在产物栏可交互预览。空间会话里不带 `path` 时照旧把 `body`/`imageBase64`/`url` 发布到空间侧边栏。原来的 `workbench_workspace_attach` 已并入这个工具，只在角色交付物收集里作为内部调用保留。

对话里直接改代码也要落到需求上。Agent 不需要调用登记工具：每轮回复保存前，平台列出工作区里两层目录内的 git 检出（远程执行机器上由 Runner 扫描，旧版 Runner 不支持时跳过），对其中指向 github.com 的远程仓库，用本轮发言人的 Git Token 查最近创建的 PR，满足以下全部条件的才登记：作者是该 Token 的用户、创建时间晚于工作区创建、头提交是工作区某个本地分支的当前提交、未关闭（或已合入），且还没有任何需求跟踪它。这样同一用户在别的会话或浏览器里开的 PR 不会被误登记；Agent 推送后又在本地追加了未推送的提交时，本轮不登记，下次推送后再登记。登记成功时回复末尾附一句登记结果。工作区仍未关联需求时，优先复用会话已关联的未完成需求，否则以第一个 PR 的标题和描述创建；借用已结束 SOP 工作区时登记到该 SOP 的需求。新建需求为本地 `requirement`、`in_progress`、`automationState=ignored`（不会被自动派发 SOP），负责人为当前用户，记录来源会话、工作区仓库和创建操作，并同时关联工作区与会话。已关联需求的工作区不再改关联；SOP 运行的需求只在派发时确定。

登记的 PR（记为 `github:owner/repo`）写入该需求的交付 MR 并登记合入跟踪，链接能对上项目登记的仓库时同时记录仓库，需求转为 `in_review`；PR 合入默认分支后由轮询把需求标为完成。每轮对话的模型用量按回复模型定价累加到当前对话工作区。

数据统计中，对话工作区不是 Run：不计入运行数、成功率、耗时和「平均 Token / Run」，但其 Token 与成本计入总量、趋势、成员、项目和来源，并在 `summary.conversation*` 与趋势点 `conversationTokens/conversationCost*` 中单列。只经对话改代码的需求（`directDelivery`）在交付明细中按 MR 状态显示「对话改代码中 / 对话 · MR 待合入 / 对话 · MR 已关闭」，MR 合入默认分支后算已交付并显示「对话直接交付」；同一需求有 SOP 运行时仍按 SOP 口径。对话工作区不出现在「运行记录」列表、运行概览和全局搜索中，也不计入 SOP 页「进行中 / 待审批」：它没有节点，且在回收前一直是 waiting，列出来只会变成看不懂的「已回收」记录。关联了需求的对话工作区仍在需求列表「关联 SOP」中显示为「对话工作区」。入口是对话本身（侧栏工作区）和关联需求；从对话直接打开 `#/runs/{id}` 仍可查看工作区详情。

## 界面与样式

环境选择默认隐藏，普通用户由 Main Bot 自动选择环境。技术人员通过「＋ → 高级设置 → 显示运行环境选择」开启；显示偏好按用户保存在当前浏览器，隐藏入口不改变已选环境或已有工作区。开启后，环境选择与项目选择并排，复用同一个 `SelectMenu`，使用相同的搜索、选中态和弹出方向规则。菜单底部的「新增沙箱」打开当前项目的创建页，并默认选择 Docker；返回工作台后重新读取环境目录。普通对话随时可以改选环境，只影响之后新开的工作区；任务会话只展示任务环境。

旧协作样式中的全站主题、顶栏和页面框架规则归入 `web/app/frame.css`；仍由 SOP、角色等页面使用的模型控件样式归入 `web/assistant-models.css`。移除旧 UI 不改变其他页面的背景、搜索框或导航。

## 本地验证

- 原生对话前端 178 项测试通过；后端 HTTP、Engine、Runner、Repo、MCP 与 Web/OpenAPI 契约回归通过。
- 独立 Docker MySQL 验证了会话环境与生成启动的并发事务、SOP 配额，以及迁移脚本的作用范围与幂等性。
- 真实 CodeBuddy 对话分别选择项目默认和会话覆盖 Docker 环境，命令读取到 `business-default` 与 `conversation-override`，文件写入与验证命令均返回 0。新增沙箱入口也已通过实际界面创建验证。
- 工作区沙箱的实机用例默认跳过，在能创建沙箱的执行机器上手动打开：
  - `LOOPFORGE_LIVE_BWRAP=1 go test ./internal/runtime -run TestLiveBwrapConversationWorkspace`：真实 bwrap 下 cwd `<会话根>/ws` 是运行工作区且可写、宿主路径可见同一份文件、home 可写、守卫文件与根目录只读、宿主 Docker socket 可见。
  - `LOOPFORGE_LIVE_DOCKER=1 go test ./internal/runtime -run TestLiveDockerDriverConversationWorkspace`：Docker 驱动下同样的布局检查，并在容器里从 `<会话根>/ws` 经 docker 包装脚本 `docker run -v "$PWD:/m"` 读回工作区文件（需要能拉 alpine）。
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
