# 企业 IM 接入实施方案（单分支开发）

状态：实施方案第 2 版（2026-11-17）。产品范围、验收条件和评审结论以 [enterprise-im-integration.md](enterprise-im-integration.md)（评审稿第 3 版，下称“方案”）为准；本文只说明代码怎么改、按什么顺序改、怎么验证。代码现状核对基于 `main@69e4bf0`。

第 2 版相对第 1 版的变化：

- 交付顺序改为“第一轮：企微回归基线 + 飞书私聊跑通”，让真实 IM 消息尽早进来。凭据批量迁移、企微发送重构、数据清理、助手写审批和主动通知往后挪。
- 迁移脚本：不再手写补列 DDL。列由现有的增量 DDL 补齐，手工 SQL 只做前置检查、查重和回填，并在 MySQL 8.4 上演练升级（§2.2）。
- 企微旧绑定归属：同一个外部 Bot ID 对应多个配置时，不再按“当前启用”推断归属，改为列出来人工确认，必要时让用户重新绑定（§2.3）。
- 换主与投递：补充“请求已发出、还没收到回执时换主”的处理和测试；写清新实例怎样领取 `ready` 和到期可重试的 `sending`；企微 `WriteJSON` 报错一律按“结果不确定”处理（§3）。

## 0. 开发约定

| 事项 | 约定 |
| --- | --- |
| 分支 | 本地分支 `feat/enterprise-im`，基于 `main@69e4bf0`，暂不推送 |
| 交付轮次 | 第一轮（R1）企微回归基线 + 飞书私聊；第二轮（R2）钉钉复用同一条链路；第三轮（R3）企微发送重构、存量迁移与清理；第四轮（R4）助手与主动通知。每轮按步骤提交，每个提交都保证测试通过 |
| 与需求的关系 | 需求 #1–#4 是按旧的 M1–M4 拆分的。R1 横跨 #1 和 #2。等这一版方案确认后，再提交需求描述的修改草稿，把 #1–#4 对齐到 R1–R4 |
| 迁移 | 遵守 `AGENTS.md`。新列和索引由 gorm 增量 DDL 补齐（`internal/repo/automigrate.go`）；手工 SQL 只做检查和回填，由运维执行。R1 不需要执行任何手工 SQL |
| 依赖 | `github.com/larksuite/oapi-sdk-go/v3` v3.13.0（R1），`github.com/open-dingtalk/dingtalk-stream-sdk-go` v0.9.1（R2） |
| 环境 | 工作区已装好 Go 1.26.11，`chatbot` 前端已构建（`internal/httpapi` 编译需要 `web/native-chat`） |
| 兼容底线 | 企微存量 Bot 的外部 Bot ID、绑定键、去重键、租约键、审批行为和发送行为在 R1 全部保持不变 |

### 0.1 基线测试结果（未改代码时）

| 包 | 结果 |
| --- | --- |
| `internal/botinteraction` | 通过 |
| `internal/notification` | 通过 |
| `internal/repo` | 通过 |
| `internal/httpapi` | 只有 `TestInstallStatusSurvivesAReload` 失败。这条测试检查 CLI 安装状态，与 Bot 无关，失败原因是本工作区已经装有 `dsh`；Bot、通知、绑定相关的用例全部通过。之后每次对比都以这份结果为基线 |

## 1. 第一轮（R1）：企微回归基线 + 飞书私聊跑通

### 1.1 第一轮的验收结果

R1 只看一个结果：

> 一个真实的飞书用户，用 LoopForge 现有的网页登录绑定自己已有的账号，在私聊里发「项目状态」，在原会话收到这个项目的状态；同一条消息被重复投递时只执行一次；同时，现有企微的回归全部通过。

「项目状态」是一条**固定的只读查询**，由服务端直接生成回复，不调用模型，也不依赖 SSO：本地登录或 OIDC 都可以完成绑码。

### 1.2 步骤

