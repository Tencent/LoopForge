# IM SDK 诊断模块

本目录是独立 Go 模块（`agentflow/scripts/im-sdk-probe`），用来固定和验证企业 IM 方案评估过的第三方 SDK 版本，见 `docs/proposals/enterprise-im.md` 第 8 节。它不进入主模块的 `go.mod`；主模块只在适配器真正使用某个 SDK 时才增加依赖。

```bash
cd scripts/im-sdk-probe
go vet ./... && go test -count=1 -v ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

升级任一 SDK 时，先在这里修改版本并重跑；结论变化时同步更新方案第 8 节。

## 飞书 `github.com/larksuite/oapi-sdk-go/v3@v3.13.0`

`TestFeishuStartStopsOnContextCancel`：`ws.Client.Start(ctx)` 阻塞运行，context 取消后返回 `context canceled`。同一文件中的 `feishuSurface` 在编译期确认阶段 E 会用到的接口：消息接收事件头（`app_id`、`tenant_key`、`event_id`）、Message Reply/Create 的 `uuid`、`tenant/v2` 的 `Query`。

## 钉钉 `github.com/open-dingtalk/dingtalk-stream-sdk-go@v0.9.1`

结论：**不使用 `StreamClient` 的连接循环**，阶段 F 在主模块中自行实现最小连接层。只复用接入点获取、`payload` 帧编解码和 `chatbot.BotCallbackDataModel` 事件模型。

### 崩溃复现

`TestDingTalkPongTimeoutThenFrame` 在子进程中运行 `TestDingTalkPongTimeoutChild`，并断言子进程以该 panic 退出。

1. 本地 HTTP 服务模拟 `POST /v1.0/gateway/connections/open`，返回指向本地 WebSocket 的 endpoint。
2. 用 `WithOpenApiHost` 指向本地服务，`WithKeepAlive(3s)`、`WithAutoReconnect(false)`，注册机器人回调后 `Start`。
3. 服务端吞掉 ping、不回 pong。3 秒后 SDK 发 ping，再过 5 秒判定 pong 超时，`processLoop` 返回。
4. 服务端在第 10 秒推送一帧文本消息。
5. 进程以 `panic: send on closed channel` 退出（exit status 2）。

单独查看调用栈：

```bash
DT_CHILD=1 go test -count=1 -run TestDingTalkPongTimeoutChild -v ./...
```

```text
panic: send on closed channel

goroutine 20 [running]:
github.com/open-dingtalk/dingtalk-stream-sdk-go/client.(*StreamClient).processLoop.func6()
	$GOMODCACHE/github.com/open-dingtalk/dingtalk-stream-sdk-go@v0.9.1/client/client.go:165 +0x9b
created by github.com/open-dingtalk/dingtalk-stream-sdk-go/client.(*StreamClient).processLoop in goroutine 19
	$GOMODCACHE/github.com/open-dingtalk/dingtalk-stream-sdk-go@v0.9.1/client/client.go:156 +0x2c5
```

### 源码位置（`client/client.go`）

| 行 | 内容 | 问题 |
| --- | --- | --- |
| 147–149 | `defer close(closeChan)`、`close(pongChan)`、`close(readChan)` | `processLoop` 返回时关闭三个通道 |
| 156–168 | 读协程 `for { ReadMessage(); readChan <- message }` | 没有退出信号；连接未被关闭时继续读 |
| 182–197 | ping 后起协程，5 秒未收到 pong 就 `closeChan <- struct{}{}` | 超时只让主循环返回，不关闭 `cli.conn` |
| 198–199 | `case <-closeChan: return` | 主循环返回触发第 147–149 行的 close |
| 165 | `readChan <- message` | 连接仍然可读，下一帧写入已关闭通道，panic 发生在 SDK 协程里，`processLoop` 的 `recover` 捕获不到，进程退出 |

同一路径还有：第 152 行 pong handler 和第 161、194 行向已关闭通道发送，同样会 panic。

### 其他不采用的原因

| 位置 | 问题 |
| --- | --- |
| `Start`（第 74 行起） | 内部 `dialer.Dial`，不接受 context，停止时可能卡在建连 |
| `reconnect`（第 275 行起） | `context.Background()` 无限重试，`Close` 后仍可能重连 |
| `chatbot/chatbot_replier.go` `ReplyMessage` | 默认重定向策略、固定 5 秒超时、200 响应不关闭 body |

### 阶段 F 自行实现的要求

按官方 Stream 协议覆盖：接入点鉴权（`connections/open` 换 endpoint 和 ticket）、服务端 `SYSTEM/ping` 应答和客户端 WebSocket 心跳、context 取消、`SYSTEM/disconnect` 和断线后的退避重连、每个 `CALLBACK` 帧入库后的回执。测试覆盖 pong 超时后迟到的帧、关闭期间的帧、取消时的建连与重连，均要求进程不退出、连接循环按时结束并能恢复。

实现位置：`internal/botinteraction/channel_dingtalk.go`。单进程测试 `TestDingTalkStream*`（`-race` 连跑 3 轮）覆盖 ping/pong、回执 200/500、入库超时、`disconnect`、心跳超时后的迟到帧、关闭期间的帧、取消；多进程故障测试 `TestDingTalkFaultMySQL` 的 D3 在真实子进程中复现“网关停止回 pong 后再推迟到帧”，接收进程不退出并自动重连。

本探针在上游修复后会失败（不再复现 panic），届时重新评估是否改用 `StreamClient`。
