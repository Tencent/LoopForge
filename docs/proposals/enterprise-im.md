# LoopForge 企业 IM 接入实施方案（单分支开发）

版本为第 3 版，日期为 2026-11-18。状态为实施中：阶段 A–E（R1 飞书私聊闭环）的代码与自动化测试已完成，R1 已于 2026-11-18 验收通过（真实环境通过 R1-01/02/05，其余经验收负责人豁免）。阶段 F（R2 钉钉私聊闭环）已于 2026-11-19 验收通过，见 `docs/testing/im-r2-acceptance.md`。阶段 G 已于 2026-11-20 验收通过，见 `docs/testing/im-stage-g-acceptance.md`；实现与本文的差异见第 9.3 节。阶段 H（数据保留、安装配置、迁移与回退手册）已完成，最终测试记录见 `docs/testing/im-final-test-record.md`；实现与本文的差异见第 10.1 节。验收提交、预发演练、故障测试和逐项结果见 `docs/testing/im-r1-acceptance.md`。第 3 版补齐数据库时间、配置物理列、锁顺序、重试终态、停用语义、状态枚举、企业确认和维护窗口的实现约定，并按 `origin/main@e89cda9` 重新核对源码和迁移编号。本文是 `feature/enterprise-im` 分支的执行清单。

目标是在 LoopForge 已有的企微 Bot、账号绑定、交互队列和通知服务之上，扩展飞书和钉钉。首轮先完成三个渠道的账号绑定、私聊查询和可靠回复，再接入项目助手与 SOP 主动通知。

**基线。** 开发分支 `feature/enterprise-im` 于 2026-11-18 从 `origin/main@e89cda9804643d3631e77652d1bca47a079adb69` 建立。第 2 版依据的是 `69e4bf0`，两者之间共 20 个提交，其中第 2.1 节列出的 Bot、通知、身份、助手入口文件都没有改动。迁移目录的最大编号已从 033 变为 035，本方案的新迁移脚本改用 036、037。

## 1. 交付范围与默认行为

| 范围 | 本分支交付 |
| --- | --- |
| 企微兼容 | 已有 Bot、绑定、会话、通知与卡片继续可用；补齐发送状态和连接易主处理 |
| 飞书 | 企业自建应用，使用官方 Go SDK 长连接；支持私聊文本、账号绑定、只读项目查询、回复原消息 |
| 钉钉 | 企业内部应用，接入 Stream 协议（SDK 使用范围见第 8.3 节）；支持私聊文本、账号绑定、只读项目查询、通过会话 Webhook 回复 |
| 助手 | 使用绑定用户的个人模型凭据和项目权限；新增渠道首轮只执行只读任务 |
| 主动通知 | 向已验证的目标发送 `succeeded`、`failed`、`waiting` 文本通知，复用现有 SOP 通知任务 |
| 管理与运维 | Bot 配置、凭据轮换、连接状态、交互与投递记录、人工核对或补发、数据保留 |
| 后续扩展 | 飞书、钉钉的群聊、交互卡片、附件和写操作，按渠道单独验收后开放 |

首轮产品行为采用以下默认值。这些是本方案的设计选择，实施评审时可以调整。

- **写操作默认只读。** 新建 Bot（**包括新建的企微 Bot**）默认 `writeApproval=readonly`。企微历史记录缺少该字段时，继续映射现有的自动执行策略。新渠道收到写请求时，引导用户到项目页面操作。新建企微 Bot 从“自动执行”改为“只读”属于管理员能感知到的变化，写入上线说明和 `CHANGELOG.md`，见第 12 节。
- **配置归属与执行权限。** 一个 Bot 配置固定归属一个项目。绑定用户的执行权限来自其平台账号和当前项目。即使 Bot 由平台管理员创建，普通成员发来的消息仍按该成员的权限执行。
- **聊天范围。** 飞书、钉钉首轮只处理私聊文本，群消息计数后直接结束处理。企微已有的群聊和卡片按原协议继续工作。
- **接收配置唯一。** 同一 LoopForge 部署中，一个企业应用只能启用一个接收配置。不同环境需要配置各自独立的机器人应用。租约在同一部署的数据库范围内协调。
- **未绑定用户。** 未绑定的用户先收到网页绑定链接，在网页上确认现有 LoopForge 账号，再由原 IM 发送者提交 `/bind <code>`。绑定成功后，用户需要重新发送业务请求。
- **回复形式。** 企微保留现有的流式进度。飞书、钉钉首轮只返回最终文本；处理时间较长时，另发一条接单提示，提示有独立的记录和投递 key。
- **网页登录。** 网页绑定使用 LoopForge 现有的登录能力。企业 SSO 可以作为已有登录入口使用。

本分支按第 11 节的 IM-01 至 IM-12 验收。三个渠道的真实联调分别记录证据。

## 2. 现有代码与改动落点

### 2.1 已核对的实现

| 现有位置 | 当前行为 | 本次改动 |
| --- | --- | --- |
| `internal/domain/notification.go` | `NotificationBot` 保存外部 Bot ID、项目、启停、模型和通知目标 | 增加渠道、企业范围、配置版本和写策略 |
| `internal/botinteraction/service.go` | 直接维护企微连接、入队、模型处理与回复 | 阶段 A 已改为通过渠道接口编排；阶段 C 增加租约版本与发送恢复 |
| `internal/botinteraction/socket.go` | 企微发送和回执等待；原先错误以字符串返回 | 阶段 A 已改为记录是否开始写出、回执状态，转换成明确的发送结果 |
| `internal/botinteraction/progress.go`、`cards.go` | 企微流式进度与交互卡片 | 阶段 A 已包装为企微适配器的可选能力 |
| `internal/notification/service.go`、`provider.go` | 用官方 `wecom-cli` 查询会话和发送通知，复用现有通知队列 | 按渠道分流；飞书、钉钉新增独立于接收连接的主动发送 |
| `internal/repo/notifications.go` | `NotificationsRepo` 以 JSON payload 保存配置，保存时逐行扫描以防重复启用；投递已有 `lease_token` | 补物理列、配置版本、投递 key 和生命周期事务 |
| `internal/repo/bot_credentials.go` | `UsersRepo` 按**配置 ID** 把传入的 secret 原样写入名为 `ciphertext` 的列 | 引入带版本的凭据加密和密钥轮换 |
| `internal/repo/bot_identities.go` | 两阶段绑定、nonce 摘要、15 分钟有效期、解绑 | 增加配置归属、渠道显示、原子限频和撤销联动 |
| `internal/repo/bot_interactions.go` | 队列、会话、45 秒连接租约、10 秒续约、结果保存 | 增加 epoch、条件更新、发送阶段和旧 owner 保护 |
| `internal/httpapi/bot_interactions.go` | `botExecutionUser` 校验绑定与项目权限，`generateBotReply` 接入现有助手 | 按渠道取身份范围，执行前复核配置，传入只读策略 |
| `internal/httpapi/bot_identities.go` | 已登录网页确认，原发送者回传确认码 | 扩展元数据，按配置校验，多渠道文案 |
| `web/app/bots.js`、`web/bot-bind.js` | Bot 管理、发送记录、绑定确认页面 | 沿用原页面，增加渠道、状态、确认信息和补发操作 |
| `docs/api/openapi.yaml` | 已有 Bot、交互和绑定 API | 同步接口字段、错误码和新增操作 |

以下事实已对照 `origin/main@e89cda9` 逐条核对。 [交互 Repo](../../internal/repo/bot_interactions.go) · [凭据 Repo](../../internal/repo/bot_credentials.go) · [Bot 对话入口](../../internal/httpapi/bot_interactions.go)

| 核对项 | 源码证据与修订落点 |
| --- | --- |
| 结果写回 | `SaveBotReply` 只按交互 ID 和 `running` 状态更新，不检查 `RowsAffected`。阶段 C 增加归属条件，并处理更新 0 行的冲突 |
| 发送完成 | `MarkBotReplySent` 直接把 `ready` 改为 `replied`。阶段 C 增加 `sending` 状态和明确的结果迁移 |
| 凭据存储 | `putBotCredential` 原样写入传入的 secret，`BotCredential` 读取时也原样返回；行主键是配置 ID。阶段 B 增加加密封装 |
| 企微备用发送 | `service.process` 在 `ws.stream` 失败后直接调用 `ws.send`。阶段 A 保留这一行为；阶段 C 改为按送达证据分类，用户能感知到的变化由 IM-08 单独验收 |
| CLI 原生会话 | `generateBotReply` 每轮生成 `cliSessionID`，调用 `runAssistantCLI` 时丢弃了返回的 session ID。阶段 B 建表，并接入写入点 |
| 通知卡片 ID | 用户消息的交互 ID 由 `BotMessageID(bot.BotID, msgid)` 派生，即外部 ID；而通知卡片和详情交互由 `BotMessageID(bot.ID, "notification:"…)` 派生，即配置 ID。两者都保持不变；`IdentityScope()` 只用于平台消息的去重 |
| 防重复启用 | `SaveNotificationBot` 先用 `lockBotDefinition` 在 `bot_connections` 中按外部 BotID 建行并加锁，再扫描全部配置的 payload。阶段 B 改为 `receive_slot` 唯一列；企微的连接行键保持不变 |
| 通知状态 | `notification_deliveries.status` 现有取值：`pending`、`sending`、`sent`、`failed`、`cancelled`、`expired`、`awaiting_authorization`、`target_unavailable`。企微的 `waiting`/`failed` 卡片通知进入交互队列后即记为 `sent`，实际送达状态在对应的交互记录里 |
| 停用处理 | 通知 worker 领取任务时，若发现 Bot 已停用，把任务记为 `cancelled`。对话 worker 由 `reconcile` 停止连接，但交互记录的状态不变 |
| 迁移编号 | `origin/main@e89cda9` 已有 `034_schedule_job_executions.sql`、`035_codebuddy_model_catalog.sql`。本方案使用 036、037；合并前如果目标分支又新增了迁移，再顺延，并在 PR 中说明 |

