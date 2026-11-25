# Token 成本统计（人民币）

业务领域、研发效能和项目统计使用同一套只读计费投影：**有效上报费用优先，未上报时才按公开标准价估算**。原始 Run、节点和外部工作流上报金额不被覆盖，不需要数据库迁移；SOP 历史节点按保留的上报事实使用新口径展示。旧对话工作区只有已累计的金额时，不能凭汇总恢复丢失的原始上报费用。

## 计费规则

- `costStatus=reported` 且金额是有限、非负值时，保留上报金额，包括明确上报的 **0 美元**；不因公开价较高而覆盖。此时无需模型价目表或 Token 分项。美元按 **1 USD = 7 CNY** 换算。
- CodeBuddy 的兼容字段 `total_cost_usd: 0` 不能作为真实免费凭据：本机核查的官方发布包 `@tencent-ai/codebuddy-code@2.132.0` 将它硬编码为 0，积分在独立字段中上报。CodeBuddy 的占位美元 0 归一化为未上报费用，按下条规则回退；非零有效美元上报则保留。其他来源明确上报的 0 仍按 0 统计。
- 没有有效上报费用时，才按已核实的公开标准 Token 价格估算。优先使用人民币单价；只有美元单价的模型按 **1 USD = 7 CNY** 换算。缺少上报的 Hy / Hunyuan 系列（含 `hy3-ioa`、`hy4std`、`hy4-preview-ioa`）采用平台免费价。
- 输入、输出、缓存读取和缓存创建分别计费；推理 Token 已包含在输出中，不再额外收费。Anthropic 原生输入不含缓存分项；Codex/OpenAI 及外部上报的输入包含缓存，计算普通输入时先扣除缓存分项。
- 没有上报费用且缺少模型、公开价或 Token 分项时，不能推算完整费用；保留可识别部分的金额并显示覆盖率，不把未知显示成免费。
- 一个 Run 包含多个模型时，分别计价后求和；统计页总额、趋势、项目、责任人、SOP、模型和需求明细使用同一个投影。零用量的脚本节点不产生 Token 费用。
- 小额费用在求和前不取整。新字段 `costCny` / `totalCostCny` / `costReportedCny` 的单位是人民币；兼容字段 `costUsd` 等表示该统计金额按 7 折算的美元等值，不能再当成原始账单金额。

### CodeBuddy 的会话累计与本轮用量

