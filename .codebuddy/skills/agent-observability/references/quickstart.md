## Quickstart

### 安装

将 `templates/settings-hook.json` 合并到 `~/.codebuddy/settings.json` 或 `<project>/.codebuddy/settings.json`。

必须启用 hooks：`SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse`、`Stop`。

### 查看输出

完成一次正常会话后检查：
- `.codebuddy/skills/agent-observability/logs/metrics.ndjson`
- `.codebuddy/skills/agent-observability/logs/.state.json`

重点字段：
- `event=tool`：工具耗时、`skill`、`rule`
- `event=usage`：`tokens`、`model`、`cost_usd`
- `event=stop`：会话尾部 flush 与总成本

### 常见问题

- 没有日志：检查 hook 命令路径。
- `ms` 为空：通常是 pre/post 未配对。
- 没有 `usage`：`transcript_path` 缺失、tail 无 usage，或增量已去重。
- AgentLens 未启用：检查 `.state.json` 的 `_agentlens.enabled` / `last_error`。
