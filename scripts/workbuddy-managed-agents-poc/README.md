# WorkBuddy Managed Agents 角色与工作流

## 用普通 Agent 节点运行 WorkBuddy 角色

在项目的「角色定义 → 身份」中，将 CLI 选为 **WorkBuddy Managed Agents**。选择已有的项目 AK/SK 凭据，填入开启 API 访问的 WorkBuddy Agent ID。腾讯云地域默认 `ap-beijing`。可填写版本 ID 固定 Gate 连接器和云侧技能等配置。角色和凭据一起限定在当前项目。

模型选择器内置了 2026-11-27 的 WMA 模型目录，包含支持工具调用的选项。绑定凭据后，查询成功时显示该腾讯云账号启用的模型。也可以手动填入模型 ID 或跟随云侧版本。显式选中的模型如果与会话当前模型不同，LoopForge 会先发出 ACP `session/set_model` 请求，确认成功后再提交任务。该 ACP 扩展仍处于实验阶段，切换失败会在提示词发送前停止。 [查询内置模型列表](https://cloud.tencent.com/document/api/1831/138624) · [WMA ACP 接入](https://cloud.tencent.com/document/product/1831/140759)

在 LOOP 中给普通 Agent 节点绑定这个角色即可。任务书、绑定的规则、产物契约和人工复核沿用角色定义。技能与连接器配在 WorkBuddy 版本中。由 LOOP 节点处理返工和重试。

## 执行与产物交接

角色模式和节点库里的独立 WorkBuddy 节点都通过腾讯云 API 3.0 创建会话，通过 ACP 执行任务。LoopForge 用 `submit_gate_result` 返回的终态推进流程。`pass` 后校验必需产物，`rework` 走节点配置的打回分支，`human` 等待确认，`fail` 结束本次执行。

1. 在当前项目的「项目凭据」新建 AK/SK 凭据，填入腾讯云 SecretId、SecretKey。临时凭据再填 Session Token。腾讯云账号需绑定对应的 WorkBuddy 企业。
2. 使用独立 WorkBuddy 节点时，在节点选择凭据，填写开启 API 访问的 Agent ID、地域和任务提示词。被路由的版本都需绑定 LoopForge Gate 连接器。
3. 如需文件交付，添加产物契约，填写 `/workspace` 下的相对路径。节点会让 Agent 用 `PresentFiles` 登记后再提交终态。后续可连接人工验收节点。
4. 下游 WorkBuddy 节点可在角色任务书或节点提示词中写 `{{artifact.上游节点ID.产物名}}`。节点会读取归档文件并校验 SHA-256，再把引用替换成 UTF-8 内容。每个节点显式引用的文件内容合计最多 8 KiB，若需要下游沙箱文件，提示词可让 Agent 将收到的内容写入指定路径。

执行过程中取消运行会请求取消当前 ACP 轮次。创建会话前会登记本次执行，防止 Worker 恢复同一次尝试时重复创建已计费的会话。会话创建结果不明时，先核对远端会话再手动重跑。

[腾讯云会话和签名说明](https://cloud.tencent.com/document/product/1831/140759) · [腾讯云会话物料下载接口](https://cloud.tencent.com/document/api/1831/140522)

## Gate MCP

### ACP 反向请求实验

设置 `AGENTFLOW_WMA_GATE_ACP_QUESTION=1` 时，WMA 通过 `AskUserQuestion` 在同一出站 ACP 会话内提交节点终态。LoopForge 仅将匹配本次会话与执行票据的结构化请求交给本机 Gate MCP 的 stdio 处理器。提交仍按 Gate MCP 的规则校验和落盘，文件产物校验和人工确认继续由 LoopForge 执行。此模式适用于验证 WMA ACP 反向请求，无需部署云侧连接器。

### 机房仅放行 WMA 出站

在 WMA Connector 能访问的云侧网络部署 `./cmd/agentflow-gate-mcp`，通过 HTTPS 提供 Streamable HTTP `/mcp` 入口。LoopForge 保持在机房，通过出站连接创建和读取会话。在 WMA 控制台的「智能体构建 → 连接器」登记云侧入口，再把连接器绑定到要执行的 Agent 版本。采用 API Key 鉴权时，配置连接器发送 `Authorization: Bearer <连接器密钥>`。[WMA 连接器](https://cloud.tencent.com/document/product/1831/140280)

使用腾讯云 API 绑定时，用 `DescribeConnectorList` 获取登记后的 `ConnectorId`，再用 `ModifyAgentVersion` 的 `ConnectorSet` 与 `Manifest` 指定该版本绑定的连接器。WMA 会为绑定生成 `*.agent-gateway.auth-proxy.local` 代理地址。[修改 Agent 版本](https://cloud.tencent.com/document/api/1831/138622)

用 Ed25519 生成签名密钥和公钥。

```bash
openssl genpkey -algorithm Ed25519 -out gate-private.pem
openssl pkey -in gate-private.pem -pubout -out gate-public.pem
```

1. 在云侧 Gate 设置 `AGENTFLOW_GATE_MCP_SIGNING_KEY_FILE` 指向私钥文件。使用 API Key 时，设置与 WMA 连接器一致的 `AGENTFLOW_GATE_MCP_CONNECTOR_TOKEN`。运行 `agentflow-gate-mcp -listen 127.0.0.1:8090`，让 HTTPS 代理将 `/mcp` 转到该地址。
2. 在 LoopForge 设置 `AGENTFLOW_WMA_GATE_PUBLIC_KEY_FILE` 指向公钥文件。此模式由 LoopForge 本地生成本次尝试的随机票据，只通过现有 WMA ACP 会话发送和接收 Gate 数据。
3. Gate 对终态和票据签名。LoopForge 可从 ACP 工具返回中读取回执，也可读取 Agent 在最终回复中逐字附上的回执。验签和票据匹配通过后才处理终态。缺少有效回执时停止该节点。腾讯云公开文档确认 ACP 文本增量回传，真实租户的工具输出透传和 Agent 回执行为仍需联调。[WMA ACP 接入](https://cloud.tencent.com/document/product/1831/140759)

连接器可配置为「无认证」，由 Gate 自己验证一次性票据。此时在 Gate 和 LoopForge 设置 `AGENTFLOW_GATE_MCP_TICKET_KEY_FILE`，分别指向包含同一组 32 字节随机值的文件。直接通过 Manifest 的 `mcp_servers` 声明服务 URL 时，WMA 会额外检查代理 MCP 主机允许名单。[WMA Manifest 字段说明](https://cloud.tencent.com/document/product/1831/139534)

文件产物仍通过腾讯云 API 返回的签名下载地址归档。若该地址使用独立域名，机房需要允许访问该域名。

### 两端都能访问的 Gate

如两端都能连接同一 Gate MCP，可使用共享模式。Gate 进程配置两个独立密钥 `AGENTFLOW_GATE_MCP_CONTROL_TOKEN` 和 `AGENTFLOW_GATE_MCP_CONNECTOR_TOKEN`，启动 `agentflow-gate-mcp -listen 127.0.0.1:8090`。LoopForge 设置 `AGENTFLOW_WORKBUDDY_GATE_URL` 和相同的 `AGENTFLOW_GATE_MCP_CONTROL_TOKEN`，由 Gate 签发票据并保存结果。WMA 连接器的配置方式相同。

两种 HTTP 模式的 PoC 都使用单实例内存记录，在五分钟内约束一次执行的重复提交。

## 本地验证

在无云端凭据的环境可运行本地协议测试。测试覆盖 TC3 签名和临时令牌、会话执行、取消、文件回传及完整性校验，以及门禁回执验签和跨尝试防重放。

```bash
go test ./internal/gatemcp ./internal/workbuddy -count=1
```

服务端以 `AGENTFLOW_WMA_LOOPBACK_ENDPOINT` 指定本机模拟接口，并允许 `AGENTFLOW_WORKBUDDY_GATE_URL` 使用回环 HTTP 地址，可配合本地 Docker MySQL 演示完整执行和审批。
