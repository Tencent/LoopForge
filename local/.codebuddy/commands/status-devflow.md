---
description: 查看 devflow 工作流当前进度
argument-hint: TASK_SLUG 或 TASK_ID
allowed-tools: ["read_file", "list_dir", "search_content"]
---

# /status-devflow

## 用法
```
/status-devflow {TASK_SLUG}
/status-devflow {TASK_ID}
```

## 参数识别

- 优先尝试按 TASK_SLUG 匹配：`{artifacts_root}/{TASK_SLUG}/workflow-state.json`
- 若未匹配，按 TASK_ID 扫描 `{artifacts_root}/*/workflow-state.json`，匹配 `task_id` 字段
- 两者均未匹配 → 输出"未找到对应工作流"并终止

## Main Agent 动作

1. **解析参数**：提取 TASK_SLUG 或 TASK_ID（必填，空则终止）
2. **定位状态文件**：按上述参数识别规则定位 `workflow-state.json`
3. **读取 JSON**：提取关键字段
4. **输出一行进度摘要**：

```
task_slug: {task_slug} | stage: {current_stage} | status: {status} | retry: {retry_count} | last_error: {last_error || "none"}
```

5. 若存在已完成阶段，追加各阶段状态简表：

```
| 阶段 | 状态 | 产物路径 |
|------|------|---------|
| TASK-02 | completed | {artifact_path} |
| ... | ... | ... |
```

## 注意

- 本命令为只读操作，不修改任何文件
- 不触发调度循环
