---
name: report-workflow-usage
description: 将外部工作流已结束执行的真实模型用量上报到 AgentFlow 用量分析。用于工作流结束后的上报节点、补报或重试，支持多模型汇总和项目限定凭据；不用于重新上报 AgentFlow 已原生统计的执行。
---

# 上报外部工作流用量

读取宿主提供的完整执行汇总，校验后调用本 Skill 的脚本上报。默认只处理当前工作流明确指定的那次执行。

## 工作流输入

- `AGENTFLOW_URL`：目标 AgentFlow 基础地址。
- `AGENTFLOW_INSIGHT_TOKEN`：宿主通过密钥环境变量注入的数据源 Token；不要把 Token 放进提示词、命令参数、产物或日志，也不要打印环境变量。
- `AGENTFLOW_USAGE_FILE`：宿主已生成的终态汇总 JSON 文件路径；也可显式指定文件参数。
- Python 3：脚本只依赖标准库。

挂载到工作流时，参考 [工作流集成](references/workflow-integration.md)。首次生成或转换输入时，读取 [上报契约](references/contract.md)。

## 执行

1. 确认汇总属于本次指定的外部执行，状态为 `succeeded`、`failed` 或 `cancelled`。不要为收尾节点另造一次“业务执行”，也不要把上报成功改写为业务成功。
2. 使用执行器/API 返回的真实模型用量。若宿主已给出符合契约的文件，直接使用；若给出其他格式，仅按已知字段含义转换。不要估算 Token、读取本机其他无关会话或用零填补未知用量。缺少真实用量时停止上报并说明需要的宿主数据；费用未知可以使用 `null` / `unknown`。
3. 从本 Skill 根目录调用以下脚本。脚本路径相对 Skill 根目录，输入文件路径相对工作目录；工作目录可以与 Skill 目录不同。

```sh
python3 /absolute/path/to/report-workflow-usage/scripts/report_usage.py --check
python3 /absolute/path/to/report-workflow-usage/scripts/report_usage.py
```

实际使用时将路径替换为当前已加载 Skill 的根目录。若没有设置 `AGENTFLOW_USAGE_FILE`，在脚本后传入宿主提供的 JSON 文件路径。`--check` 只校验文件，不联网、不要求 Token。

4. `accepted` 或 `duplicate` 是已持久化的成功回执。返回上报状态、记录 ID 和执行 ID；不要声称业务执行因此成功。失败时保留原文件，说明可补报。

## 重试与边界

- 同一来源、相同记录 ID、相同内容可安全重试。记录 ID 和执行 ID 在首次上报前固定，重试不要刷新时间、ID 或改用另一数据源 Token。
- 脚本对网络错误、429、5xx 默认最多尝试 5 次；用尽后停止本次上报，交给宿主重试策略。400、401、403、409、413 不自动重试；409 要核对原事实，不能生成新 ID 绕过去重。
- 不创建数据源、不扩大用户映射、不修改工作流主任务结果。接入时明确使用哪个项目的数据源；用户显式调用本 Skill，或宿主明确配置该上报步骤，即可按既有配置执行，不需要每次重复确认。
- 一次执行只由一个来源上报。AgentFlow 原生 Run 已被统计；不要把同一 Run 再作为外部执行上报。
- Agent 的最终 Token 往往在该 Agent 退出后才完整可得。若要包含收尾 Agent 本身的消耗，必须由宿主在会话结束后调用脚本，不能在该 Agent 尚未结束时声称上报了完整用量。