CodeBuddy 的 `result.usage` 是原生会话的累计值，续聊和重试时不能把它当成本轮增量再次相加。助手对话优先使用 `result._meta["codebuddy.ai/turnUsage"].total.usage` 的本轮终态快照；`stage`、`delta`、`total`、终态快照和子代理结果不重复累加。旧版 CLI 没有本轮快照时，按原生会话保留累计游标、计算差额，缓存读写也分别做差。续接会话缺少可靠游标或游标重置时，只采集独立的消息用量，否则将该轮保留为未采集并建立后续基线。进程中断后若已按消息用量计入，则废弃旧游标，避免下一次累计结果再次包含这部分消耗。协议参见 [CodeBuddy 每轮用量说明](https://www.codebuddy.ai/docs/cli/stream-json-turn-usage)。

CodeBuddy 和 Anthropic 协议的 SOP 消息级 `usage.update` 使用独立游标暂记到运行与节点，不推进会话累计游标。根会话上报最终 Token 快照时，用最终快照结算并扣除已暂记部分；子代理最终结果不再单独累计。同一会话重试延续累计游标，进程在最终结果前中断或结果未包含 Token 用量时，保留已经观测的流式用量，避免将其计为零。

CLI 顶层的 `total_cost_usd` 是美元字段，助手对话也采集它；有效的会话累计美元上报做差后再计入，不能直接叠加。CodeBuddy 的兼容占位 0 按缺失处理；`turnUsage.total.credits` 是积分，不转换成美元或人民币。一次回复内的多次调用分别选择上报金额或公价回退，再累加；`usage.costPricingVersion` 标记平台已计价的合计，防止混合金额再次整体估算。兼容字段 `costReportedUsd` / `costReportedCny` 保留原 API 语义，仍为已识别部分的总金额，可能同时包含上报与估算。

Claude Code 的 `usage` 是本轮 Token，`total_cost_usd` 则包含续接会话的历史美元合计，两者分别处理。其美元上报本身仍可能是 CLI 本地估算，并非供应商结算凭证，见 [Claude 官方成本跟踪说明](https://code.claude.com/docs/en/agent-sdk/cost-tracking)。

此修正影响后续采集。此前保存的回复、SOP 和工作区汇总可能已重复累计；仅凭汇总无法可靠区分重试、会话重建、模型切换和历史回放，需核对原始逐轮记录后单独重算，不自动改写历史账单或执行数据。

## 价格版本与适用范围

计费策略版本：`reported-first-2026-11-22`。回退价表沿用 `public-standard-2026-11-05`，核价日期 **2026-11-05**（Kimi K3.1 Pro、MiMo-V2.6-Pro 于该日补登，其余沿用 2026-11-02 核价），此次不调整单价。费率登记在 `internal/domain/token_pricing.go`，新增模型先核实官方价格再更新登记和测试；不使用模糊前缀给未知型号套价。已知的 `-ioa`、`-internal-ioa`、Claude 点号/短横线版本、日期快照和 `-1m` 别名显式归一化；`custom-local:<模型>`（用户自带 key 直连厂商）按去前缀后的同名公开价计算。Gemini 3.5 Flash 已不在 Google 官方价格页，暂不登记，显示为未识别。

上报金额使用执行器提供的美元事实；**公价回退部分是估算**，不是供应商结算账单，也不是按历史时点的价格回溯。存量累计 Token 无法还原逐请求的长上下文阶梯、缓存 TTL、区域、加速档或峰谷时段。公价回退统一使用普通在线、基础上下文、标准缓存创建档，DeepSeek 使用高峰标准单价；不额外叠加订阅、赠送、批处理和渠道活动折扣。精确结算需逐请求的计量和计价依据。

## 官方价格来源

| 模型 | 公开价格来源 | 采用币种 |
| --- | --- | --- |
| OpenAI GPT-6 / GPT-5.6 / Codex | [OpenAI API Pricing](https://developers.openai.com/api/docs/pricing)，[GPT-5.6 Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra)，[GPT-5.6 Luna](https://developers.openai.com/api/docs/models/gpt-5.6-luna) | USD × 7 |
| GPT-5.5 / GPT-5.4 / GPT-5.3 Codex / GPT-5.2 | [GPT-5.5](https://developers.openai.com/api/docs/models/gpt-5.5)，[GPT-5.4](https://developers.openai.com/api/docs/models/gpt-5.4)，[API Pricing](https://developers.openai.com/api/docs/pricing)，[GPT-5.2](https://developers.openai.com/api/docs/models/gpt-5.2) | USD × 7 |
| Claude | [Anthropic Pricing](https://platform.claude.com/docs/en/about-claude/pricing) | USD × 7 |
| DeepSeek Flash / V4 Pro | [DeepSeek 中文价格表](https://api-docs.deepseek.com/zh-cn/quick_start/pricing/) | CNY |
| GLM 5.3 / Flash / FlashX / 5.2 | [智谱官方价格页](https://bigmodel.cn/pricing)，该页需 JavaScript；已核对输入、输出、缓存命中人民币价格 | CNY |
| GLM 5V Turbo / Kimi（含 K3.1 Pro）/ MiniMax / MiMo | [腾讯云 TokenHub 国内在线推理价格表](https://cloud.tencent.com/document/product/1823/130055)（广州，2026-11-04 版），[MiniMax 价格表](https://platform.minimax.cn/docs/pricing/overview)，[小米 MiMo-V2.6-Pro](https://mimo.mi.com/models/zh-CN/mimo-v2.6-pro) | CNY |
| 千问 | [阿里云百炼华北2标准价格](https://help.aliyun.com/zh/model-studio/model-pricing) | CNY |
| Gemini 文本模型 | [Gemini API Pricing](https://ai.google.dev/gemini-api/docs/pricing) | USD × 7 |

未上报费用时的回退校验例（输入、输出各 100 万 Token，不含缓存）：GLM-5.3-Flash 为 **3.6 元**；Claude Opus 4.6 为 **210 元**。Hy 系列缺少上报时按免费价；任何模型明确上报费用时均优先保留上报金额。