### 2.2 结构约定

遵守根目录 `AGENTS.md` 的约定：

- **Repo 归属。** 通知归 `NotificationsRepo`，凭据和绑定归 `UsersRepo`，交互与连接归 `BotInteractionsRepo`，草稿和原生会话映射归 `AssistantRepo`。
- **方法与协作。** 新增的领域方法写在对应 Repo 上，跨领域通过现有的显式注入协作。共享的 `deps` 只保留 gorm 句柄。
- **handler。** 按领域归属。
- **前端。** 沿用 `design.md` 中 Bot 管理的布局、状态徽标和现有设计令牌。

[仓库约定](../../AGENTS.md) · [前端约定](../../design.md)

新增或调整的文件如下。“已新增”表示阶段 A 已提交。

```text
internal/botinteraction/
  channel.go                 渠道接口与事件、回复、发送结果类型（阶段 A 已新增）
  channel_wecom.go           包装现有 socket / progress / cards（阶段 A 已新增）
  channel_feishu.go          飞书适配器（阶段 E 已新增）
  channel_dingtalk.go        钉钉适配器（阶段 F）
  inbound.go                 公共入站校验、分类和持久化（阶段 A 已新增）
  delivery.go                回复状态机和恢复（阶段 C 已新增）
  retention.go               数据保留（阶段 H）
  service.go                 编排连接、领取和处理
internal/credcrypt/
  keyring.go                 AES-GCM 凭据加解密（阶段 B 已新增）
internal/repo/
  dbtime.go                  数据库时间方言函数（阶段 B 已新增）
  notifications.go           Bot 配置与通知投递
  bot_credentials.go         凭据存储
  bot_identities.go          身份绑定
  bot_interactions.go        连接、交互和会话
  bot_native_sessions.go     Bot 与原生 CLI 会话映射，归 AssistantRepo（阶段 B 已新增）
internal/httpapi/
  notifications.go           Bot 管理
  bot_identities.go          绑定确认
  bot_interactions.go        助手入口
  bot_interaction_records.go 对话记录和人工操作
web/app/bots.js
web/bot-bind.js
migrations/
  036_enterprise_im_channels.sql
  037_bot_binding_ownership.sql
scripts/im-sdk-probe/        SDK 固定版本探针，独立 Go 模块（阶段 A 已新增）
docs/
  im-channels.md             配置与联调手册
  api/im.md                  IM 管理和使用接口
  api/openapi.yaml
```

## 3. 单分支开发方式

全部阶段都在 `feature/enterprise-im` 分支上完成，最终合成一个 PR。每个阶段保留 1–3 个可以独立审阅的提交。开发目录使用独立的 worktree。

```bash
git fetch origin main
git worktree add -b feature/enterprise-im ../LoopForge-im origin/main
cd ../LoopForge-im
git rev-parse HEAD   # e89cda9804643d3631e77652d1bca47a079adb69
```

`AGENTFLOW_*` 环境变量和 `cmd/agentflow` 名称沿用项目现有约定。

| 阶段 | 主要提交主题 | 阶段完成后的可验证结果 |
| --- | --- | --- |
| A | `refactor(im): define channel contracts and wrap wecom` | 企微通过渠道接口工作，存量回归通过；模拟渠道覆盖接收、回执和断线；SDK 固定版本完成探针验证 |
| B | `feat(im): scope bot configs and encrypt credentials` | 渠道配置、物理列、数据库时间函数、身份范围、加密、CLI 会话映射写入、手动迁移可用 |
| C | `fix(im): fence workers and persist delivery outcomes` | 换主保护、发送恢复、停用和删除语义、人工补发和记录页可用 |
| D | `feat(im): bind channel identities and enforce read-only policy` | 两阶段绑定与只读业务链路可用 |
| E | `feat(im): connect feishu private chats` | 飞书真实私聊完成企业确认、绑定、查询和回复 |
| F | `feat(im): connect dingtalk stream chats` | 钉钉真实私聊通过同一套验收，并完成回复过期验证 |
| G | `feat(im): deliver sop notifications across channels` | 三个渠道发出主动通知；企微可选网页确认写操作；钉钉过期回复可改用主动单聊补发 |
| H | `docs(im): record rollout and acceptance evidence` | 保留策略、部署说明、真实验收和最终 PR 齐全 |

阶段检查只使用已经实现的能力。尚未接入的渠道在 UI 中显示为开发中，API 返回明确的 `channel_not_ready`。飞书、钉钉的业务消息走同一个入口，所以阶段 D 的只读约束必须在阶段 E 之前完成。

## 4. 渠道接口与运行规则

### 4.1 接收、回复与主动发送

渠道适配器放在 `internal/botinteraction` 包内，这样企微适配器可以直接包装已有的未导出 socket 方法。公共编排只处理领域事件和发送结果。下面的签名已在阶段 A 落地（`channel.go`）。

```go
type Channel interface {
    Kind() string
    Verify(context.Context, domain.NotificationBot, Credential) (Verification, error)
    Connect(context.Context, domain.NotificationBot, Credential, InboundSink) (Conn, error)
}

type Conn interface {
    Done() <-chan struct{}
    Reply(context.Context, OutboundMessage) SendResult
    Close() error
}

// 返回 Accepted=true 表示本次事件新建了交互；重复事件返回 Accepted=false 且 err 为 nil。
// 返回 err 时适配器按平台协议回失败确认，让平台重投。
type InboundSink func(context.Context, InboundEvent) (InboundAck, error)

// 主动通知通过应用 API 发送，实例只需持有通知投递租约。
type PushSender interface {
    Push(context.Context, domain.NotificationBot, Credential, OutboundMessage) SendResult
}

type SendOutcome string

const (
    Delivered    SendOutcome = "delivered"
    NotDelivered SendOutcome = "not_delivered"
    Rejected     SendOutcome = "rejected"
    Unknown      SendOutcome = "unknown"
)

type SendResult struct {
    Outcome           SendOutcome
    Code              string
    Retryable         bool
    PlatformMessageID string
    SafeDetail        string
}
```

第 2 版写的 `InboundSink` 只返回 `error`，第 3 版改为同时返回 `InboundAck`。原因是企微要在“新建交互成功”之后才发出“已收到，正在处理…”的接单流式消息，重复事件不能再发一次。

`InboundEvent` 包含以下内容：

- **消息字段：** 业务消息 ID、平台事件 ID、发送者、会话、聊天类型、消息类型、正文和引用；
- **可信事件字段：** 适配器从可信事件中读取的应用标识、企业范围和回复路由；
- **企微专用字段：** 卡片动作、投递 ID、卡片 task ID 和请求 ID。

服务端以当前连接对应的 Bot 配置决定项目、模型和权限，不信任事件中自带的项目信息。

`OutboundMessage` 包含：

- 内部交互或投递 ID；
- 冻结的目标；
- 正文；
- 是否为最终回复；
- 持久化的 `send_key`；
- 回复路由。

日志和公共 DTO 只使用脱敏后的信息。

企微的流式进度和卡片通过可选接口提供（`progressConn`、`cardConn`），飞书、钉钉首轮只用普通文本回复。

`PushSender` 由渠道对象实现，与 `Conn` 相互独立。阶段 A 中，企微的 `Push` 包装注入的 `wecom-cli` 发送函数；通知服务仍按原路径直接调用 `Provider`，到阶段 G 再切换到 `PushSender`。

企微的凭据校验复用 `CLIProvider` 的签名鉴权：阶段 A 新增 `CLIProvider.Verify`，由 `notification.New` 把它显式注入企微渠道。校验只调用鉴权接口，不建立短暂的验证连接，因此不会挤掉正在运行的接收连接。

### 4.2 身份和连接范围

JSON 中的 `botId` 继续作为外部应用标识。新增渠道字段 `channel`，取值为 `wecom`、`feishu`、`dingtalk`。

| 标识 | 历史企微 | 飞书、钉钉 |
| --- | --- | --- |
| 配置 ID | `NotificationBot.ID` | `NotificationBot.ID` |
| 外部 ID | 原 `BotID` | App ID / Client ID |
| 身份范围 `IdentityScope()` | 原外部 Bot ID | 内部配置 ID |
| 平台消息去重 | `BotMessageID(bot.IdentityScope(), messageID)` | `BotMessageID(bot.IdentityScope(), messageID)` |
| 内部卡片交互 ID | 保持 `BotMessageID(bot.ID, "notification:"…)` | 首轮不生成卡片 |
| 会话 ID | 保留现有派生规则（内部配置 ID、聊天类型、Chat ID） | 使用相同派生函数 |
| 接收占位 `ReceiveSlot()` | 对渠道和外部 ID 做稳定派生 | 对渠道和外部 ID 做稳定派生 |
| 连接租约 `LeaseKey()` | 原外部 Bot ID | 接收占位值 |

`ReceiveSlot` 采用 `channel + ":" + hex(sha256(externalID))`，长度小于现有列的 191 字符上限。只有在 `enabled && interactionEnabled` 时才写入这个唯一列，其余情况写 SQL `NULL`。

**创建后不可改的字段：** 配置 ID、渠道、外部应用 ID 和项目。企业范围按第 8.2、8.3 节先确认，进入 `locked` 状态后同样固定。

- 修改名称、模型、启停，或更换同一应用的 Secret，使用更新接口。
- 切换项目或应用，需要新建配置并重新绑定。
- 旧客户端提交时缺少 `channel`，一律按企微处理；新客户端必须明确提交渠道。

