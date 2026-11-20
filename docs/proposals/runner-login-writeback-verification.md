# Runner 登录凭证写回：前期验证记录

验证日期：2026-11-17。基于 `0dabc555`（Antigravity 登录支持），独立分支
`feature/runner-login-writeback-verification`。这一阶段只有测试与实验记录，未把原型验证器接入
Runner。后续正式实现位于 `feature/runner-managed-auth`，见[实现说明](../runner-login-refresh.md)。
补充验证中，经用户明确授权，使用本机 Antigravity 登录
真实刷新并保留最新凭证；未读取、刷新或修改本机 Codex / Claude Code 的真实登录。

## 结论

**身份校验能阻止换号，但“cleanup 再刷新一次以确认 refresh token 的归属”尚不适合直接作为三种 CLI 的通用实现。**

在模拟认证服务的契约下，原型保留正常轮换和后续刷新，能拒绝整套换号、伪造本地身份、
只换 refresh token、只换 access token、同工作区不同用户、同用户不同组织。
这些结果证明的是算法在明确假设下的行为，不是真实供应商兼容性认证。

同时复现了两个会影响正常续期的边界：

1. 服务端已经轮换 refresh token，但响应在客户端收到之前丢失。隔离保存的仍是已消耗的
   旧 token，无法恢复客户端从未收到的新 token；直接重试还可能触发 token 重用检测。
2. 两个沙箱或宿主机 CLI 使用同一份一次性 refresh token。第二次使用可以导致认证服务
   撤销整个 token family。宿主机文件的内容比较与原子 rename 不能撤销服务端的变化。

因此，先前“遇到网络错误就隔离并重试，可以避免丢失轮换凭证”的说法需要收窄：
**只有已经收到、并持久化了新凭证，之后的身份查询失败，才验证了可在重启后安全恢复。**

### Codex 三方同时刷新的具体解释

