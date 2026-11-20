# Hermes Agent 适配器

Hermes Agent 通过项目内的 Agent Skills 目录加载 LoopForge：安装器写入
`.hermes/skills/`，不会修改 Hermes 用户目录或 `config.yaml`。首次在该项目运行
Hermes 时，需要按宿主的信任提示执行 `hermes skills trust`，之后 `/devflow` 才会
出现在会话中。

## 版本和能力边界

适配器支持当前稳定版以及后续仍提供项目级 `.hermes/skills/`、`hermes skills trust` 和
`delegate_task` 的 Hermes Agent 版本，不绑定私有版本号。Hermes 的插件和 skill API
仍在演进；LoopForge 只依赖这三个公开能力，不读取 Hermes 私有状态文件。若当前会话没有
`delegate_task`，medium/large 工作流必须记录具体原因并降级为
`single-context`，不能把当前上下文伪造为 isolated。

## 能力映射

1. `spawn(role, prompt) -> executor_id`：调用
   `delegate_task(goal=<完整阶段提示>, context=<项目路径和交接信息>)`，将返回值中的
   `delegation_id`（或宿主提供的等价句柄）登记为 `executor_id`。
2. `wait(executor_id)`：等待 Hermes 的 delegation 完成通知；不得轮询或猜测句柄。
3. `close(executor_id)`：Hermes 会在 delegation 完成后回收子 Agent，不要求额外关闭。
4. `spawn_helper(role, bounded_prompt) -> executor_id`：同样调用
   `delegate_task`，并在提示中限定只读范围；用返回的 delegation 句柄登记 helper。

REQUIREMENT 和 SUMMARY 仍由协调者执行。每个 DESIGN、IMPLEMENT、REVIEW、TEST
阶段必须新建独立 delegation；子 Agent 只能通过完整阶段提示和允许的上游产物获得上下文。

## 子 Agent 能力边界

1. 子 Agent 是全新独立会话，只继承父级 toolset 中未被禁用的工具，不继承
   `delegate_task`、`clarify`、`memory`、`send_message`、`cronjob_manage`（以宿主实际
   返回为准）。阶段 Agent 不能再派生子 Agent，也不能用 `send_message` 回传中间结果，
   只能在 delegation 完成时一次性返回；需要并行或重试时，由协调者在当前会话重新调用
   `delegate_task` 并登记新的 `delegation_id`。
2. 子 Agent 没有 `memory` 工具，跨阶段记忆只能靠完整阶段提示、允许的上游产物和
   `artifacts/<task_slug>/workflow-state.json`；不得假设子 Agent 记得上一阶段的口头约定
   或本会话的中间结论。
3. MCP 工具虽可随 toolset 继承，但每个 MCP server 的身份由宿主侧静态配置决定，
   阶段 Agent 不能假设自己带上了调用者的 per-user 身份。涉及写操作或审批时，先确认
   身份；缺身份就 fail-closed，记录原因并停在当前阶段，不得以默认身份继续写。
4. 宿主对 delegation 的深度与并发有上限，协调者不得在阶段内再扇出多层 delegation。

## 安装和入口

在项目根目录执行：

```bash
loopforge skills install hermes
hermes skills trust .
```

重启或重新打开 Hermes 会话后使用 `/devflow <需求>`。任务状态和产物保存在
`artifacts/<task_slug>/`，与其他 Portable 宿主相同；卸载使用
`loopforge skills uninstall hermes`，只删除 LoopForge 自己未修改的托管文件。