绑定的读取、创建挑战、确认、解绑、平台消息去重和助手执行，统一调用 `IdentityScope()`。企微的旧绑定继续按原键访问。

`bot_connections.bot_id` 存的是 `LeaseKey()`：

- 企微行存原外部 Bot ID；
- 飞书和钉钉行存 `ReceiveSlot()`，即 `channel:hex(sha256(externalID))`，例如钉钉为 `dingtalk:<Client ID 的 SHA-256 小写十六进制>`。

连接的领取、续约、释放、状态查询，以及第 7.1 节中按连接行加锁的参数，都统一传 `bot.LeaseKey()`。而 `bot_interactions.bot_id` 继续存内部配置 ID。这两列分别表示连接租约和配置归属，不能混用。

### 4.3 配置、凭据与重连

**保存流程。** Secret 的校验放在数据库事务之外完成。保存请求携带预期的 `configVersion`，事务内按第 7.1 节的锁顺序先锁配置行，比较版本，再原子更新配置和密文；版本冲突返回 `409 config_changed`。

**两个版本号。** 两者都是 `notification_bots` 上的物理列，payload 中同步保存一份用于展示：

| 版本号 | 递增时机 | 不递增的情况 |
| --- | --- | --- |
| `config_version` | 每次成功写配置：名称、模型、启停、写策略、企业锁定、目标变更 | 记录 `tenantCandidate` 等候选字段 |
| `credential_version` | 只在 Secret 变更时 | — |

**重连只看连接相关字段。** 接收服务比较以下字段的指纹：渠道、外部应用 ID、`credential_version`、`enabled`、`interaction_enabled`。指纹变化才关闭旧连接并重连。现有 `reconcile` 用 `UpdatedAt` 判断是否重连，阶段 B 改为比较这个指纹。

因此，企业锁定、改名称和模型只会增加 `config_version`，不会触发重连；从下一条事件或下一轮执行开始按新值校验。出站发送每次都重新读取配置和当前凭据。

停用和删除的状态处理见第 7.2 节。删除 Bot 的事务还会撤销该配置的挑战和绑定。对历史企微按外部 ID 关联的绑定，先核对是否还有其他保留的配置使用同一身份范围。历史记录中的 Secret 和会话 Webhook，在取消或进入终态后的清理中移除。

## 5. 数据模型、凭据和迁移

### 5.1 增量字段

| 实体或表 | 拟新增字段与约束 |
| --- | --- |
| `notification_bots` | 物理列：`channel`、`external_app_id`、`tenant_key`、可空唯一列 `receive_slot`、`enabled`、`interaction_enabled`、`config_version BIGINT NOT NULL DEFAULT 0`、`credential_version BIGINT NOT NULL DEFAULT 0`。payload 同步保存渠道、企业名称、`tenantState`、`tenantCandidate`（含候选事件 ID、接收时间、当时的凭据版本）、确认人和确认时间、`writeApproval`。物理列与 payload 在同一事务写入；归属判定只读物理列 |
| `user_bot_credentials` | 保留现有列，密文改用带版本的封装格式 |
| `bot_connections` | `epoch BIGINT`、`lease_until_us BIGINT`、换主次数、最后事件时间、鉴权失败次数、忽略的群消息次数；原有 owner 和 `lease_until` 列保留，仅用于展示 |
| `bot_interactions` | `project_id`（记录页授权快照）、`lease_owner`、`lease_epoch`、`config_version`、`platform_event_id`、`platform_message_id`、`msg_type`、`content_length`、`reply_route` 密文、`route_expires_at_us`、`send_key`、`delivery_generation`、`send_started_at_us`、`send_attempts`、`undelivered_retries`、`next_send_at_us`、`send_lease_until_us`、`resent_by`、`confirmed_by`、`confirmed_at`、`receipt_source`、`terminal_reason` |
| `bot_conversations` | 当前渠道所需的发送目标元数据、`target_verified_at`、提示限频的窗口和计数。授权型 URL 另存为密文 |
| `bot_user_bindings` | `config_id`，原身份主键保留。新渠道必填，历史企微通过核查回填 |
| `bot_binding_challenges` | `config_id`、`channel`、`sender_mask`；原 nonce/code 哈希和有效期保留 |
| `notification_deliveries` | `send_key`、`delivery_generation`、`send_started_at_us`、`send_lease_until_us`、`platform_message_id`、发送结果分类、`confirmed_by`、`confirmed_at`、`receipt_source`；继续使用现有 `lease_token` |
| `bot_pending_drafts` | 阶段 G 的企微网页确认草稿：用户、配置、项目、操作摘要、冻结参数、有效期、状态、执行 key。方法归 `AssistantRepo` |
| `bot_native_sessions` | 阶段 B 建表并写入：配置、项目、平台用户、会话、交互、engine、请求的 session ID、原生 session ID、服务端存储范围、相对日志路径、生命周期和清理状态。方法归 `AssistantRepo` |

索引：

- `idx_bot_queue` 覆盖配置、状态和 `next_send_at_us`；待处理状态查询与记录页各建所需的索引。
- 正文使用 utf8mb4。
- 标识的相等比较使用稳定的派生键或二进制比较，避免大小写折叠。

#### 交互状态枚举（`bot_interactions.status`）

| 状态 | 含义 | 是否终态 |
| --- | --- | --- |
| `queued` | 已入队，等待执行 | 否 |
| `running` | 正在由持有租约的 worker 执行模型或卡片动作 | 否 |
| `ready` | 回复正文已保存，等待发送（包括 Bot 停用期间暂存的回复） | 否 |
| `sending` | 已领取发送，网络调用可能在进行中 | 否 |
| `replied` | 平台确认送达，或管理员人工确认已收到（`receipt_source=manual`） | 是 |
| `reply_failed` | 平台明确拒绝、明确未送达且重试已耗尽，或回复路由已过期 | 是，可人工再次发送 |
| `reply_unknown` | 无法确认是否送达，等待人工核对 | 是，可人工确认或再次发送 |
| `cancelled` | 未执行就被取消，或执行中因停用、删除被取消 | 是 |

现有的 `ExpireBotCard` 把过期卡片记为 `replied` 并写入原因，这一行为保持不变。

#### 通知状态枚举（`notification_deliveries.status`）

| 状态 | 含义 | 是否终态 |
| --- | --- | --- |
| `pending` | 等待发送或等待重试 | 否 |
| `sending` | 已领取发送 | 否 |
| `sent` | 平台确认送达、人工确认已收到，或企微卡片已交给交互队列 | 是 |
| `failed` | 平台明确拒绝，或重试已耗尽 | 是，可重试 |
| `send_unknown` | 无法确认是否送达，等待人工核对（新增） | 是，可人工确认或再次发送 |
| `awaiting_authorization` | 渠道授权过期 | 否，授权恢复后重试 |
| `target_unavailable` | 目标不可用 | 是，可重试 |
| `cancelled` | Bot 停用或删除、来源已删除、项目不匹配 | 是 |
| `expired` | 超过 24 小时，或等待节点已推进 | 是 |

通知不使用交互的 `reply_*` 状态名。前端徽标和查询按两张表各自的枚举处理。

### 5.2 数据库时间

所有租约、发送窗口、重试时间和路由有效期，**写入和判断都使用数据库时间**，Go 进程的墙钟只用来决定何时在本地提前取消。

- **存储格式。** 这些字段以 epoch 微秒存为 `BIGINT`，列名带 `_us` 后缀，避免 MySQL `DATETIME` 与 SQLite 文本时间在格式和时区上的比较差异。
- **方言函数。** `internal/repo/dbtime.go` 提供 `dbNowUS(db)`，返回当前方言的 SQL 表达式：
  - MySQL：`CAST(UNIX_TIMESTAMP(NOW(6)) * 1000000 AS SIGNED)`，与会话时区无关；
  - SQLite：`CAST(ROUND((julianday('now') - 2440587.5) * 86400000000.0) AS INTEGER)`。
- **写入与判断都在 SQL 内。** 写入时用 `SET lease_until_us = <dbNowUS> + 45000000`，判断时用 `lease_until_us > <dbNowUS>`。Go 端不传入当前时间值。
- **测试。** 阶段 B 为该函数单独写测试，覆盖 SQLite 下的写入、比较、跨秒边界和 `OpenTest` 时间注入；MySQL 测试覆盖相同的断言。可控时钟测试通过替换 `dbNowUS` 表达式为固定值实现。
- **展示列。** 原有的 `DATETIME` 列（如 `lease_until`）保留，在同一语句中一并写入，仅用于展示和兼容，不参与判断。

### 5.3 凭据加密

`internal/credcrypt` 使用标准库的 AES-256-GCM，每次加密生成随机 nonce。

**密钥配置。** 通过 `AGENTFLOW_CREDENTIAL_KEYS` 注入，格式为 `kid:base64(32字节)[,kid:base64(32字节)]`，第一把是当前写入密钥。

**密文格式。** 封装为 `lf1:<kid>:<base64(nonce || ciphertext)>`。凭据内容序列化为 `{"secret":"..."}`，AAD 编码配置 ID 与渠道。会话 Webhook 的 AAD 编码配置、交互 ID 和字段用途。

**读写规则：**

- 历史明文仍可读取，管理页标记为“待迁移”。
- 带 `lf1:` 前缀的数据必须按密文解析，解密失败直接返回错误。
- 新写入需要有效密钥；密钥缺失时返回 `503 credential_key_missing`。

