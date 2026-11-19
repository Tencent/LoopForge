# LoopForge DSH 插件

这个插件是一个适用于 DSH 0.2.1 及后续 bundle-compatible 版本的 Host bundle，把
LoopForge Local 接入 DeepSeek Harness（DSH）Profile。它复用项目中
`.dsh/skills/devflow` 的 Portable 工作流，不复制工作流规则或产物模板。

## 本地安装

先在目标项目安装 DSH Portable Skill：

```bash
loopforge skills install dsh
```

所用 CLI 版本必须支持 `dsh` 目标；不同发布版本支持的宿主可能不同，见
[Local 使用说明](../../docs/local/usage.zh-CN.md#快速开始)。

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

## 使用

完整工作流由单独安装的 `devflow` Skill 提供。在 DSH 的技能工具或技能目录中调用
`devflow` 并提供需求，让 Agent 按 Skill 执行流程。

重启 DSH 后，这个 bundle 提供以下任务控制命令和状态工具：

- `/loopforge start <需求>`：初始化一个 medium 任务并显示状态文件位置。
- `/loopforge status`：查看最近任务、当前阶段和待审批门禁。
- `/loopforge result`：查看任务状态和产物目录。
- `/loopforge approve [阶段]`：批准当前人工门禁；REQUIREMENT 会强制带用户确认。
- `loopforge_status`：供 DSH Agent 读取结构化状态。

`/loopforge start` 只初始化任务，后续阶段仍由 Agent 按 `devflow` Skill 推进。
这个 bundle 注册的是 `/loopforge` 命令和 `loopforge_status` 工具，不注册 `/devflow` 命令。

阶段 Agent 使用 DSH 的 `subagent` 工具。没有该工具时，工作流必须按协议记录
`single-context` 降级原因，不能伪造隔离执行器。

## 卸载

```bash
dsh plugin --profile web remove loopforge-dsh
loopforge skills uninstall dsh
```
