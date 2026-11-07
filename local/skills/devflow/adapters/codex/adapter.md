# Codex 适配器

Codex 提供 `spawn_agent`、`send_message` 和 `wait_agent` 时使用。

1. 确认 `devflow-clarify-requirements` 可发现；缺失时停止并报告，不自创替代访谈。
2. REQUIREMENT 执行 `prepare --emit-prompt → start`，由主 Agent 调用 `devflow-clarify-requirements` 并填写需求报告，再校验和 `finish`；不得创建需求分析 Agent。
3. route 中其他 Agent 阶段先用 `prepare --emit-prompt` 一次完成准备和提示生成，然后创建全新通用 Agent；逻辑角色由提示注入，不要求宿主存在同名 Agent 定义。
4. 将 `spawn_agent` 返回的真实 ID 用 `assign --executor-type dynamic --dispatch-tool spawn_agent` 登记并调用 `start`，等待交接、校验后调用 `finish`。协调者不得代写。
5. 不同阶段使用不同 ID；同阶段重试可复用原 Agent。REVIEW 前不得使用 `devflow-code-reviewer`。

只读检索助手先生成有边界提示，再创建并通过 `helper --executor-type dynamic --dispatch-tool spawn_agent` 登记。

协作工具不可用时先报告限制；只有明确授权后才能降级为 `single-context`。
