---
description: Git 提交：暂存、提交并推送代码变更
argument-hint: [提交信息]
allowed-tools: ["execute_command", "ask_followup_question", "read_file"]
---

# /git-commit

## 用法
```
/git-commit "feat: add comment API endpoints"
/git-commit
```

暂存所有变更、生成提交信息并推送到远端。独立于 devflow 主流程，可在流程完成后使用。

## 参数

| 参数 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| 提交信息 | 否 | 自动生成 | commit message（遵循 Conventional Commits 格式） |

## Main Agent 动作

### 第一步：检查当前状态

```bash
git status
git diff --stat
```

- 无变更 → 提示"没有需要提交的变更"并结束
- 有变更 → 继续

### 第二步：生成提交信息（未传时）

如果用户未提供提交信息：
1. 读取 `git diff --stat` 分析变更范围
2. 如果存在 `workflow-state.json`，读取 task_slug 和需求描述
3. 自动生成 Conventional Commits 格式的提交信息
4. 展示给用户确认

### 第三步：暂存并提交

```bash
git add -A
git commit -m "{提交信息}"
```

### 第四步：推送到远端

```bash
git push origin HEAD
```

- 如果是新分支（远端不存在），使用 `git push -u origin HEAD`
- 推送失败 → 提示用户处理冲突

### 完成提示

输出：已提交并推送到远端分支 `{当前分支}`。变更摘要：{N} 个文件变更。

## 注意事项

- `git add` 和 `git commit` 使用 `requires_approval: false`
- `git push` 使用 `requires_approval: true`（推送到远端需确认）
- 提交信息遵循 Conventional Commits 格式：`type(scope): description`
  - type: feat / fix / docs / style / refactor / test / chore