| 步骤 | 内容 | 提交后的状态 |
| --- | --- | --- |
| R1-1 企微基线 | 先补企微的特征测试，把现状固定下来：<br>• `BotMessageID`、绑定 ID、租约键的取值<br>• 私聊流式回复，以及流式失败后改用 `send` 的现有行为<br>• 群聊会话 ID<br>• 卡片发送和卡片回调路由<br>• 通知卡片的入队<br>同时整理一份真实环境回归清单（私聊、群聊 @、交互卡片、主动通知、重启恢复），在改代码前和 R1 完成后各跑一次 | 只加测试，不改代码 |
| R1-2 最小接口 | 新增 `channel.go`，只定义飞书需要的接口（见 1.3），企微的现有代码包装成 `wecomChannel`：<br>• `run`、`receive`、`process` 里的协议细节搬进 `channel_wecom.go`<br>• 流式进度和卡片作为企微的可选能力，用类型断言取得 | 现有测试和 R1-1 的特征测试**一行不改**全部通过 |
| R1-3 数据列 | 按 §2.1 新增列，由增量 DDL 补齐。代码里 `channel` 为空就按企微处理，企微的 `receive_slot` 暂时留空，旧的加锁扫描逻辑保留 | 存量数据不用回填就能正常运行 |
| R1-4 凭据加密 | 新增 `internal/credcrypt` 包（§2.4）：<br>• 三个渠道新建或轮换凭据，都要求部署配置了主密钥<br>• 存量企微的明文凭据照常读取，Bot 管理页提示“未加密”<br>• 批量 `reencrypt` 命令放到 R3 | 企微旧凭据照常连接 |
| R1-5 租约版本与领取规则 | 实现 §3 的 epoch、带版本条件的写回、领取规则和恢复规则。企微仍走现有发送路径，只是写回时多带版本条件；飞书使用两段式发送 | 换主相关测试通过 |
| R1-6 绑定 | `IdentityScope` 按渠道区分：企微不变，飞书使用 `内部配置 ID + open_id`。<br>• 挑战记录渠道、`config_id`、脱敏后的发送者<br>• 确认页显示渠道、Bot、项目、脱敏发送者和授权说明（方案 §5.5）<br>• 飞书启用绑定引导限频<br>• 企微是否同时启用限频，见 §6 第 2 条 | 飞书绑码流程可以端到端走通 |
| R1-7 飞书适配器 | 实现 Verify、Connect、事件映射、群消息忽略、非文本和超长消息提示（含限频）、按 `内部配置 ID + message_id` 去重、带 `uuid` 回复原消息（§1.4） | 模拟平台下的适配器测试通过 |
| R1-8 固定只读查询 | 实现「项目状态」「帮助」和绑定命令（§1.5）。权限沿用 `botExecutionUser` 的现有规则 | 服务测试通过 |
| R1-9 管理页 | `web/app/bots.js` 增加渠道选择、飞书的 App ID / App Secret 表单、识别出的企业名称和连接状态。“编辑时不能修改外部应用 ID”同时对企微生效，因为它只拦截改 ID，不影响其他编辑 | 页面可以创建飞书 Bot |
| R1-10 真实环境验收 | 用一个真实飞书自建应用、一个试点项目和一个非管理员账号，跑 §1.1 的验收；再跑一遍企微回归清单；最后在 MySQL 8.4 上做升级演练（§2.2） | R1 收口 |

### 1.3 第一轮的最小接口

```go
type Channel interface {
    Kind() ChannelKind
    Verify(ctx context.Context, bot domain.NotificationBot, cred Credential) (TenantInfo, error)
    Connect(ctx context.Context, bot domain.NotificationBot, cred Credential, sink InboundSink) (Conn, error)
}
type Conn interface {
    Done() <-chan struct{}
    Reply(ctx context.Context, msg OutboundMessage) SendResult // msg 中包含 ReplyKey，飞书用作 uuid
    Close()
}
type InboundSink func(ctx context.Context, ev InboundEvent) error // 返回 error 时，适配器按协议报告失败
type SendResult struct { Outcome SendOutcome; Code int; Detail string } // Delivered / NotDelivered / Rejected / Unknown
```

