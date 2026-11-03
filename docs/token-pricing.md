# Token 成本统计（人民币）

业务领域、研发效能和项目统计使用同一套只读计费投影。原始 Run、节点和外部工作流上报金额不被覆盖，也不需要历史数据库迁移；历史数据在查询时按本口径重新测算。

## 计费规则

- Hy / Hunyuan 系列保留适配器上报金额，包括明确上报的零费用；缺失金额仍是未识别。现有原始金额字段单位是 USD，统计时按 7 折成人民币。
- 其他模型统一按已核实的公开标准 Token 价格计算，覆盖已有上报的低价或零价。优先使用人民币单价；只有美元单价的模型按 **1 USD = 7 CNY** 换算。
- 输入、输出、缓存读取和缓存创建分别计费；推理 Token 已包含在输出中，不再额外收费。Anthropic 原生输入不含缓存分项；Codex/OpenAI 及外部上报的输入包含缓存，计算普通输入时先扣除缓存分项。
- 未记录模型、未收录公开价、只有总 Token 而没有输入输出分项，均不能推算完整费用。保留可识别部分的金额并显示覆盖率，不用适配器低价兜底，也不把未知显示成免费。
- 一个 Run 包含多个模型时，分别计价后求和；统计页总额、趋势、项目、责任人、SOP、模型和需求明细使用同一个投影。零用量的脚本节点不产生 Token 费用。
- 小额费用在求和前不取整。新字段 `costCny` / `totalCostCny` / `costReportedCny` 的单位是人民币；兼容字段 `costUsd` 等表示该统计金额按 7 折算的美元等值，不能再当成原始账单金额。

## 价格版本与适用范围

版本：`public-standard-2026-11-02`，核价日期 **2026-11-02**。费率登记在 `internal/domain/token_pricing.go`，新增模型先核实官方价格再更新登记和测试；不使用模糊前缀给未知型号套价。已知的 `-ioa`、`-internal-ioa`、Claude 点号/短横线版本、日期快照和 `-1m` 别名显式归一化。

这是用于比较投入的**公开标准价测算**，不是供应商结算账单，也不是按历史时点的价格回溯。当前存量数据主要是每节点/执行的累计用量，无法从累计输入量还原每次请求的长上下文阶梯、缓存 TTL、区域、加速档或峰谷时段。因此统一使用普通在线、基础上下文、标准缓存创建档，DeepSeek 使用高峰标准单价；采用核价日官方标准在线表列价格，不额外叠加订阅、赠送、批处理和渠道活动折扣。需要精确结算时应接入逐请求的计量和计价依据。

## 官方价格来源

| 模型 | 公开价格来源 | 采用币种 |
| --- | --- | --- |
| OpenAI GPT-6 / GPT-5.6 / Codex | [OpenAI API Pricing](https://developers.openai.com/api/docs/pricing)，[GPT-5.6 Terra](https://developers.openai.com/api/docs/models/gpt-5.6-terra)，[GPT-5.6 Luna](https://developers.openai.com/api/docs/models/gpt-5.6-luna) | USD × 7 |
| GPT-5.5 / GPT-5.4 / GPT-5.3 Codex / GPT-5.2 | [GPT-5.5](https://developers.openai.com/api/docs/models/gpt-5.5)，[GPT-5.4](https://developers.openai.com/api/docs/models/gpt-5.4)，[API Pricing](https://developers.openai.com/api/docs/pricing)，[GPT-5.2](https://developers.openai.com/api/docs/models/gpt-5.2) | USD × 7 |
| Claude | [Anthropic Pricing](https://platform.claude.com/docs/en/about-claude/pricing) | USD × 7 |
| DeepSeek Flash / V4 Pro | [DeepSeek 中文价格表](https://api-docs.deepseek.com/zh-cn/quick_start/pricing/) | CNY |
| GLM 5.3 / Flash / FlashX / 5.2 | [智谱官方价格页](https://bigmodel.cn/pricing)，该页需 JavaScript；已核对输入、输出、缓存命中人民币价格 | CNY |
| GLM 5V Turbo / Kimi / MiniMax | [腾讯云 TokenHub 国内在线推理价格表](https://cloud.tencent.com/document/product/1823/130055)，[MiniMax 价格表](https://platform.minimax.cn/docs/pricing/overview) | CNY |
| 千问 | [阿里云百炼华北2标准价格](https://help.aliyun.com/zh/model-studio/model-pricing) | CNY |
| Gemini 文本模型 | [Gemini API Pricing](https://ai.google.dev/gemini-api/docs/pricing) | USD × 7 |

常用校验例（每项各 100 万 Token，不含缓存）：GLM-5.3-Flash 输入 0.8 元 + 输出 2.8 元 = **3.6 元**；Claude Opus 4.6 输入 5 USD + 输出 25 USD，按 7 折算为 **210 元**。Hy 上报 0.001 USD 时折算为 **0.007 元**。
