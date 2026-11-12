# Hermes Agent 适配器

Hermes Agent 通过项目内的 Agent Skills 目录加载 NLForge：安装器写入
`.hermes/skills/`，不会修改 Hermes 用户目录或 `config.yaml`。首次在该项目运行
Hermes 时，需要按宿主的信任提示执行 `hermes skills trust`，之后 `/devflow` 才会
出现在会话中。

## 版本和能力边界

适配器支持当前稳定版以及后续仍提供项目级 `.hermes/skills/`、`hermes skills trust` 和
`delegate_task` 的 Hermes Agent 版本，不绑定私有版本号。Hermes 的插件和 skill API
仍在演进；NLForge 只依赖这三个公开能力，不读取 Hermes 私有状态文件。若当前会话没有
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

## 安装和入口

在项目根目录执行：

```bash
loopforge skills install hermes
hermes skills trust .
```

重启或重新打开 Hermes 会话后使用 `/devflow <需求>`。任务状态和产物保存在
`artifacts/<task_slug>/`，与其他 Portable 宿主相同；卸载使用
`loopforge skills uninstall hermes`，只删除 NLForge 自己未修改的托管文件。