企微的流式进度（`ProgressConn`）和卡片（`CardConn`）是可选接口。主动发送（`PushConn`）在 R4 再加，R1 不定义。

### 1.4 飞书适配器要点

| 环节 | 实现 |
| --- | --- |
| Verify | 用 `tenant_access_token/internal` 换取令牌（同时校验凭据），再调用 `GET /open-apis/tenant/v2/tenant/query` 取得 `tenant_key` 和企业名称。R1-10 的第一项验证：这个接口取到的 `tenant_key` 是否和事件头里的一致。如果取不到，就改成用第一条合法事件的 `tenant_key` 固定下来 |
| Connect | 使用 `ws.NewClient(appID, secret, ws.WithEventHandler(dispatcher), ws.WithOnReady/OnDisconnected/OnReconnecting(...))` 和 `Start(ctx)`，SDK 的连接状态回调接到 `RenewBotConnection` 的 state |
| 入站确认 | 已核对 SDK v3.13.0 源码（`ws/client_message.go`）：事件处理函数返回 error 时，SDK 回 500，否则回 200。处理函数只调用 `sink`（一次数据库事务），保证在 3 秒内返回。入库失败时返回 error，让平台重新投递；已经落库的重复事件直接返回成功 |
| 事件映射 | • `header.app_id`、`header.tenant_key`：交给公共校验，不一致就丢弃，并计一次鉴权失败<br>• `event_id` 存入 `platform_event_id`<br>• `message.chat_type`：`p2p` 对应 `single`，`group` 对应 `group`<br>• 发送者：`sender.sender_id.open_id`<br>• `message_type` 和 `content`：文本消息解析其中 JSON 的 `text` 字段 |
| 消息边界 | 群消息：确认后丢弃，不写入交互表，只计数。私聊的非文本或超过 16000 字的消息：写入一条 `ignored_*` 交互，只记类型或长度，回复一次固定提示，10 分钟内最多提示一次（限频用会话行 `last_notice_at` 的条件 UPDATE 实现） |
| 回复 | 调用 `im.v1.message.reply`，参数为 `message_id`、`msg_type=text`、`uuid=reply_key`，其中 `reply_key = "lf" + hex(sha256(交互 ID))[:40]`，第一次发送前生成，之后一直不变 |
| 发送结果 | • 建连失败、请求没有写出：`NotDelivered`<br>• 限流错误码：`NotDelivered`，允许重试<br>• 鉴权或权限错误码：`Rejected`<br>• 请求已写出但读取响应超时：`Unknown`<br>四类结果的处理见 §3.3 |

### 1.5 固定只读查询

| 用户发送 | 回复 |
| --- | --- |
| `项目状态` | 由服务端生成，分四部分：<br>① 项目名称和链接<br>② 需求按状态计数（backlog / todo / in_progress / in_review / blocked）<br>③ 进行中的运行，最多 5 条，每条包含需求标题、当前节点、状态和运行链接<br>④ 最近 24 小时失败的运行，最多 3 条<br>数据来源为 `IssuesRepo.Issues()` 和 `RunsRepo.RunSummariesFor(filter{ProjectID})`，都按 Bot 所属项目过滤 |
| `帮助` | 列出当前支持的命令 |
| `绑定账号` / `解除绑定` / `/bind <码>` | 沿用现有的绑定流程 |
| 其他内容 | 回复“首版只支持：项目状态、帮助、绑定账号、解除绑定”。不调用模型 |

每次执行前都会重新校验：Bot 处于启用状态、发送者已绑定、绑定的账号存在，并且对该项目有执行权限。权限规则沿用 `botExecutionUser`：项目 owner、平台管理员，或角色不是 viewer 的成员。只读查询要不要对 viewer 放开，见 §6 第 3 条。

这条查询独立于模型：飞书 Bot 在 R1 根本不会调用 `Generate`，所以不需要配置 CodeBuddy Key，也不会经过工具网关。

## 2. 数据与迁移

### 2.1 R1 新增的列（gorm tag 声明，启动时由增量 DDL 补齐）

