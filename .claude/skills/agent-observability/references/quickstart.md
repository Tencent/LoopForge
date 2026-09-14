## Quickstart

### 安装

将 `templates/settings-hook.json` 合并到 `~/.claude/settings.json` 或 `<project>/.claude/settings.json`。

必须启用 hooks：`SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse`、`Stop`。

### 查看输出

完成一次正常会话后检查：
- `.claude/skills/agent-observability/logs/metrics.ndjson`
- `.claude/skills/agent-observability/logs/.state.json`

### 指定数据源路径（可选）

`build_dashboard_data.py` 默认从 `<skill_root>/logs/metrics.ndjson` 与 `<skill_root>/logs/.state.json` 读取，**不传参数时行为完全不变**。

如需从其它位置读取，可用 `--metrics-path` / `--state-path` 覆盖：

```bash
python3 scripts/build_dashboard_data.py \
  --metrics-path ~/Downloads/metrics.ndjson \
  --state-path  ~/Downloads/.state.json \
  --out dashboard/dashboard-data.json
```

注意：

- 两个参数都留空（或省略）时回退到 `<skill_root>/logs/` 下的默认文件。
- 路径支持 `~` 展开（如上例的 `~/Downloads/...`）。
- 输出 `dashboard-data.json` 的 `source.metrics_ndjson` / `source.state_json` 会**如实反映实际读取到的路径**，覆盖后自然指向你给定的文件，便于核对数据来源。

重点字段：
- `event=tool`：工具耗时、`skill`、`rule`
- `event=usage`：`tokens`、`model`、`cost_usd`
- `event=stop`：会话尾部 flush 与总成本

### 常见问题

- 没有日志：检查 hook 命令路径。
- `ms` 为空：通常是 pre/post 未配对。
- 没有 `usage`：`transcript_path` 缺失、tail 无 usage，或增量已去重。
- AgentLens 未启用：检查 `.state.json` 的 `_agentlens.enabled` / `last_error`。