**迁移命令。** 增加运维子命令 `-credential-action status|reencrypt`，通过显式指令逐条迁移旧明文或旧 kid 密文，记录成功、跳过和失败的数量。命令可以重复执行。密钥解析和 Repo 注入使用现有的装配入口，日志只记录 kid 和计数。

### 5.4 手动迁移顺序

遵守 `AGENTS.md` 中“增量 DDL 与手工数据迁移”的约定。生产 MySQL 是验收数据库，SQLite `OpenTest` 用于服务测试。

1. **备份与只读检查。** 备份数据库和原版本二进制。先跑只读检查，列出重复启用的企微配置、孤立绑定和无法判断归属的绑定。
2. **停旧副本，以维护模式启动新版本。** 先停止**全部**旧版本副本。`AGENTFLOW_BOT_WORKERS_ENABLED` 是阶段 B 新增的开关，旧版本二进制不认识它，所以不能指望旧版本靠这个开关暂停 worker。然后以 `AGENTFLOW_BOT_WORKERS_ENABLED=false` 启动一个新版本实例。开关关闭时：
   - 对话和通知两个后台入口都不启动；
   - Bot 配置的写接口返回 `503 bot_maintenance`；
   - 启动时的增量 DDL 补齐缺失的表、列和索引，`receive_slot` 初始为 `NULL`。
3. **运行 `036` 回填段。** 填写渠道、外部应用 ID、接收占位、物理启停列和版本列。缺少渠道的旧值统一识别为企微。回填前先处理重复占位；脚本失败时事务回滚。
4. **运行 `037` 归属查询。** 明确匹配的，回填绑定和挑战的 `config_id`。多个配置共用旧企微身份的记录，保留清单，按真实启用的配置确认归属。
5. **重新加密凭据。** 所有副本配置相同的 Keyring，执行显式的 `reencrypt`，复核计数。
6. **恢复服务。** 以 `AGENTFLOW_BOT_WORKERS_ENABLED=true` 重启全部新版本副本。先验证企微，再依次启用飞书和钉钉。

**脚本补充说明：**

- `036` 同时补齐 `bot_native_sessions` 的基线 DDL 和交互的项目归属快照。
- `037` 对历史原生会话单独输出“可证实映射”和“待核查”两份清单；映射证据不足的记录标记为待核查。
- 脚本先确认所需的列存在再引用；DDL 与回填分段执行，出错即停止。
- MySQL 版本兼容性通过目标测试镜像验证，按 schema 检查生成明确的手工 DDL。
- 迁移前后记录表行数、绑定归属计数和冲突数。

**升级与回退。** 升级完成后，所有运行中的副本使用同一套新租约协议。凭据加密完成后，回退的二进制必须具备相同的解密能力；如果要退回更旧的版本，使用升级前的备份，并按停机窗口核对期间新增的业务数据。

## 6. 入站、绑定与业务权限

### 6.1 公共入站流程

处理链路：`SDK 事件 → 校验当前配置与应用/企业 → 分类 → 事务入队 → 平台确认 → worker 执行`。阶段 A 已把“由事件生成交互并入队”提取到 `inbound.go` 的 `Service.accept`；企微、模拟渠道以及后续的飞书、钉钉适配器都经过这个入口。

**确认时限。** 飞书 handler 只做校验和有界的数据库事务，要留在官方约 3 秒的确认窗口之内，因此入队操作设置短超时。