| 表 | 新增列 |
| --- | --- |
| `notification_bots` | `channel` varchar(16)；`external_app_id` varchar(191)；`tenant_key` varchar(191)；`receive_slot` varchar(208) NULL，带唯一索引。<br>payload JSON 同步增加 `channel`、`tenantKey`、`tenantName`、`credentialVersion`、`credentialEncrypted` |
| `bot_connections` | `epoch` bigint default 0；`takeovers`、`auth_failures`、`ignored_group` bigint；`last_event_at`；`last_auth_error` varchar(255) |
| `bot_interactions` | `lease_owner` varchar(64)；`lease_epoch` bigint；`reply_key` varchar(64)；`send_started_at`；`send_attempts` int；`next_send_at`；`msg_type` varchar(32)；`content_length` int；`platform_event_id` varchar(191) |
| `bot_conversations` | `last_notice_at`；`guide_window_at`；`guide_count` int |
| `bot_user_bindings` | `config_id` varchar(191)，带索引 |
| `bot_binding_challenges` | `config_id`；`channel`；`sender_mask` varchar(64) |

R1 不新增表。钉钉的 `reply_route` 和 `route_expires_at` 放到 R2，`bot_pending_drafts` 放到 R4。

**唯一索引的安全性**：`automigrate.go` 建索引失败会直接返回错误，导致服务启动失败。`receive_slot` 是新补的列，补完时全部为 NULL，MySQL 唯一索引允许多个 NULL，所以建索引不会失败。R1 只有飞书的新配置会写这个列。

### 2.2 手工 SQL 与 MySQL 8.4 演练

- **不手写补列 DDL**。LoopForge 的 Compose 使用 `mysql:8.4`，而 MySQL 不支持 `ADD COLUMN IF NOT EXISTS`。列一律交给增量 DDL 补齐。如果以后确实需要手工补列，按仓库已有写法（`020_conversation_workspaces.sql`、`021_*`、`032_*`）：先查 `information_schema.COLUMNS`，再用 `PREPARE` 执行。
- **R1 不需要手工 SQL**：`channel` 为空按企微处理，企微不依赖 `receive_slot`。
- **R3 的 `034_enterprise_im_backfill.sql`**：
  1. 前置检查：查询 `information_schema.COLUMNS`，确认 `channel`、`external_app_id`、`receive_slot`、`config_id` 等列都已存在。缺列时用 `SIGNAL SQLSTATE '45000'` 中止，并提示“请先用新版本启动一次 API 补齐列”。
  2. 查重：列出启用了对话、且外部 Bot ID 重复的配置。如果有重复，就中止并输出清单。
  3. 回填：`channel='wecom'`、`external_app_id`，以及启用了对话的行的 `receive_slot`。
- **MySQL 8.4 升级演练**（R1-10 做第一次，R3 再做一次）：
  1. 用 `compose.yaml` 中的 `mysql:8.4`，导入一份带存量企微数据的备份。
  2. 先启动旧版本，再换新版本启动，确认增量 DDL 补齐了全部新列和唯一索引、启动没有报错、企微连接正常。
  3. R3 时再执行 034，验证前置检查、查重、回填三条路径：列缺失、存在重复、正常。

  工作区如果起不了 MySQL 8.4，就在测试环境由运维执行，我提供脚本和检查清单。

### 2.3 企微旧绑定归属（R3 执行，R1 只建列）

旧绑定只记录了外部 Bot ID。`035_bot_binding_ownership.sql` 只做分类和输出，不自动归属：

| 分类 | 判定 | 处理 |
| --- | --- | --- |
| 明确 | 这个外部 Bot ID 在 `notification_bots` 中只对应一个配置 | 脚本里给出回填 `config_id` 的 UPDATE，运维确认后执行 |
| 有歧义 | 对应多个配置，包括跨项目、仅用于通知的配置 | **不推断归属**，输出清单（配置、项目、绑定数），交给人工确认。确认归属后手工回填；确认不了的，撤销绑定，让用户在目标 Bot 上重新绑定。被撤销的用户下次发消息时会自动收到绑定引导 |
| 孤立 | 没有对应的配置 | 列出来，确认后撤销 |

