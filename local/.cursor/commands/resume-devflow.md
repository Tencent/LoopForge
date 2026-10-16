# /resume-devflow

读取 `artifacts/<task-slug>/workflow-state.json`，保留已有产物和 task_slug。
根据 `current_stage`、`last_event` 和 `next_target` 找到首个未完成阶段，使用宿主
Agent 工具创建该阶段对应的全新 `devflow-<role>` subagent，并把状态路径、允许输入、
必需输出和失败信息一次性放入完整提示。阶段返回后更新状态并继续路由。

不得扫描或恢复其他任务的 subagent，不得删除已有产物，不得把失败阶段标成完成。
