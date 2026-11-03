# 将 Skill 集成到工作流

## 外部 Agent 工作流

将整个 `report-workflow-usage` 文件夹放入宿主的 Skill 目录，按宿主已有机制将其挂载到收尾 Agent。工作流配置中给该步骤提供：

| 输入 | 来源 |
|---|---|
| `AGENTFLOW_URL` | 目标平台地址 |
| `AGENTFLOW_INSIGHT_TOKEN` | CI / 执行器的密钥配置，不放到节点提示词 |
| `AGENTFLOW_USAGE_FILE` | 该执行的终态汇总文件绝对路径 |

可直接使用的收尾节点指令：

> 使用 $report-workflow-usage，将 AGENTFLOW_USAGE_FILE 指定的外部执行汇总上报至已配置的数据源。先校验，只使用宿主真实用量；保留原始状态和幂等 ID。返回接收回执；如果失败，保留文件并标记待补报，不修改主任务结果。

安排在业务执行已结束且最终用量已落盘后。成功、失败、取消都应进入宿主的 finalizer / finally / always-run 步骤。单纯连一条“成功时进入下一节点”的边无法保证失败执行上报；宿主被强制终止时只能依赖后续补报。

如果最后一个 Agent 的用量必须纳入总额，由宿主在它退出后直接调用脚本。这也适用于没有 Agent 的 CI 工作流：

```sh
python3 "$USAGE_SKILL_ROOT/scripts/report_usage.py" --check
python3 "$USAGE_SKILL_ROOT/scripts/report_usage.py"
```

`USAGE_SKILL_ROOT` 是解压后的 Skill 绝对路径。JSON 由宿主按 `contract.md` 生成，不依赖预设的 CI 厂商变量。执行工作目录与 Skill 目录可以不同。

宿主集成建议（伪代码，按自己的执行器适配）：

```text
运行主工作流并保留其成功/失败/取消状态
等待执行器落盘最终 usage
保存稳定的完整汇总 JSON（记录 ID、执行 ID、完成时间均固定）
在收尾步骤调用 Skill 或脚本
  accepted/duplicate -> 记录回执
  失败 -> 保留文件，安排补报；主任务结果保持不变
```

Skill 上下文本身不会自动传递进程环境变量：须确认执行 Skill 的脚本进程能获得上述环境变量。凭据只能在可信执行环境由宿主注入，避免日志中输出。

## 在 AgentFlow 中挂载

平台现有继承链路是：**工作流节点 → 角色定义 → Skills → 执行环境内的 Skill 文件**。

1. 「技能管理」新建技能，建议 ID 使用 `report-workflow-usage`。
2. 保存后上传整个 Skill 文件夹或 ZIP；保留 `SKILL.md`、`scripts/`、`references/` 和 `agents/` 的相对结构。
3. 在收尾节点对应的「角色定义」中勾选该技能；将该角色绑定到工作流节点。
4. 为运行它的执行器/Runner 配置环境变量，并把**外部执行的汇总文件**交给节点。选择技能不会自动创建凭据或采集最终 Token。

没有引入新的工作流级 `skills` 字段或自动安装逻辑；无需迁移工作流 schema，也无需在启动时 seed 数据。

AgentFlow 自己执行的 Run 已原生计入分析。只在节点负责收集、转发另一外部系统的执行事实时使用本 Skill，不能再次上报该平台 Run。不要把该 Skill 挂到每个业务节点重复上报同一份总量。