在归属确认完成之前，删除企微 Bot 时不撤销 `config_id` 为空的旧绑定；它们只在后续核查中处理。

### 2.4 凭据加密（R1）

- `internal/credcrypt.Keyring` 从 `AGENTFLOW_CREDENTIAL_KEYS` 读取密钥，格式为 `kid:base64(32 字节)`，多个用逗号分隔。加密用 AES-256-GCM，存储格式为 `lf1:<kid>:<base64(nonce‖密文)>`，附加认证数据（AAD）为 `内部配置 ID + 渠道`。
- Keyring 在 `main` 中解析，注入 `UsersRepo` 的一个字段。按 `AGENTS.md` 的约定，它不放进 `deps`。
- 凭据内容统一为 JSON：`{"secret":"..."}`。没有 `lf1:` 前缀的旧值按明文读取。
- 没有配置主密钥时，三个渠道的新建和轮换都返回 400。存量企微 Bot 的连接不受影响。
- 保存新 Secret 之前，先调用 `Channel.Verify`。校验失败就保留原凭据和原连接。重连判断改用“渠道 + 外部应用 ID + 凭据版本”组成的指纹，不再看 `UpdatedAt`，所以只改名称不会再触发重连。

## 3. 租约版本、领取规则和换主时的投递

### 3.1 写回加版本条件（R1）

- `ClaimBotConnection` 换主成功时，`epoch` 和 `takeovers` 各加一，并返回新的 epoch。同一 owner 续约时 epoch 不变。
- `SaveBotReply`、`MarkBotReplySending`（新增）、`MarkBotReplySent`、`FailBotReply`、`MarkBotReplyUnknown`（新增）、`SetBotCardTaskID` 这几个写回操作，统一加条件：`lease_owner=? AND lease_epoch=?`，并要求 `bot_connections` 中的 owner、epoch 仍是自己，且 `lease_until > now`。影响行数不等于 1 时返回 `ErrLeaseLost`，处理循环收到后立即退出。
- 现有 `SaveBotReply` 影响 0 行时也返回成功，这个问题一并修正。

### 3.2 新实例的领取规则

1. **接管时恢复**（`RecoverBotInteractions(configID, epoch)`）。只处理 `lease_epoch` 小于当前 epoch 的行；旧行的 `lease_epoch` 为 NULL，也算在内。

   | 原状态 | 处理 |
   | --- | --- |
   | `running` | 改为 `ready`，并写入中断提示（沿用现有文案） |
   | `sending`，飞书，当前时间早于 `send_started_at + 55 分钟` | 改为 `ready`，**保留** `reply_key` 和 `send_started_at`，让新实例用同一个 uuid 重试 |
   | `sending`，飞书已超过 55 分钟，或企微、钉钉 | 改为 `reply_unknown`（待核对），不自动重发 |
   | `queued`、`ready` | 不改状态，等领取时再写入新属主 |

2. **领取**（`ClaimBotInteraction`）：
   - 在持有租约的事务里，选 `status IN ('queued','ready') AND (next_send_at IS NULL OR next_send_at <= now)`，按 `created_at, id` 排序取第一条。
   - **无论 `queued` 还是 `ready`**，都写入 `lease_owner` 和 `lease_epoch`。现有代码遇到 `ready` 直接返回、不更新属主，这里要修正。
3. **发送**：
   - 发送前把状态从 `ready` 改为 `sending`，带版本条件。`reply_key` 为空时才生成，已有就沿用。`send_started_at` 只在第一次发送时写入。
   - 发送前再检查一次本地租约余量：最近一次续约成功时间 + 45 秒 − 10 秒。余量不足就不发，留给新属主处理。

### 3.3 发送结果的处理

