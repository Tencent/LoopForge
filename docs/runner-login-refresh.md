# Runner 本机登录与自动续期

对话工作区与 SOP 的 native、Docker 沙箱共用同一套登录准备逻辑。
API Key 模式沿用个人设置，不经过本机 OAuth 刷新管理器。

## Codex / Claude Code

Runner 保管宿主机原凭证。任务只能得到当前 access token 与本次任务的刷新能力，
不会得到上游 refresh token。任务在自己的 HOME 里换号、伪造身份字段或替换 refresh token，
cleanup 都不会把这些文件写回宿主机。

- **Codex**：私有 `auth.json` 中的 refresh token 是随机任务句柄，
  `CODEX_REFRESH_TOKEN_URL_OVERRIDE` 指向 Runner 的临时入口。官方 CLI 自己判断何时需要刷新，
  Runner 用宿主原凭证向 OAuth 服务申请，保存成功后返回 access token 和下一个临时句柄。
  同一旧句柄的重试复用原响应，不会再次刷新上游。
- **Claude Code**：通过 stream-json 的 `oauth_token_refresh` 控制通道回调 Runner，
  stdin 返回 access token。任务 credential 文件不含 refresh token；临近过期的宿主凭证在启动前续期。
  单次 SOP 收到最终结果后关闭输入；对话保留原有可继续发送消息的输入通道。

普通 Docker SOP 使用 `AGENTFLOW_DOCKER_NETWORK` 指定的网络。对话工作区和包含 Codex 账号登录节点的
Docker SOP 使用中继网络配置连接执行机器上的回调。SOP 的准备脚本、终端和执行节点共享同一个容器。

Claude Code 的回调在 **2.1.326** 的官方客户端中实测可用，但属于受入口限制的内部协议，
并不是公开稳定的 SDK 类型契约。当前绑定 `local-agent` 入口，内置安装版本为 2.1.326。
已知较旧版本或初始化失败会提示更新，不会退回向沙箱发送真实 refresh token。
未使用真实 Claude 订阅验收；测试使用官方二进制、虚构凭证与本机模拟认证/模型服务。

## 并发、持久化与账户切换

同一凭证来源使用进程内锁与跨进程文件锁。两个 SOP 同时刷新时，第一个完成上游交换，
后续请求复用宿主已经更新的凭证。已接入同一管理器的宿主进程也遵守这把锁。

刷新前记录交换状态，收到响应后先将完整新凭证保存到私有 journal，再原子替换宿主文件并同步磁盘。
最终只保留不含 token 的 hash receipt，供另一 Runner 进程识别该更新；正常完成后删除 journal。
进程重启时先恢复已收到的新凭证，避免重复使用已经轮换的旧 refresh token。
任务取消不取消已经开始的交换，最多等待独立的 30 秒期限来保存结果。

若请求已发出，但响应丢失或不可解析，旧 access token 和宿主文件保留；
同一份 refresh token 不会被自动重复兑换。此时需宿主登录源被重新登录或其他官方进程更新后才能继续续期。
客户端无法恢复从未收到的新 token。明确的 HTTP 4xx 拒绝（408 除外）允许以后重试。

宿主退出登录不会被 journal 复活；宿主切换账号时，已有任务不会接入新账号。
凭证来源为符号链接时，同时核对原路径；链接被删除或改指向后，停止使用旧目标，刷新中也不会向旧目标写回。
Codex 的用户身份与工作区共同作锚点；Claude 使用可信宿主 `.claude.json` 的账号/组织资料。
缺少账号资料时，只接受管理器自己有 receipt 的更新，其他变化需要重启任务。

默认私有状态目录为系统用户缓存目录下 `LoopForge/runner-auth`；可以用
`AGENTFLOW_RUNNER_AUTH_DIR` 指定持久目录，必须放在任务工作区之外。目录权限 `0700`，文件 `0600`。
native 托管任务遮蔽原凭证与刷新目录；Docker 只得到工作区中的私有副本。
任务结束会关闭刷新入口并移除临时凭证，不把刷新控制消息写入任务日志。

**独立启动、没有接入管理器的宿主机官方 CLI 不受这把锁约束。**
Runner 会尽量识别并保留宿主更新，文件内容复核不能等价于所有应用共同参与的事务。

## Antigravity

保持官方 CLI 在沙箱私有文件中自行刷新。当前 Google 登录实测允许并发重用同一 refresh token，
正常刷新会改变 access token，refresh token 不变。

cleanup 要求 refresh token 与宿主原件一致，并通过 Google OIDC userinfo 核验新 access token
属于原账号。access token 未变化时跳过写回。写回前检查 expiry 是尚未到期的时间戳，
显式提供的 token type 必须为 Bearer。保留宿主原有的账户资料及 refresh token。
宿主已变化、核验失败或只替换 refresh token 时继续保留宿主记录。
这没有实现 access-only 的长任务热更新，也没有把未来可能出现的 Google refresh-token 轮换当作已验证行为。

## 验证方式

普通测试不读取本机真实登录。可显式启用以下测试：

```sh
# 官方 Codex / Claude Code：仅本机虚构认证服务，macOS sandbox-exec 禁止外网与真实凭证访问
LOOPFORGE_PROBE_CODEX_BIN=/absolute/path/to/codex \
LOOPFORGE_PROBE_CLAUDE_BIN=/absolute/path/to/claude \
go test ./internal/runtime -run 'TestManagedLoginOfficial' -race -count=1

# 实际 native + Docker，对话和 SOP；使用 Python CLI 夹具与虚构 token
# 需要预先提供 python:3.13-alpine，macOS 的工作区放在 Docker 已共享的用户缓存目录
LOOPFORGE_PROBE_AUTH_SANDBOXES=1 \
go test ./internal/runtime -run TestManagedLoginRuntimePaths -race -count=1
```

Antigravity 的真实 Google 刷新实验单独 opt-in，命令及边界见
[验证记录](proposals/runner-login-writeback-verification.md)。不与虚构认证代理共用进程。