**确认结果。** 入库成功和重复事件都正常确认。数据库写入失败时，handler 返回错误；飞书固定版本 SDK 会据此回复 500，平台随后重投（见第 8.2 节）。 [飞书长连接](https://open.feishu.cn/document/ukTMukTMukTM/uYDNxYjL2QTM24iN0EjN/event-subscription-configure-) · [飞书事件重投](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/event-subscription-guide/event-subscriptions)

**去重。** 业务去重键是 `IdentityScope + 平台业务消息 ID`，平台事件 ID 另存，便于排查。平台重投时即使事件 ID 不同，同一业务消息也只执行一轮。接单提示同样通过持久化记录发送，与最终回复使用不同的 key。

**输入限制：**

- 新渠道文本上限暂定 8000 个 Unicode 字符。
- 非文本和超长输入只记录类型与长度，并按限频提示用户；正文在进入助手之前完成分类。
- 企微沿用已有的输入与历史限额。
- 同一个 Bot 的同一会话串行处理。

### 6.2 两阶段绑定

1. 未绑定的私聊发送者收到 `/bot-bind.html#request=<nonce>`。服务端只保存 nonce 的哈希，有效期 15 分钟。
2. 网页通过现有的 `/api/auth/current` 登录，显示渠道、Bot 名称、项目、脱敏后的发送者，以及将要绑定的账号。
3. 用户确认后，页面发起同源 POST `/api/bot-bindings/claim`。服务器保存短期确认码的摘要，页面显示 `/bind <code>`。
4. 用户在原 IM 中发送确认码。适配器从原 Bot 的可信事件中读取发送者；公共处理校验配置范围、原发送者、有效期和确认码摘要，在同一事务中写入绑定并消费挑战。
5. 绑定成功后提示用户重新发送业务请求。挑战、确认码和绑定指令在进入模型和构造历史之前剔除。

**解绑与复核。** 沿用现有的 `/unbind` 命令和个人配置中的解绑入口。状态 API 和确认 API 都会复核：配置存在、Bot 已启用、当前账号有效、有项目访问权限。绑定页只接受同源写请求，授权依赖网页登录会话。

**限频：**

- 引导消息默认 10 分钟最多发 3 次；用户主动获取新链接每分钟最多 1 次；计数使用原子更新。
- 已有有效挑战时，提示用户查看上一条链接。
- 旧链接只存了哈希，无法重新生成；用户主动获取新链接时，作废旧挑战并创建新挑战。

### 6.3 执行业务和只读约束

`botExecutionUser` 每轮都读取当前配置和绑定，核对：用户存在、Bot 状态、项目成员权限、配置版本。每次工具调用仍经过平台原有的权限检查。绑定有效性和工具授权在执行边界再验证一次，以覆盖执行过程中被解绑或撤权的情况。

阶段 D 在 `assistantExecMode` 中加入明确的只读标记，并贯穿工具目录、工具网关和终态草稿处理：

- **平台工具。** 只读工具继续按项目权限开放；写工具在工具目录和执行网关两处按同一策略拒绝。
- **外部 MCP 和原生工具。** 按真实能力分类，只开放已确认为只读的能力。CLI 命令、文件写入和其他具有写入能力的工具，在只读模式下一律拒绝。
- **写草稿。** 模型产生写草稿时，回复“请在平台页面确认操作”。新增渠道首轮只通过只读助手完成状态查询。
- **模型凭据。** 使用绑定用户自己的模型凭据；用户未配置密钥时提示其配置，不回退到 Bot 创建者的身份。
- **提示词边界。** 模型输入保留角色层级和用户原始消息，提示词中的输入标记用于区分材料来源。写权限和目标校验都在服务端实施，不依赖提示词。

用三类真实项目查询验证上述策略：项目状态、SOP 进度、本人待办。首批真实验收使用普通项目成员账号。

## 7. 连接租约与发送状态机

### 7.1 锁顺序、连接与交互归属

**锁顺序固定为：配置 → 连接 → 交互/投递。** 同一类表有多行时，按主键升序加锁。所有带归属条件的写操作（保存结果、开始发送、写回发送结果、写卡片 task ID、切换会话、恢复、停用、删除、人工补发、人工确认）都在一个事务内按以下步骤执行：

1. `SELECT … FROM notification_bots WHERE id = ? FOR UPDATE`：读物理列 `enabled`、`interaction_enabled`、`config_version`。
2. `SELECT … FROM bot_connections WHERE bot_id = <bot.LeaseKey()> FOR UPDATE`：检查 `owner`、`epoch`，以及 `lease_until_us > <dbNowUS>`。删除、停用和人工操作没有连接 owner，只加锁，不比较 owner。
3. 按条件更新交互或投递行，检查 `RowsAffected`。

```sql
-- 第 3 步示例（MySQL）；第 1、2 步已在同一事务内持锁
UPDATE bot_interactions
SET status = ?, updated_at = ?
WHERE id = ?
  AND status = ?
  AND lease_owner = ?
  AND lease_epoch = ?;
```

第 1、2 步任一条件不满足，或第 3 步更新了 0 行，就返回 `ErrLeaseLost` 或明确的状态冲突，执行循环随即停止本地工作。SQLite 没有行锁，以数据库级写锁串行化；`r.locking(tx)` 在 SQLite 上不加子句，测试中语义相同。阶段 C 的评审清单逐个核对每条写路径的加锁顺序；MySQL 测试用两个并发事务复现“删除”与“写回结果”的交错，确认不会死锁。

**连接租约。** 继续采用 45 秒 TTL、10 秒续约，写入和判断按第 5.2 节使用数据库时间。

- owner 变更或租约过期后重新领取时，原子递增 `epoch`；同一个有效 owner 续约时保持 epoch 不变。
- 领取交互时保存 `lease_owner`、`lease_epoch` 和 `config_version`。连接相关的 Repo 方法统一以 `bot.LeaseKey()` 为键；领取交互时的配置过滤条件使用 `bot.ID`。

**续约失败与恢复。** 接收副本续约失败时，关闭连接，并取消模型和发送的上下文。恢复时：

- 旧 epoch 下的 `running` 记录，写入现有提示“执行已中断，请核查结果”；
- `ready` 记录继续投递已保存的结果。

### 7.2 回复发送、重试和终态

```text
queued → running → ready → sending → replied
             └──中断──→ ready（中断说明）

sending → 明确未送达，未达重试上限      → ready（next_send_at_us，保留正文和 key）
sending → 明确未送达，已达重试上限      → reply_failed（undelivered_exhausted）
sending → 平台明确拒绝                  → reply_failed
sending → 送达未知（飞书，窗口内）      → ready（同一 key，按窗口退避）
sending → 送达未知（飞书窗口已到期 / 企微 / 钉钉） → reply_unknown
ready   → 回复路由过期                  → reply_failed（route_expired）
reply_unknown / reply_failed → 人工再次发送 → ready（generation + 1，新 key）
reply_unknown → 人工确认已收到          → replied（receipt_source=manual）

queued              → 停用 / 删除 → cancelled（bot_disabled / bot_deleted）
running             → 停用 / 删除 → cancelled，并向本地任务发送取消信号；执行记录保留
ready               → 停用        → 保持 ready（暂存，停用期间不领取）
ready               → 删除        → cancelled（bot_deleted，正文保留在记录页）
sending             → 停用 / 删除 → reply_unknown（bot_disabled_during_send / bot_deleted_during_send）
```

**两类自动重试分开处理：**

| 结果 | 判断依据 | 处理 |
| --- | --- | --- |
| `Delivered` | 平台确认该回复成功 | 记为 `replied`，保存回执。该状态只表示平台确认已发送 |
| `NotDelivered` | 有证据表明请求未提交，或平台明确表示未发送且可重试 | **按次数重试**：首次失败后最多自动重试 3 次，间隔 5 秒、30 秒、2 分钟，`undelivered_retries` 单独计数；耗尽后转 `reply_failed`（通知转 `failed`）。所有渠道一致，重试沿用同一个 key |
| `Rejected` | 平台明确拒绝 | 直接转 `reply_failed`，显示安全错误码。已核对过的限流码或可刷新的鉴权错误，按 `NotDelivered` 处理 |
| `Unknown` | 请求可能已写出：读回执超时、网络中断，或进程在发送阶段退出 | **飞书**：按固定 key 和时间窗口恢复（第 7.3 节），不计入 `undelivered_retries`；窗口到期转 `reply_unknown`。**企微、钉钉**：直接转 `reply_unknown`（通知转 `send_unknown`） |

发送步骤：

1. 领取 `ready` 记录，复核绑定、项目、当前配置和回复路由。复核以当前配置为准，要求应用身份、企业范围和启用状态有效，不要求与生成回复时的 `config_version` 相等。这样停用期间暂存的回复，重新启用后仍可发送。
2. 按第 7.1 节的锁顺序原子写入：`sending`、当前 generation 的首次 `send_started_at_us`（只在为空时写）、发送次数、短发送租约 `send_lease_until_us`。`send_key` 在首次生成回复时就已持久化。
3. 事务提交后再调用 SDK，网络操作不在事务内。单次发送超时 10 秒；发送前要求连接租约至少还剩 15 秒。
4. 按平台回执和请求证据分类，带条件写回结果和平台消息 ID。

**送达证据。** `WriteJSON` 报错时请求可能已部分写出；HTTP 5xx 也可能出现在平台已经处理请求之后。因此分类依据实际的请求证据，不看错误类型。现有企微“流式失败就主动再发一次”的路径，阶段 C 起只在确认未送达时才启用备用发送。

**换主时的发送窗口。** `sending` 状态的交互被新 owner 接管时，保留它原有的发送占用窗口。新 owner 要等原发送租约到期、再加上预留的时钟偏差之后，才执行平台幂等恢复或转为待核对。外部 IM 接口无法检查数据库 epoch，所以靠发送窗口、固定 key 和未知状态三者共同防止重复发送。

**停用与删除。** 停用和删除事务都按锁顺序执行：

- **迟到写回。** 成功写回先提交的，交互保留 `replied`、通知保留 `sent`；停用或删除先提交的，`sending` 记录转为 `reply_unknown` 或 `send_unknown`。旧 worker 迟到的写回会因状态、租约或配置校验失败而停止，迟到的回执写入独立审计。
- **删除后的待核对记录。** 仍可人工核对、确认已收到；人工再次发送返回 `409 bot_deleted`。删除事务清空回复路由、发送租约和旧的写回凭据，保留正文、generation、key 和已有回执。对话记录保存项目归属快照，项目管理员可以从项目记录页继续查看。
- **停用期间暂存的回复。** 记录页显示“停用期间暂存”。重新启用后，回复路由仍有效的正常发送，已过期的转 `reply_failed`（`route_expired`）。
- **停用对通知的影响。** 沿用现有行为：领取时发现停用，记为 `cancelled`；处于 `sending` 的转为 `send_unknown`。

### 7.3 固定 key 与人工处理

**固定 key。** 首次生成回复时，持久化一个随机 UUID 或等价的不透明 key。自动重试从数据库读取同一个 key、正文和目标。

**飞书恢复窗口。** 飞书的 `uuid` 参数提供一小时的请求去重窗口，本方案以当前 generation 的“首次 `send_started_at_us` + 55 分钟”作为恢复上限：

- **时钟。** 首次时间值由数据库时间写入，恢复和重试资格也用同一个数据库时钟计算。自动重试、连接易主、进程重启都不重置这个值。
- **每次发送前检查。** 每次调用 SDK 前都重新检查窗口，并要求单次请求的超时也落在上限之内。
- **退避。** 窗口内依次按 5 秒、30 秒、2 分钟退避，之后最多每 2 分钟一次；到截止时刻转 `reply_unknown`。

[飞书固定版本 SDK 的回复去重字段](https://github.com/larksuite/oapi-sdk-go/blob/v3.13.0/service/im/v1/model.go)

**SDK 自身的重试。** 适配器显式设置 SDK 和 HTTP 客户端的重试策略，确保底层重试沿用同一个 key。没有平台幂等保护的发送，由公共状态机决定是否重试，并关闭 SDK 的自动重发选项。

**记录页操作。** 企微和钉钉在缺少已验证的平台幂等能力时，`Unknown` 一律进入 `reply_unknown`。记录页分别提供“确认已收到”和“再次发送”两个操作，“再次发送”会明确提示可能重复。

**人工再次发送：**

- 只接受 `reply_unknown` 或 `reply_failed` 状态的记录。
- 在事务内校验：操作权限、Bot 和绑定有效、目标和路由仍可用。
- 校验通过后：状态改为 `ready`，`delivery_generation += 1`，生成新 key，重置发送次数、`undelivered_retries`、`next_send_at_us`，并把当前 generation 的 `send_started_at_us` 置空（首次进入 `sending` 时由数据库填写）。
- 留下操作人和原因，只投递已保存的结果，之后由正常的发送 worker 领取。
- 请求的 action key 去重覆盖状态更新，重复点击返回同一个 generation。

**人工确认已收到：**

- 交互从 `reply_unknown` 改为 `replied`，通知从 `send_unknown` 改为 `sent`。
- 写入 `confirmed_by`、数据库时间 `confirmed_at` 和 `receipt_source=manual`，界面显示“人工确认”。
- 相关审计字段在阶段 C 一并加入交互和通知模型。

**冲突返回。** Bot 已删除时，人工再次发送返回 `409 bot_deleted`；路由已过期时返回 `409 route_expired`。路由过期的钉钉回复，到阶段 G 后可改用主动单聊补发，见第 9.2 节。

## 8. 三个渠道的实现细节

### 8.1 企业微信

**接收。** 阶段 A 起由 `channel_wecom.go` 包装 `dial`、`socket.request`、`stream` 和卡片更新。`socket.request` 的错误现在带有发送阶段，分类规则如下：

| 情况 | 分类 |
| --- | --- |
| 写出前失败 | `NotDelivered` |
| 写出失败，或已写出后回执超时、连接关闭、上下文取消 | `Unknown` |
| 平台返回非 0 错误码 | `Rejected` |

阶段 A 只把分类结果映射回原有行为，用户可感知的行为不变：

- `Delivered` 记为已发送；
- `Rejected` 记为发送失败；
- 其他情况断开重连，之后重发 `ready` 记录；
- 流式失败后仍然备用发送一次。

**主动通知。** 继续沿用官方 CLI 的会话发现、目标校验和授权状态。

**兼容回归固定以下内容：**

- 历史外部 Bot ID、绑定 ID、消息 ID、租约 key 的派生结果；
- 私聊、群聊、群内 @、新会话命令与流式进度；
- `waiting`、`failed` 通知卡片的服务端 task ID 和操作者授权检查；
- 主动通知的目标发现、授权过期处理和最终文本；
- 同 key 的平台回执、连接中断、发送结果未知和人工补发。

### 8.2 飞书

**依赖版本。** 官方 `github.com/larksuite/oapi-sdk-go/v3@v3.13.0`。阶段 A 已在 Go 1.26.11 下用探针模块验证，结论如下：

- `ws.Client.Start(ctx)` 会阻塞，context 取消后返回 `context canceled`；
- 提供 `Close` 和 `CloseAndWait`；
- 事件 handler 返回错误时，SDK 回复 500，平台据此重投；
- `ReplyMessageReqBody.Uuid`、`CreateMessageReqBody.Uuid`、`tenant/v2` 的 `Query` 均可编译；
- govulncheck v1.9.0 未发现漏洞。

依赖在阶段 E 首次实际使用时写入主模块的 `go.mod`、`go.sum` 和 `THIRD_PARTY_NOTICES.md`，避免 `go mod tidy` 移除尚未使用的依赖。 [飞书 Go SDK](https://github.com/larksuite/oapi-sdk-go)

| 环节 | 实施约定 |
| --- | --- |
| 应用 | 企业自建应用，开启机器人、单聊接收和发送权限，发布给试点成员 |
| Verify | 用 App ID / Secret 获取 `tenant_access_token`，验证凭据。企业归属进入 `awaiting_confirmation`，按下文流程锁定 |
| 企业信息辅助 | 用应用令牌调用 `GET /open-apis/tenant/v2/tenant/query`（官方“获取企业信息”接口，自建应用可用，频率上限 1000 次/分钟）。固定版本 SDK 的 `Tenant` 模型包含 `tenant_key`、企业名称和 display_id。查询结果只作为确认页的辅助信息，与事件候选值并排展示；接口失败（如 `1184001` 无权限）不阻断流程。所需权限以开放平台应用后台的显示为准 |
| Connect | 官方长连接客户端加消息事件 dispatcher，设置 `WithAutoReconnect(false)`，由公共连接循环负责重连。`Start` 返回时关闭 `Conn.Done()`，`Close` 调用 `CloseAndWait` |
| 事件 | `header.app_id`、`header.tenant_key`、`event_id`、`message_id`、`sender.open_id`、`chat_id`、`chat_type`、`message_type`、文本 JSON |
| 原会话回复 | Message Reply 接口，传原 `message_id`、文本和已持久化的 `send_key`（作为 `uuid`） |
| 主动通知 | Message Create 接口，使用当前 Bot 已验证的目标，带独立的投递 key |
| 鉴权与限流 | 从 SDK 返回中提取平台错误码，建立明确的可重试名单；遇到未知错误码时保守地停止自动重发，并显示可诊断的原因 |

**企业确认流程（`tenantState`）。** 飞书和钉钉共用这套状态：`awaiting_confirmation` → `locked`，异常时为 `conflict`。

1. 新配置通过 Verify 后保存 `tenantState=awaiting_confirmation`。接收配置照常占用唯一的 ReceiveSlot，连接可以接收用于发现企业的事件。管理页显示“等待企业确认”。
2. 适配器从当前凭据认证的 SDK 连接中，读取 App ID 匹配、`tenant_key` 非空的首个消息事件，保存 `tenantCandidate`、事件 ID、接收时间和当时的凭据版本。**写候选字段不增加 `config_version`。** 该事件到此结束：
   - 不创建绑定或业务交互；
   - 通过一条持久化提示记录回复发送者：“机器人正在完成企业确认，请稍后重发消息。”
   - 出现不同的候选值时，状态转为 `conflict`，显示冲突诊断，等待管理员重新发起发现。
3. Bot 管理员在同源页面确认候选企业：POST `/api/notification-bots/:id/tenant/confirm`，携带候选值和 `configVersion`。事务检查候选来源、当前凭据版本、管理权限，然后写入固定的 `tenant_key`、确认人、数据库确认时间，状态变为 `locked`，`config_version` 加一。因为第 2 步没有改版本号，管理员页面拿到的版本仍然有效；锁定也不触发重连（见第 4.3 节）。
4. 锁定后，提示测试用户重新发送业务消息。之后接收的每个事件都必须与固定值匹配。轮换同一应用的凭据时保留锁定范围，并继续检查事件归属。要更换应用或已锁定的企业范围，需新建配置。
5. 阶段 E 的适配器测试、HTTP 测试和真实测试覆盖：发现前、候选已采集、确认并发、凭据轮换、锁定后跨企业消息被拒绝。已取到令牌但尚未锁定的 Bot，在 UI 和 API 中持续显示“企业待确认”。

飞书事件样例需覆盖：企业或应用不匹配、同一消息不同投递 ID、应用范围内的 `open_id`、短事务失败、SDK 关闭。接收方使用 `open_id` 时保留其应用范围。 [飞书身份范围](https://open.feishu.cn/document/platform-overveiw/basic-concepts/user-identity-introduction/introduction)

### 8.3 钉钉

**SDK 只用一部分。** 依赖为官方 `github.com/open-dingtalk/dingtalk-stream-sdk-go@v0.9.1`，这是当前最新的稳定版，另有 `v0.9.2-beta.1`。阶段 A 的探针验证发现以下问题，因此**阶段 F 不使用 SDK 的 `StreamClient` 连接循环和 `ChatbotReplier`**：

| 问题 | 证据 | 影响 |
| --- | --- | --- |
| 心跳超时后再收到一帧，未恢复的 panic 会使进程退出 | 探针中服务端不回 pong，10 秒后推送一帧文本，子进程以 `send on closed channel` 退出（exit 2）。原因是 `processLoop` 退出时关闭了 `readChan`/`closeChan`，而读协程和 pong 超时协程仍会向它们写入 | 宿主进程整体崩溃，所有项目的服务中断 |
| `Start` 建连不接受 context | 内部调用 `dialer.Dial`，而不是 `DialContext` | 停止时可能卡住 |
| 自动重连使用 `context.Background()` 无限循环 | `reconnect` 实现 | 调用 `Close` 后仍可能重新连上，形成僵尸连接 |
| `ChatbotReplier` 使用默认重定向策略、固定 5 秒超时，200 响应未关闭 body | `ReplyMessage` 实现 | 违反“禁止重定向”约定，并泄漏连接 |

阶段 F 只采用该 SDK 中经过验证、无副作用的部分：

- 用 `payload.ConnectionEndpointRequest/Response` 获取接入点（实施时改为自建请求：SDK 的 `GetConnectionEndpoint` 挂在 `StreamClient` 上，使用默认传输和重定向策略，与本节的 HTTP 约定不符）；
- 用 `payload` 包解析 DataFrame、构造确认帧；
- 用 `chatbot.BotCallbackDataModel` 解析事件。

WebSocket 读写循环、心跳和重连由适配器基于主模块已有的 `gorilla/websocket v1.5.3` 实现，与企微连接的生命周期一致。

会话 Webhook 和主动发送使用自建的 HTTP 客户端：禁止重定向，设置明确超时，限制响应读取长度。如果上游修复了上述问题并发布稳定版，再评估是否改回 SDK 循环。

**凭据校验。** 该 SDK 不提供 access token 接口，Verify 直接调用官方 `POST https://api.dingtalk.com/v1.0/oauth2/accessToken`。 [钉钉 Stream Go SDK](https://github.com/open-dingtalk/dingtalk-stream-sdk-go)

| 环节 | 实施约定 |
| --- | --- |
| Verify | 用官方 accessToken 接口校验 Client ID / Secret |
| 企业确认 | 复用第 8.2 节的 `tenantState`。管理员必须手填企业 CorpId，作为预期值保存，状态为 `awaiting_confirmation`。首个可信事件的企业字段与预期值一致时，自动转为 `locked`，记录事件 ID 和数据库时间，确认人记为填写 CorpId 的管理员，`config_version` 加一；不一致时转为 `conflict`，不处理业务，由管理员修正后重新发现。等待确认期间的消息回复与飞书相同的提示 |
| Connect | 自建 Stream 读写循环，注册机器人回调 topic；事件入库成功后回复成功确认帧，入库失败时回复错误帧 |
| 发送者 | 优先读取经过验证的 `senderStaffId`，与当前企业范围组合；字段缺失时拒绝业务执行并记录诊断 |
| 事件 | `msgId`、`conversationId`、`conversationType`、`chatbotCorpId`、`senderCorpId`、`msgtype`、`text.content` |
| 应用归属 | 绑定当前应用的连接。平台原始事件中带有 `robotCode` 时，解析并核对；SDK 模型未暴露的字段，用原始 payload 的解析测试覆盖 |
| 会话回复 | 保存 `sessionWebhook` 的密文和 `sessionWebhookExpiredTime`，后者按毫秒解释，转成 `route_expires_at_us`；发送前留出超时的安全余量 |
| 到期 | 保存业务结果，原因记为“会话回复授权已过期”，状态为 `reply_failed`（`route_expired`），可在记录页查看结果 |
| 主动通知 | 使用官方机器人单聊主动发送接口，发给已验证的本企业员工目标，不依赖 sessionWebhook |

`v0.9.1` 的公开事件模型没有定义 `robotCode`，因此应用归属校验基于实际的连接上下文和原始事件契约实现。`chatbotCorpId` 的格式与配置中 CorpId 的对应关系，在真实试点验证后记录到渠道手册。 [钉钉固定版本事件模型](https://github.com/open-dingtalk/dingtalk-stream-sdk-go/blob/v0.9.1/chatbot/model.go)

会话 Webhook 只接受可信 Stream 事件给出的平台 HTTPS 地址：校验官方允许的主机和路径，禁止重定向，固定请求方法和内容。用户输入和模型结果都不能提供该地址。发送进入终态后，按数据保留策略清理授权 URL。

## 9. 助手与主动通知

### 9.1 助手接入

新增渠道首轮通过 `generateBotReply` 进入现有助手，复用 `openAssistantToolSession`、`botReplyMessages` 和平台工具网关。渠道适配器只负责传输，项目和用户权限仍由服务端确定。

**写策略：**

- 企微旧 Bot 的 `auto` 策略通过兼容分支读取。
- 新建 Bot 一律采用 `readonly`。
- 阶段 G 为企微新增可选的 `confirm` 策略：写草稿保存后，在 IM 中返回操作摘要和 `/bot-approve.html#draft=<id>` 链接。

**网页确认。** 要求登录，并检查绑定用户、Bot 配置、项目权限、草稿有效期和冻结参数。

- 草稿使用随机 ID。确认请求（POST）原子地把状态从 `pending` 改为 `executing`，并把草稿的执行 key 传给实际的业务写入。
- 执行中断的草稿进入待核对；用户重复点击时，返回当前的执行状态。
- 解绑、停用、删除 Bot 或撤销权限，都会使 `pending` 状态的草稿失效。
- 草稿执行复用现有的 `resolveAssistantDrafts` 和预检逻辑。网页确认相关的改动放在 `AssistantRepo` 和 `assistantHandlers`。
- 飞书、钉钉的 `confirm` 模式，在后续按同一契约验收后开放。

### 9.2 SOP 主动通知

`notification.Service` 保留现有的投递任务、报告生成和 `lease_token`。飞书、钉钉通过 `PushSender` 发送，在只启用通知、未启用对话的配置下也能工作。

1. 在用户已绑定的可信私聊中记录目标元数据；管理页从本 Bot 已知的会话中选择目标。
2. 服务端试发成功后写入 `target_verified_at`，并绑定当时的配置版本和实际平台目标。
3. SOP 用原有的状态事件生成通知，覆盖现有枚举 `succeeded`、`failed`、`waiting` 的全部取值。
4. 发送前重新读取 Bot 和目标，保留投递记录中冻结的项目、目标和 key，按第 7 节的规则分类结果。通知的未知送达结果使用 `send_unknown`。
5. 处理节点等待通知时，复用 `waitingNodeStale` 和原有的轮次校验；节点已经推进的，取消过时通知。

**目标维护：**

- Bot 更换 Secret 后，重新验证原应用身份，目标验证状态随轮换结果一并更新。
- 切换目标时，旧的待发任务要么显式取消，要么由人工选择迁移，操作都记录下来。

**钉钉的额外限制：**

- 钉钉的通知配置，只有在独立主动发送接口真实验证成功后才能启用。
- 管理页分别展示接收连接健康、会话回复能力和主动发送权限。

**钉钉过期回复改用主动单聊补发（阶段 G）。** 前提是该 Bot 的主动单聊发送已通过真实验证，即 `target_verified_at` 有效。满足前提时，对于状态为 `reply_failed` 且原因是 `route_expired` 的钉钉私聊回复，管理员可以在记录页**显式选择**“改用主动单聊发送”。服务端在事务内重新校验以下几项：

- 操作者对该 Bot 的管理权限；
- 原发送者的绑定仍然有效，并且对项目仍有权限；
- 目标是本 Bot 已验证的、属于原发送者的单聊目标，且属于同一企业范围。

校验通过后，按人工再次发送的规则处理：`delivery_generation += 1`，生成新的发送 key；回复路由切换为主动发送，只投递已保存的结果；记录操作人、原因和路由切换。没有已验证目标时，该按钮不显示，接口返回 `409 push_target_unverified`。

### 9.3 阶段 G 实现说明（与上文的差异）

- **目标验证记录在绑定上。** 第 5.1 节把 `target_verified_at` 放在 `bot_conversations`。实现放在 `bot_user_bindings`：飞书、钉钉的通知目标就是在本 Bot 单聊中绑定的成员（飞书 open_id、钉钉 staffId），解绑或重新绑定会清除验证，比按会话记录更直接。配置上的 `target` 只保存所选成员，展示时从绑定读取验证状态。
- **验证不绑定凭据版本。** 应用标识创建后不可修改，更换 Secret 时先校验同一应用，因此验证在轮换后继续有效，不随凭据版本失效。
- **业务写入没有幂等键。** 第 9.1 节要求把执行 key 传给业务写入；平台写入接口目前没有幂等参数。实现中执行 key 只记录在草稿上，“只执行一次”由 `pending → executing` 的加锁状态迁移保证，执行中断的草稿转为待核对、不再执行。
- **切换接收人的处理只覆盖飞书、钉钉。** 企微切换接收会话的行为保持不变，以免改变已有配置的使用方式。

## 10. 阶段任务清单

| 阶段 | 具体改动 | 必须通过的验证 |
| --- | --- | --- |
| A | 定义渠道契约，包装企微，接入模拟渠道，提取公共入站入口，分类发送结果；用探针模块验证 SDK 固定版本 | 既有企微用例全部通过；模拟渠道能控制接收、回执和断线；探针记录 SDK 生命周期、确认行为和漏洞扫描结果 |
| B | 领域字段和标识方法、物理列（含启停和版本）、`dbNowUS` 方言函数、凭据加密、生命周期事务、CLI 会话映射表及启动前写入、维护开关、手动 SQL、配置 UI | 历史键值逐字相等；并发保存时只启用一个接收配置；SQLite 和 MySQL 数据库时间比较测试通过；新 Bot 的原生 CLI 运行可追溯；密钥轮换和迁移重入通过 |
| C | epoch、锁顺序和归属条件、发送 key、两段式发送、短发送租约、两类重试、停用和删除语义、状态枚举、人工操作 | 旧 owner 失效后无法保存结果；`sending` 恢复遵守窗口；未知状态不会误记为成功；重试耗尽转失败；停用保留 `ready`；删除与写回并发时不死锁 |
| D | 两阶段绑定元数据与限频、项目权限复核、只读工具策略 | 换人点击链接无法完成原发送者的绑定；撤权生效；写工具与原生写入被拒绝 |
| E | 飞书 Verify / Connect / Reply、企业确认（含企业信息辅助查询）、Bot 配置表单、官方事件 fixture；写入依赖 | 锁定前只采集候选；锁定后在真实租户完成绑定、状态查询、同消息去重和重启恢复 |
| F | 钉钉自建 Stream 循环、企业确认（手填 CorpId）、会话路由密文与过期；写入依赖 | 真实企业通过与飞书相同的用例；过期回复可在记录页核对结果；连接异常不导致进程退出 |
| G | 独立主动发送、三类 SOP 通知、企微 confirm、身份撤销联动、钉钉过期回复改用主动单聊补发 | 通知专用 Bot 发送成功；过时的等待通知被取消；重复确认只执行一次；改用主动发送重新校验并使用新 key |
| H | 分批清理、安装配置、迁移与回退手册、最终测试记录 | 三个渠道的实际验收结果可追溯；CI、API 和前端文档与最终实现一致 |

**数据保留。** 暂定正文保留 90 天、终态元数据保留 180 天，可通过配置调整。每小时分批清理一次，每批 1000 条，按终态白名单和实际完成时间筛选。`running`、`ready`、`sending` 以及待核对的记录，保留到人工处置或明确的业务过期为止。

**CLI 会话日志：阶段 B 建映射表并持续写入，阶段 H 实现清理。** 同一个 Bot 逻辑会话可能对应多个消息轮次和多个原生 session，因此映射以“交互 + 请求 session”为单位保存，支持一对多。

- **启动前写入。** 在 `generateBotReply` 生成 `cliSessionID` 之后、调用 `runAssistantCLI` 启动进程之前，把 `config_id`、`project_id`、`user_id`、`conversation_id`、`bot_session_id`、`interaction_id`、`engine`、`requested_session_id` 原子写入 `bot_native_sessions`。写入失败时，本轮不启动 CLI，并在交互记录中写明追踪错误。
- **补全实际 session。** 通过已有的原生事件解析逻辑和 `runAssistantCLI` 的返回值，更新实际的 session ID；进入终态时补充完成时间。
  - 重试时恢复了同一个原生 session 的，复用原映射；实际新建了 session 的，新增一条映射。
  - 原生 session 尚未确认、或进程中断的映射，保留其追踪状态。
- **清理范围。** 保存服务端确定的存储范围和相对日志路径，按当前用户实际的 CLI HOME 解析，并验证归属。清理只处理同时满足以下条件的 Bot 原生日志：已终结、超过保留期、能由映射明确定位。逐文件记录清理结果和错误。
- **历史日志。** 升级前产生的原生日志，只为有可信记录、能精确关联的 session 生成历史映射，其余列入 `legacy_unmapped` 清单，等待人工核查。共享 HOME 下网页助手和其他来源的日志，按各自的归属处理。

阶段 B 的验证覆盖：启动前映射落库、映射写入失败、实际 session 与请求 ID 不同、同一 Bot 的多轮会话、崩溃记录。阶段 H 在临时目录构造混合来源的日志，验证只删除映射指向的到期 Bot 文件。

### 10.1 阶段 H 实现说明（与上文的差异）

- **清理范围包含企微已有数据。** 保留规则作用于 `bot_interactions`、`notification_deliveries` 和 `bot_pending_drafts` 的全部终态记录，不区分渠道，升级后首轮清理就会处理历史企微记录。手册第 2 节第 7 步要求先确认保留设置。
- **通知记录的完成时间。** `notification_deliveries` 没有完成时间列，用“创建时间、最后一次尝试时间、发送时间都早于期限”判断，最近重试过的记录不会被提前清理。
- **每轮批次上限。** 每类每轮最多 10 批（每批 1000 条），避免升级后积压数据导致首轮长时间占用数据库。
- **飞书、钉钉开放网页确认。** 第 9.1 节原定飞书、钉钉只读；2026-11-20 按验收负责人要求，飞书、钉钉也可以选“网页确认”，以便在单聊中创建需求、派发或启动 SOP。机制与企微相同，确认链接在纯文本中单独一行；“自动执行”仍只对企微开放。
- **原生日志按文件名匹配。** 映射没有在运行时记录日志路径；清理时按请求和报告的 session ID 在该用户的 CLI HOME 中查找（CodeBuddy、Claude Code、Codex），删除后把相对路径写回 `log_path`。其他 CLI 记为 `unsupported`，留给人工核查。

## 11. 测试与验收

### 11.1 自动化

| 层级 | 覆盖 |
| --- | --- |
| 适配器 | 官方 fixture、消息字段、应用或企业不匹配时拒绝、非文本与超长输入、回复 key、发送阶段分类、WebSocket/Stream 生命周期；钉钉自建循环在心跳超时、断线和关闭时不 panic |
| Repo | 原企微主键不变、跨 Bot 同名 ID、一次性绑定、多副本抢占、epoch、更新 0 行、删除事务、唯一索引冲突、`dbNowUS` 时间比较、锁顺序 |
| 服务 | 同一消息重复投递；发送前、发送中、发送后崩溃；生成结果只保存一次；`sending` 恢复；删除与迟到回执的竞态；停用后 `ready` 暂存与恢复；明确未送达重试耗尽；人工补发回到 `ready`；数据库时钟下的飞书窗口边界；钉钉路由到期；通知专用配置 |
| HTTP | Bot 越权访问、跨项目使用交互 ID、凭据校验失败、配置 CAS、企业确认 CAS、同源绑定、人工补发的权限与幂等、草稿确认 |
| 前端 | 渠道切换、Secret 状态、连接与发送能力显示、企业待确认与冲突、绑定确认、补发按钮、出错后保留输入 |
| 真实 CLI | 只读模式下的平台工具、外部 MCP、原生命令与文件写入约束；Bot 原生 session 映射与混合来源日志的清理；使用现有网关的契约测试 |

MySQL 测试验证并发更新和唯一索引，SQLite 测试覆盖公共业务。租约竞态测试使用可控的数据库时钟和发送屏障，复现以下时序：旧进程在 `sending` 之后暂停，新进程接管，旧进程再恢复。

开发使用仓库当前的 Go 版本：`go.mod` 要求 `1.26.11`。先跑改动涉及的包和相关前端测试，再按贡献要求和 `platform` CI 完成全量检查。 [贡献要求](../../CONTRIBUTING.zh-CN.md) · [平台 CI](../../.github/workflows/platform.yml)

```bash
# 在 IM 开发目录执行
go test -p=2 ./internal/botinteraction ./internal/notification ./internal/repo ./internal/httpapi -count=1 -timeout=600s
go vet ./...
npm --prefix webtest test
python3 scripts/check-public.py

# 构建嵌入的前端，再跑全量
bash scripts/build-chatbot.sh
go test -p=2 ./... -count=1 -timeout=600s

# 一次性本机 MySQL 测试库
make test-mysql

# SDK 探针（独立模块）
(cd scripts/im-sdk-probe && go test -count=1 ./... && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...)

# Linux CI 执行仓库提供的扫描与覆盖率流程
bash scripts/scan-secrets.sh
```

`scan-secrets.sh` 目前下载 Linux x64 二进制，在对应的 CI 环境中运行。覆盖率和真实浏览器测试沿用 `platform.yml` 的分组与门禁。依赖变更时，更新第三方来源声明，并执行 CI 的漏洞检查。

### 11.2 真实验收清单

| 编号 | 场景 | 通过标准 |
| --- | --- | --- |
| IM-01 | 新建和轮换 Bot | 渠道字段、凭据校验、企业确认状态、连接与发送能力都可见；失败时保留原配置 |
| IM-02 | 两阶段绑定 | 平台登录和原 IM 发送者都确认后才生效；确认码过期或跨 Bot 使用被拒绝 |
| IM-03 | 原会话查询 | 普通成员查询当前项目状态，在原会话中收到以正确身份执行的回复 |
| IM-04 | 重复入站 | 同一业务消息重复推送只产生一条交互，模型和业务只启动一轮 |
| IM-05 | 重启与易主 | `queued` 恢复执行；`ready` 只发送已保存的结果；`running` 中断后可核查；旧 owner 无法覆盖结果 |
| IM-06 | 发送结果未知 | 平台实际已接收而本地超时时，状态正确，并按渠道的幂等能力处理 |
| IM-07 | 只读与撤权 | 写工具被阻止；解绑和项目权限撤销对下一次工具执行生效 |
| IM-08 | 企微回归 | 旧机器人、旧绑定、群聊、流式进度、卡片与主动通知全部通过；备用发送的变化按下方专项验收 |
| IM-09 | 钉钉回复到期 | 授权过期后记录原因，已生成的结果可查看，重发按钮按路由有效期处理；阶段 G 后可显式改用主动单聊补发 |
| IM-10 | 主动通知 | 三个渠道都收到 `succeeded`、`failed`、`waiting` 通知，过时的等待通知被取消 |
| IM-11 | 人工补发 | `reply_unknown`、`reply_failed` 原子变回 `ready`，generation 加一且 key 更新；沿用已保存的业务结果，记录操作者，相同 action key 去重 |
| IM-12 | 数据升级和回退 | 旧记录可读、密钥可轮换、迁移可重入；维护窗口按“先停旧副本、再以关闭 worker 的配置启动新版本”执行；阶段 B 起新增的 CLI 会话都有映射；历史未映射清单保留；按版本与数据能力执行回退 |

**IM-08 备用发送专项**，单独保存证据：

1. 传输前已确认未发送，或平台明确拒绝且该错误允许走备用路径时：验证主动发送最多补发一次，并记录原路径和备用路径的结果。
2. 流式回复已被平台接收、但本地回执超时时：验证记录进入 `reply_unknown`，平台发送调用只计一次，页面明确提示“送达结果待核对”。
3. 在上一场景中模拟用户实际未收到：验证管理员能从记录页查看已保存的回复，并可确认或人工再次发送。再次发送使用新的 generation，并提示可能重复。
4. 发送中删除 Bot：验证 `sending → reply_unknown`，旧 worker 迟到的结果无法覆盖；项目管理员可以核对，补发操作返回 `bot_deleted`。
5. 发送中停用 Bot：验证 `sending → reply_unknown`，`ready` 状态的记录保留为暂存，重新启用后路由有效的正常发送。

**飞书窗口专项**：用可控的数据库时钟模拟以下情况——首次发送后 54 分 59 秒、55 分钟、实例重启、两个副本的本地墙钟存在偏差。检查首次发送时间不被重置；已达上限，或剩余时间不足一次请求的超时时，转为待核对。

每个平台保存一组测试企业、测试项目和普通成员的脱敏证据，记录配置版本、消息 ID、交互 ID、平台回执、实际结果和测试时间。报告使用“通过 / 失败 / 未运行 / 阻塞”四种状态。

## 12. 上线执行与交接

**上线说明与 `CHANGELOG.md` 需写明三处用户可感知的变化：**

1. **企微备用发送。** 原行为是流式失败后主动再发一次；新行为是只对确认未送达的错误执行备用发送，送达结果未知的显示“待核对”。用户可能需要到记录页查看已保存的回复，再选择人工补发。发布说明附 IM-08 专项结果、记录页入口和客服处理步骤。
2. **新建企微 Bot 默认只读。** 新建的企微 Bot 默认 `writeApproval=readonly`，不再自动执行写操作；已有企微 Bot 保持原有策略。需要写操作的项目，由管理员在 Bot 配置中显式选择（阶段 G 起可选 `confirm`）。
3. **停用 Bot 的处理。** 停用时取消正在执行的任务；已生成的回复暂存，重新启用后发送；发送中的记录进入待核对。

**试点顺序。** 依次启用：企微兼容版本 → 一个项目的飞书 → 一个项目的钉钉 → 主动通知。迁移按第 5.4 节执行，所有副本同步配置密钥和新租约协议。公开地址沿用 `AGENTFLOW_PUBLIC_URL`，确保用户能从 IM 打开绑定页和运行页。

```dotenv
AGENTFLOW_PUBLIC_URL=https://loopforge.example.com
AGENTFLOW_CREDENTIAL_KEYS=<current-kid>:<base64-encoded-32-byte-key>
AGENTFLOW_BOT_WORKERS_ENABLED=true
AGENTFLOW_BOT_MESSAGE_RETENTION_DAYS=90
AGENTFLOW_BOT_METADATA_RETENTION_DAYS=180
```

**运维与可观测：**

- 机器人的 App ID、Secret、企业范围由管理页面保存。
- 日志按项目、渠道、内部 Bot ID、交互或投递 ID 关联，记录安全错误码、发送阶段和平台回执。
- 建立以下可观测指标：连接异常、最老的 `queued` 消息、`ready` 积压、发送结果未知、凭据失败。

**交接物：** 本方案、`docs/im-channels.md`、API 文档和 OpenAPI、手动迁移脚本、配置样例、第三方依赖说明、SDK 探针、测试结果和真实渠道的验收记录。最终 PR 按 IM-01 至 IM-12 逐项列出状态，并保留各阶段提交供审阅。

**待核实的外部条件：**

| 条件 | 核实动作 | 对应阶段 | 状态 |
| --- | --- | --- | --- |
| SDK 版本和关闭/确认接口 | 用固定 tag 编译探针，核对源码和实际行为 | A | 已完成。飞书可用；钉钉连接循环不可用，改为只用部分 SDK（第 8.3 节）；复现步骤和调用栈见 `scripts/im-sdk-probe/README.md` |
| 飞书企业信息接口 | 核对官方文档和固定版本 SDK 的模型 | A | 已完成。接口与字段已确认，所需权限在应用后台核实 |
| 飞书企业范围与权限 | 准备一个自建应用，完成可信事件发现、管理员确认 tenant key，再做单聊收发 | E | 代码完成；最小权限清单见 `docs/im-channels.md` 4.1，真实租户待验 |
| 钉钉企业标识和机器人字段 | 保存脱敏的原始事件，确认 CorpId、应用和发送者之间的对应关系 | F | 待准备 |
| 钉钉主动发送权限 | 用实际的机器人 API 完成一次本企业单聊通知 | G | 待准备 |
| 企微历史绑定归属 | 跑只读 SQL 清单，核对重复 Bot、孤立绑定和现有通知配置 | B、上线前 | 待执行 |
| 目标基线变化 | 合并前对照目标分支复核本文列出的文件和迁移编号 | 每阶段 | 已核对 `e89cda9` |

本方案以三渠道接入为目标，飞书先行试点，新增渠道首轮只读。各阶段的实现和真实租户的验收结果，由本分支后续提交补齐。
