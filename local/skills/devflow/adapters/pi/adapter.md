# Pi 适配器

pi 构建在 Agent Skills 之上，通过 `npm:pi-subagents` 扩展提供 `subagent` 工具。使用前先安装：

```bash
pi install npm:pi-subagents
```

没有该工具时不存在可用的生命周期 API，只能按 [runtime-core.md](../../references/runtime-core.md)
降级为 `single-context`，不得把降级状态标成 `isolated`。

## 能力映射

`subagent` 前台调用（`async: false`）会启动一个独立子会话并在返回前阻塞，属于 `spawn` 拓扑：

1. `spawn(role, prompt) -> executor_id`：调用
   `subagent { agent: "delegate", task: "<完整阶段提示>" }`，返回内容中的
   `Mission: <uuid> (completed)` 就是宿主签发的 `executor_id`。
2. `wait(executor_id)`：前台调用本身就是阻塞的，返回即子会话完成。
3. `close(executor_id)`：前台模式无需显式关闭；异步模式下用 pi-subagents 的 fleet/stop 能力。
4. `spawn_helper(role, bounded_prompt) -> executor_id`：调用只读内置 agent，例如
   `subagent { agent: "scout", task: "<有边界的检索提示>" }`，并把返回的 `Mission` UUID 作为 helper id。
5. `send(executor_id, message)`：前台模式不支持对已结束子会话追加消息。需要补充或重试时，
   重新 `subagent` 一个全新实例并重新 `assign`，不得复用已结束的 Mission id。

## 使用步骤

1. 确认已安装 `pi-subagents`，且当前会话能看到 `subagent` 工具；缺失时停止并报告，
   不得自创替代工具。
2. medium/large 使用 `--execution-mode isolated --host-adapter pi` 初始化。
3. REQUIREMENT 执行 `prepare --emit-prompt → start`，由主 Agent 调用 `devflow-clarify-requirements`
   并填写需求报告，再 `finish`；不得创建需求分析 subagent。
4. route 中其他 Agent 阶段先用 `prepare --emit-prompt` 一次生成完整提示，再调用
   `subagent { agent: "delegate", task: "<提示>" }` 创建全新子会话，从返回的 `Mission: <uuid>`
   取出 `executor_id`，用 `assign --executor-type dynamic --dispatch-tool subagent` 登记，再 `start`，
   等待返回后校验产物并 `finish`。协调者不得代写 isolated 阶段产物。
5. 只读检索使用 `subagent { agent: "scout", task: "<有边界提示>" }`，并以 `devflow-code-explorer` 或
   `devflow-knowledge-retriever` 作为审计角色用 `helper --executor-type dynamic --dispatch-tool subagent`
   登记。REVIEW 前不得使用 `devflow-code-reviewer`。

不同阶段使用不同的 `Mission` UUID。由于前台模式不支持 `send`，同阶段重试必须重新派发新实例并重新登记，
不得在状态里复用已结束的 id。
