# /abort-devflow

定位指定任务的 `workflow-state.json`，将 `summary.status` 更新为 `aborted`，记录时间
和用户给出的原因，并停止继续派发新阶段。保留状态和已有产物供审计；不得删除项目
文件或其他任务数据。当前宿主仍有对应 subagent 运行时，只停止该精确任务。
