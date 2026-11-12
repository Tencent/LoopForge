# NLForge DSH 插件

这个插件是一个适用于 DSH 0.2.1 及后续 bundle-compatible 版本的 Host bundle，把
NLForge Local 接入 DeepSeek Harness（DSH）Profile。它复用项目中
`.dsh/skills/devflow` 的 Portable 工作流，不复制工作流规则或产物模板。

## 本地安装

先在目标项目安装 DSH Portable Skill：

```bash
loopforge skills install dsh
```

再从 LoopForge 源码目录把 bundle 加入目标 DSH profile（`web` 也可以换成你自己的
profile 名称）：

```bash
dsh plugin --profile web add -w /path/to/LoopForge/local/dsh-plugin
```

安装器会把 `@tencent/loopforge-dsh` 写入 profile 的 `dsh.profile.bundles`，并由
`cordis.patch.yml` 插入插件行；旧版 `dsh-plugin.json` 清单不会被当前 DSH Loader
读取。安装后可用下面的只读检查确认 bundle 已组合：

```bash
dsh --profile web --dump-config
dsh --profile web --dump-config-schema
```

输出应包含 `@tencent/loopforge-dsh`、`loopforge-dsh`，且 schema 的
`diagnostics` 为空。

重启 DSH 后可用：

- `/devflow <需求>`：直接按 Skill 执行完整工作流。
- `/loopforge start <需求>`：初始化一个 medium 任务并显示状态文件位置。
- `/loopforge status`：查看最近任务、当前阶段和待审批门禁。
- `/loopforge result`：查看任务状态和产物目录。
- `/loopforge approve [阶段]`：批准当前人工门禁；REQUIREMENT 会强制带用户确认。
- `loopforge_status`：供 DSH Agent 读取结构化状态。

阶段 Agent 使用 DSH 的 `subagent` 工具。没有该工具时，工作流必须按协议记录
`single-context` 降级原因，不能伪造隔离执行器。

## 卸载

```bash
dsh plugin --profile web remove loopforge-dsh
loopforge skills uninstall dsh
```
