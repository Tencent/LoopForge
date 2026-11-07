# Main Bot 流程交接与交付验收

> 历史设计记录：旧角色群协作、群清单、自动跟进及切回 SOP 写入口已移除，不再按本文启动或操作。当前实现与环境选择见 [对话工作区](conversation-workspaces.md)。

自由协作的 `waiting` 表示等待后续指令，不代表存在人工审批门禁。节点执行结束、验证结果和用户验收是三个独立事实。

- `runs_control_get` 返回当前模式、等待原因、可用动作、原 SOP 可继续的节点和验收凭据。
- `runs_group_sop` 接受 `{nodeId}`，归档最新群聊交接，从指定的原 SOP 节点继续；已有文件不会回退。
- `runs_group_complete` 接受 `{reason}`，依据用户明确验收接受协作交付，不重跑 SOP。保留实际节点执行、测试未运行/失败及群成员执行历史，并在同一事务完成 run、task、issue 与操作任务。
- `runs_group_sop_status` 查询后台操作；`runs_group_sop_retry` 重试已失败的操作。

操作返回 job 仅表示受理。Bot 应提交简短回复后结束本轮，后台在回复和工作区保存结束后执行。不要在同一轮等待该 job 完成。当前用户消息、发起人和 generation 由会话网关绑定，模型不能通过参数伪造。取消、替换或未完成的回复不能推进流程；显式重试绑定新的用户消息。无人值守定时任务不能调用这三个控制写工具。

验收凭据保存在 run 的 `workflowSnapshot.groupCompletion`，包含发起人、依据、来源会话/generation/消息序号和时间；需求操作历史也记录发起人及依据。验收不会将未运行的测试改成通过，也不表示已合并 MR。

部署前可手动执行 `migrations/017_bot_sop_controls.sql`，为原交接任务表添加操作类型和消息来源字段。脚本不创建任务、不改历史数据；已由 AutoMigrate 补齐列的环境不必重复执行。

回归测试默认使用内存测试库；设置 `AGENTFLOW_BOT_SOP_TEST_MYSQL_DSN` 可在专用本地 Docker MySQL 上验证完整工具调用、事务闭环、重复请求、取消后的显式重试和交接时序。该测试只接受 `127.0.0.1` 和 `agentflow_handoff_jobs_test_` 前缀的临时数据库，不需要生产凭据或模型调用。
