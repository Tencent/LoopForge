---
description: Git 准备：拉取最新代码并创建新分支
argument-hint: [分支名] [基准分支(默认自动检测)]
allowed-tools: ["execute_command", "ask_followup_question"]
---

# /git-prepare

## 用法
```
/git-prepare feat/my-feature
/git-prepare feat/my-feature develop
```

拉取最新代码并创建新开发分支。独立于 devflow 主流程，可在 `/start-devflow` 前使用。

## 参数

| 参数 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| 分支名 | 是 | — | 新分支名称（如 `feat/xxx`、`fix/xxx`） |
| 基准分支 | 否 | 自动检测 | 从哪个分支拉取并创建（如 `develop`、`main`、`master`） |

## Main Agent 动作

### 第一步：参数解析

1. 提取分支名（必填，未传则提示用户输入）
2. 提取基准分支；未传时标记为“待自动检测”

### 第二步：检查工作区状态

```bash
git status --porcelain
```

- 有未提交更改 → 提示用户选择：stash / commit / abort
- 无更改 → 继续

### 第三步：拉取远端并确定基准分支

```bash
git fetch origin
git symbolic-ref --quiet --short refs/remotes/origin/HEAD
```

- 用户已传基准分支 → 使用用户值
- 否则优先使用 `origin/HEAD` 指向的分支，并去掉 `origin/` 前缀
- 若远端未配置 HEAD → 依次检查本地 `main`、`master`
- 仍无法确定 → 停止并询问用户，不猜测其他分支

### 第四步：更新基准分支

```bash
git checkout {基准分支}
git pull origin {基准分支}
```

### 第五步：创建新分支

```bash
git checkout -b {分支名}
```

### 完成提示

输出：已从 `{基准分支}` 创建并切换到新分支 `{分支名}`。可以开始开发或执行 `/start-devflow`。

## 注意事项

- 所有 git 命令使用 `requires_approval: false`（工作区内安全操作）
- 不会自动 push 新分支到远端（创建仅在本地）