上面的严格一次性 token / family 撤销模拟，不能直接等同于 Codex 服务端的实际策略。
[OpenAI 维护者于 2026-02-01 的解释](https://github.com/openai/codex/issues/10332)指出，
其刷新服务允许旧 refresh token 在有限宽限期内多次使用，当时描述为约一小时量级。
这是维护者对当时实现的说明，不是当前时长或所有轮换细节的稳定契约；本次未用真实账号验证。

两个 SOP 与宿主机同时从同一份原凭证刷新，在该宽限机制下可以各自成功，不能断言只有
一个成功、另两个必定退出登录。当前 LoopForge 给 SOP 的是各自私有 HOME 内的文件副本，
不是三方共享同一个宿主机认证文件。若宿主机先保存新凭证，两个 SOP 后续 cleanup 会发现
宿主机已经不同于最初快照并跳过写回；SOP 不会因此自动收到宿主机的最新凭证。

仍需单独验证的正常路径是：沙箱早已复制了旧凭证，宿主机已刷新，沙箱却在宽限结束后才
首次用旧 refresh token 刷新。独立 HOME 无法通过重新读取自己的文件获得宿主机更新，
当前 cleanup 的内容比较也不能协调运行中的刷新。不能仅增加 cleanup 锁就声称解决了
两个 SOP 与宿主机 CLI 之间的刷新协调。

## 第一轮模拟测试边界

- 全部凭证由测试生成，只有 `example.test`、`fake-*` 等虚构值。
- 当前实现的验证使用真实的 `prepareConversationRunnerLogin` 和 cleanup，但覆盖 HOME、
  CODEX_HOME、CLAUDE_CONFIG_DIR、Claude OAuth 环境变量，并让 Antigravity 明确使用临时文件。
  不读取真实凭证、不访问钥匙串、不触发真实续期。
- 协议原型仅连接 `httptest.NewTLSServer` 创建的本机服务，拒绝重定向。
- `/profile` 是测试假定存在的可信身份接口，并非声明三家都有相同的公开接口。
- 轮换、保持原 refresh token 且响应省略该字段、重用导致 family 撤销，都是分别模拟的
  服务端策略；不代表每家目前都实施全部策略。
- 第一轮没有真实 Claude Code 订阅测试，没有对当前 Codex 账号操作。Antigravity 真实
  续期的补充结果见下节。没有用模拟结果替代对话或 SOP native/Docker 的真实路径结果。

## 第一轮执行结果

最终合并执行 **107 个叶子场景**，全部通过，开启 `-race`，无跳过、无数据竞争报告。

| 场景 | 数量 | 结果含义 |
| --- | ---: | --- |
| 当前三种 CLI 的真实 cleanup 实现 | 12 | 复现正常写回及现有换号校验缺口，并非修复 |
| 三种文件格式 × 两类刷新策略 × 身份/续期场景 | 54 | 原型允许正常续期，拒绝换号/混合凭证，后续可继续模拟刷新 |
| 私有状态恢复、宿主机更新/退出、响应丢失 | 21 | 已收到的新凭证可恢复；响应丢失明确返回结果不明 |
| 多副本重复使用 refresh token | 1 | 确认文件 CAS 不能阻止服务端 family 撤销 |
| 官方 Codex 0.163.0 二进制、虚构账号与本机服务 | 1 | 连续两次刷新并保存/复用最新 refresh token |
| 既有凭证生命周期回归 | 18 | 保持现有行为 |

官方 Codex 项使用 `account/read(refreshToken: true)` 显式触发刷新，不调用模型。
模拟服务收到两次刷新；一次非本机目标的代理请求被本地代理拒绝。子进程没有访问真实
供应商或本机登录。因此它证实了官方 CLI 的刷新/保存协议行为，**不是生产账号自动续期验收**。

## 补充验证：官方刷新接口与真实 Antigravity

### Codex 0.163.0：官方外部认证接口可用

按照[官方 App Server 文档](https://developers.openai.com/codex/app-server/)使用实验性
`chatgptAuthTokens` 登录和 `account/chatgptAuthTokens/refresh` 回调。启动三个私有 HOME
的官方 CLI，模拟宿主机与两个任务都接入同一个 Runner 刷新入口。三方最初只拿 access token，
本机模拟服务分别拒绝旧 token 后，三个 CLI 都回调宿主。

结果：**3 次认证拒绝 → 3 次回调 → 1 次模拟发行端刷新 → 3 个 CLI 恢复**。
Runner 先原子保存并 fsync 发行端返回的新凭证，再交付 access token；三个 CLI 的私有 HOME
都没有拿到 Runner 保管的 refresh token。这是官方客户端与模拟发行端的协议实测，未使用
当前 Codex 账号。没有证明不接入 Runner 的其他宿主机 CLI 也能被这把锁协调。

### Claude Code 2.1.326：有回调实现，但受入口限制

检查官方发布的 `@anthropic-ai/claude-agent-sdk@0.3.326` JavaScript 和本机官方 CLI，
发现 `getOAuthToken` / `oauth_token_refresh` 路径；**公开的 sdk.d.ts 未声明此选项**。
本机 CLI 还检查客户端入口：普通 `sdk-ts` 没有启用这条回调，允许的内部入口包括 `local-agent`。

全部在隔离 HOME、虚构凭证与本机模拟服务下实测，无 Claude 订阅、无真实 Anthropic 调用：

- 普通 `sdk-ts` 负对照：收到模拟 401 后回调数为 0，认证失败。
- 显式测试内部 `local-agent` 路径：**3 次模拟 401 → 3 次宿主回调 → 1 次模拟刷新 →
  3 次模型请求重试成功**。模型响应由本机夹具生成，没有消费订阅或 API 额度。

这证明当前客户端有可工作的协议实现，**没有证明它是稳定公开 SDK 契约，也没有完成真实
Claude 订阅、组织归属或服务端刷新策略验收**。内部入口仅用于隔离实验，正式实现不能把
未经确认的内部能力当成无条件支持。

### Antigravity 1.5.10：真实 Google 并发刷新成功

`TestWritebackProbeAntigravityLiveRefresh` 显式 opt-in 后读取本机既有 Antigravity 钥匙串
记录。只调用官方 `agy models`，通过让私有副本的 access token / expiry 失效触发真实刷新；
不修改宿主原件使其过期、不触发交互登录、不调用付费模型推理。

| 真实场景 | 结果 |
| --- | --- |
| native 单次前置刷新 | 成功，18 个模型，refresh token 未变化 |
| 两路 native + 一路 Docker 同用旧 refresh token 并发刷新 | 三路全部成功，各返回 18 个模型，refresh token 均未变化 |
| 三路完成后再用同一个 refresh token 刷新 | 成功，refresh token 仍未变化 |
| 删除 refresh token，只保留有效 access token，native 查询模型 | 成功，18 个模型 |
| 同样的 access-only 文件，Docker 查询模型 | 成功，18 个模型 |
| 最终 access token 向 Google OIDC userinfo 查询身份 | 成功，不记录账号标识 |

此处“宿主侧”是隔离的 native 凭证副本；验证的是同一个真实 Google refresh token 的
并发重用行为，没有把用户正在使用的外部宿主 CLI 改接到新的管理器。native / Docker 都是
直接执行官方 CLI，没有重新创建完整对话或 SOP 业务任务。

有效刷新结果保留到原钥匙串来源；检测到宿主源已更新且 refresh token 相同，保留宿主更新。
模型查询失败也先检查是否已生成需要保留的新凭证。所有并发进程收集结束后才清理私有副本。
Docker 使用宿主 HOME 下缓存目录的私有临时子目录和严格 bind mount；未共享的 macOS /tmp
不能被误当作已经挂载的凭证目录。测试目录 `0700`、凭证 `0600`，成功后全部删除，不写入镜像或仓库。

**结论只适用于本次账号、版本与请求范围**：没有观察到“刷新一次立即作废旧 refresh token”。
这降低了本次并发导致掉登录的担忧。access-only 查询成功还不能证明长任务中的 token 热更新、
token 到期后的自动恢复，或未来的服务端轮换策略。

### 补充实验的隔离方式

`scripts/probe-cli-auth-control.py` 为 Codex / Claude 创建临时证书和仅本机的模拟服务。
Claude 的官方主机名由本机夹具模拟，用来进入客户端第一方 OAuth 分支；代理绝不向外转发，
子进程同时由 macOS 禁止访问外网和真实认证目录。证书只传给测试子进程，不修改系统信任。
这不是生产认证代理方案。Antigravity 的真实测试在另一个进程运行，没有使用该代理或测试 CA。

## 现有实现的复现

下表为改动前的复现结果。后续 `TestRunnerLoginSandboxEditsNotWrittenBack` 已改为断言拒绝这些编辑；
托管续期的正向覆盖由刷新管理器和官方客户端测试提供。

| 改动 | Codex | Claude Code | Antigravity |
| --- | --- | --- | --- |
| 同账号正常续期（改动前） | 写回 | 写回 | 写回 |
| 整套换号，身份字段也变更 | 拒绝 | **写回** | 拒绝 |
| 换号，但保留/伪造原本的本地身份字段 | **写回** | **写回** | **写回** |
| 保留 access token，仅换另一账号的 refresh token | **写回** | **写回** | **写回** |

Codex 的 `tokens.account_id`、Antigravity 的 email、以及 Claude 的本地配置都不能单独
证明新 refresh token 的归属。尤其 Codex 的工作区 ID 不应被当成唯一的用户标识。

## 测试中的验证器

这是 `_test.go` 内的实验，不是待发布的生产验证器：

1. 在复制凭证前，向可信身份服务确认宿主机的主体、工作区、组织，保存 Runner 私有基准。
   本地已过期凭证如何建立这一基准，尚需各 CLI 的具体协议解决。
2. 将候选文件保存到沙箱之外的 Runner 私有状态。示例目录 `0700`、文件 `0600`，
   临时文件写入后 `fsync`、rename、目录 `fsync`。身份基准与原文件快照都不由沙箱提供。
3. 未变更的候选直接跳过，不额外刷新。宿主机已更新或已登出，直接放弃写回。
4. 候选 access token 若能验证，先拒绝不同主体。access token 已失效则不以此判断归属。
5. **兑换候选 refresh token**，保存认证服务返回的完整新凭证，再核实新 access token 的身份。
   这一步才会暴露“access 属于 A、refresh 属于 B”的混合凭证。
6. 身份一致且宿主机仍未变更，写回服务端返回的新凭证，绝不写回已经被再次兑换的沙箱凭证。
   响应未给新 refresh token 时保留兑换时使用的 token，不把它清空。
7. 新凭证已持久化、身份服务暂时故障：恢复后只查询身份，不重复兑换旧 refresh token。
   刷新请求结果不明：标记 `uncertain`，不盲目重试，也不声称已恢复自动续期。

测试还保留 Claude 的 scopes、subscriptionType、rateLimitTier 等字段，并验证写回后的
凭证能在下一次模拟 CLI 调用中继续刷新。

## 接入前仍需解决

| CLI | 已确认的协议依据 | 仍缺少的验证 |
| --- | --- | --- |
| Codex | 官方外部认证回调已通过三客户端并发合并验证；接口为实验性 | 独立测试账号下的主体/工作区绑定、轮换与重用策略，以及未接入管理器的宿主客户端 |
| Claude Code | 当前客户端内部回调路径通过模拟 401 与三进程重试验证；普通 sdk-ts 不支持 | 可公开依赖的接口、真实订阅与组织归属、服务端刷新契约；不能把内部入口直接作为稳定能力 |
| Antigravity | 同一真实 refresh token 的三路并发与后续重用成功；access-only 查询成功 | 长进程跨过期时接收新 access token 的机制、更多账号/版本行为；不能只比较 email |

还需处理：刷新成功到本地状态持久化之间的进程/磁盘故障窗口、长期待验证凭证过期、
待处理记录的清理与权限隔离、宿主机非合作 CLI 的并发刷新。现有文件写入器在 rename 前
再次读取，但这不是与外部 CLI 共同参与的线性化 CAS。

更稳妥的后续方向是确认各 CLI 是否允许 Runner 管理刷新：真实 refresh token 留在可信
进程，沙箱获得短期 access token 或限于本次会话的刷新句柄。先检查官方扩展点；本次不
在正式执行路径引入认证代理、TLS 拦截或新的鉴权路径，也不关闭现有自动写回。

## 复现命令

```sh
go test ./internal/runtime -run '^TestWritebackProbe' -count=1 -v
go test -race ./internal/runtime -run '^TestWritebackProbe' -count=1
```

包含既有凭证生命周期回归：

```sh
go test -race ./internal/runtime \
  -run '^Test(WritebackProbe|RunnerLogin|ManagedLogin|ConversationRunnerLogin|Antigravity.*Login)' \
  -count=1
```

默认不会启动任何本机 CLI。`TestManagedLoginOfficialCodex` 需要显式设置
`LOOPFORGE_PROBE_CODEX_BIN`，并要求 macOS `sandbox-exec`。它给官方二进制提供全新的 HOME、
虚构的 token 和本机模拟刷新服务，同时由操作系统禁止读取真实认证目录、写入真实 HOME
和访问非本机网络。此项只验证 CLI 协议行为，不验证真实供应商策略。

```sh
LOOPFORGE_PROBE_CODEX_BIN=/absolute/path/to/official/codex \
  go test -race ./internal/runtime -run '^TestManagedLoginOfficialCodex$' -count=1 -v
```

官方 CLI 外部认证协议（全部是假凭证、本机模拟响应；需要 macOS、Python 3.9+、openssl）：

```sh
python3 scripts/probe-cli-auth-control.py codex --binary /absolute/path/to/official/codex
python3 scripts/probe-cli-auth-control.py claude --binary /absolute/path/to/official/claude \
  --clients 1 --expect-unsupported-sdk
# 下面仅验证内部入口，不能据此认定公开 SDK 已支持。
python3 scripts/probe-cli-auth-control.py claude --binary /absolute/path/to/official/claude \
  --claude-entrypoint local-agent
```

以下是**会读取并刷新真实 Antigravity 登录**的单独 opt-in，要求预先准备不含凭证的官方 CLI
Docker 镜像；只在已授权的机器运行，确保 PATH 中的 docker 连接本地引擎：

```sh
LOOPFORGE_PROBE_AGY_LIVE=1 \
LOOPFORGE_PROBE_AGY_BIN=/absolute/path/to/official/agy \
LOOPFORGE_PROBE_AGY_DOCKER_IMAGE=credential-free-official-agy-image \
  go test ./internal/runtime -run '^TestWritebackProbeAntigravityLiveRefresh$' -count=1 -v
```

## 协议依据

- [Codex 官方认证实现](https://github.com/openai/codex/blob/5a4aff84319b4376658b4cac50fcd15fa976f6fa/codex-rs/login/src/auth/manager.rs)：
  `persist_tokens`、`request_chatgpt_token_refresh`、`RefreshResponse`。
- [Claude Code 官方认证说明](https://code.claude.com/docs/en/authentication)：登录及自动刷新。
- [Google OAuth 刷新契约](https://developers.google.com/identity/protocols/oauth2/web-server#offline)：
  刷新返回新 access token，示例响应不含 refresh token。
- [Google OIDC](https://developers.google.com/identity/openid-connect/openid-connect)：userinfo 与稳定主体标识。
- [RFC 6749 §6](https://www.rfc-editor.org/rfc/rfc6749#section-6)：若发行新 refresh token，客户端必须替换旧值。
- [RFC 9700 §4.14.2](https://www.rfc-editor.org/rfc/rfc9700#section-4.14.2)：refresh token 轮换及重用检测可能导致当前 family 撤销。