| 结果 | 飞书（R1） | 企微 | 钉钉（R2） |
| --- | --- | --- | --- |
| `Delivered` | `replied` | `replied` | `replied` |
| `Rejected` | `reply_failed` | `reply_failed`（现有行为） | `reply_failed` |
| `NotDelivered` | 回到 `ready`，`next_send_at` 依次退避 5 秒、30 秒、2 分钟，第 3 次仍失败则记为 `reply_failed` | R1 保持现状；R3 改为同样的退避 | 同飞书，另外每次重试前检查 Webhook 是否已过期 |
| `Unknown` | 保持 `sending`，用同一个 uuid 重试，直到 55 分钟 | R1 保持现状（见下）；R3 改为 `reply_unknown` | `reply_unknown` |

**企微 `WriteJSON` 一律保守处理（R3 实施）。** gorilla 的 `WriteJSON` 在刷出缓冲时报错，数据可能已经部分写到了连接上，无法确定平台有没有收到。所以只有一种情况算 `NotDelivered`：调用 `WriteJSON` **之前**就已经确认 `socket.done` 关闭或 `ctx` 已取消。`WriteJSON` 返回的任何错误，以及等待回执超时、等待期间连接中断，都算 `Unknown`。

**企微现状的风险，R1 不改，R3 修复。** 现在发送出现非业务错误时，`process` 直接返回，状态留在 `ready`，重连后会自动再发一次；流式回复失败后也会立即改用 `send` 再发一次。这两处都可能让用户收到重复消息。R1 承诺企微发送行为不变，所以只用特征测试把现状固定下来，R3 和三类结果分类一起改。

### 3.4 换主时的投递测试（R1，使用模拟渠道和可注入时钟）

| 场景 | 步骤 | 断言 |
| --- | --- | --- |
| 飞书：已发出、未收到回执时换主 | 旧实例完成 `MarkBotReplySending`，模拟平台收下请求但不返回回执。时钟推进 46 秒后，新实例接管（epoch+1）、恢复、领取，并用同一个 uuid 重试。之后旧实例收到回执，尝试 `MarkBotReplySent` | 模拟平台按 uuid 去重后，用户只看到 1 条消息；该行由新实例写成 `replied`；旧实例写回得到 `ErrLeaseLost`，没有再发送 |
| 企微：同样的场景 | 用模拟的企微连接，其余同上 | 平台只收到 1 次请求；该行变为 `reply_unknown`；新实例没有自动重发 |
| 换主后领取 `ready` | 旧实例已把结果写成 `ready`，还没发送就失去租约 | 新实例领取后写入新属主并发送；旧实例无法再把它改成 `sending` |
| 到期的 `NotDelivered` | 飞书返回 `NotDelivered`，`next_send_at` 设为 5 秒后 | 5 秒内领不到这一行，5 秒后能领到，`reply_key` 保持不变 |
| 旧实例暂停后恢复 | 旧实例在 `running` 时暂停超过 45 秒 | 新实例写入中断提示；旧实例恢复后 `SaveBotReply` 得到 `ErrLeaseLost` |

## 4. 后续轮次

| 轮次 | 内容 | 对应原需求 |
| --- | --- | --- |
| R2 钉钉 | • Stream 接收：先核对 SDK 收到处理函数返回的 error 后怎样回复平台<br>• 用 `sessionWebhook` 回复原会话；Webhook 加密保存在 `reply_route`，并检查过期，过期时记录原因<br>• 校验 CorpId 与 `chatbotCorpId` 是否一致：先在真实应用上确认 `chatbotCorpId` 是不是明文<br>• 复用同一套固定只读查询和绑定流程<br>• 编写三个渠道的配置文档 | #3 |
| R3 企微发送重构、存量迁移与清理 | • 企微发送结果三类分类和退避重试；流式回复失败后，只有 `NotDelivered` 或 `Rejected` 才改用 `send`<br>• 人工处理：“确认已送达”和“重发已保存的回复”<br>• `-credential-action status\|reencrypt`<br>• 034 回填、035 归属核查<br>• 禁止原地修改企微 Bot ID；删除 Bot 时清理关联数据（规则见 §2.3）<br>• 保留期自动清理、核查 CLI 原生会话日志<br>• 第二次 MySQL 8.4 演练，补齐升级说明 | #1 |
| R4 助手与主动通知 | • 飞书、钉钉接入只读助手（`ReadOnly` 执行模式：网关拦截写入工具、不注入外部 MCP、丢弃草稿）<br>• 企微增加写操作审批开关，网页确认链接（`bot_pending_drafts` 加上复用 `resolveAssistantDrafts`）<br>• 防提示注入的分隔标记<br>• 身份撤销后作废待确认草稿<br>• 「待人工处理」和「失败」通知的主动发送<br>• CLI 日志清理 | #4 |

## 5. 测试要求

- 每个提交跑四个包：`internal/botinteraction`、`internal/notification`、`internal/repo`、`internal/httpapi`，结果和 §0.1 的基线对比；再跑 `go vet ./...`、`scripts/check-public.py`、`scripts/scan-secrets.sh`。
- 飞书适配器测试：用官方事件样例 JSON 做 fixture，用 `httptest.Server` 模拟开放平台，覆盖映射、应用和租户校验、入站确认码、四类发送结果和 uuid 是否透传。
- 服务测试（模拟渠道 + SQLite `OpenTest`）：
  - 跨 Bot 的同名 `open_id`
  - 重复投递只执行一次
  - 群消息被忽略
  - 非文本和超长消息的提示限频
  - 绑定引导限频
  - 一次性绑码
  - 解绑或权限下调后，「项目状态」被拒绝
  - §3.4 的全部换主场景
- 企微：R1-1 的特征测试在 R1 全程都不允许修改。

## 6. 已确认事项（2026-11-17）

1. 需求 #1 至 #4 保留原编号，范围和验收条件改为按 R1 至 R4 对齐，先提交描述修改的草稿。
2. R1 只对飞书启用绑定引导限频，企微沿用现有行为做回归。企微的限频放到 R3，届时单独验收三项：首次绑定、链接过期、重新发起。
3. 「项目状态」沿用现有权限规则，暂不对 viewer 开放。真实验收分两步：先用有执行权限的普通项目成员走成功路径，再用 viewer 验证会被拒绝。
4. 环境由需求方准备：飞书测试应用、试点项目、企微测试环境。MySQL 8.4 升级演练使用脱敏后的存量库副本。
5. R1 按 §1.1 约定收口，R1 验收通过后再启动 R2。

## 7. 企微回归清单（R1 改代码前后各跑一次）

自动化部分是 R1-1 的特征测试：`internal/botinteraction/wecom_baseline_test.go` 和 `internal/repo/bot_keys_baseline_test.go`。R1 期间不修改其中的断言。

真实环境部分在企微测试环境逐项执行，并记录结果：

| # | 场景 | 预期 |
| --- | --- | --- |
| 1 | 已绑定的成员私聊发起提问 | 先显示“已收到，正在处理…”，随后在同一个气泡里出现最终回复 |
| 2 | 群聊里 @Bot 提问 | 群内收到回复，会话与私聊分开 |
| 3 | 发送 `/new` | 回复“已开启新会话”，下一条提问不带上之前的历史 |
| 4 | 未绑定成员发消息 | 收到绑定链接；完成网页确认并回填 `/bind` 码后绑定成功（企微不限频，行为与现在一致） |
| 5 | 发送 `/unbind` | 解除绑定，下一条业务消息重新要求绑定 |
| 6 | SOP 节点进入待人工处理 | 目标会话收到通知卡片；点击按钮后操作执行，卡片更新为处理结果 |
| 7 | SOP 失败通知（目标会话不开启对话的配置） | 通过 `wecom-cli` 发出文本通知，投递记录显示“已发送” |
| 8 | 重启 API 实例 | 连接自动恢复为“已连接”；重启前排队的消息继续处理 |
| 9 | Bot 管理页编辑名称、切换模型 | 保存成功。现有实现会因 `UpdatedAt` 变化短暂重连，3 秒巡检后恢复“已连接”；R1-4 改为按凭据指纹判断后，只改名称不再重连 |
