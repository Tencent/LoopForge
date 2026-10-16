# 平台实测套件

测试真实部署的 HTTP/API、Runner、MySQL、模型执行和浏览器页面。默认目标为 `http://127.0.0.1:8090`；请显式选择自己的验收环境，用例会创建、修改并清理测试资源。

```bash
cd e2e
npm ci
npx playwright install chromium
# 先在平台个人设置创建 API Token，通过当前终端环境安全传入。
export AGENTFLOW_E2E_BASE=http://127.0.0.1:8090
./run.sh api
./run.sh ui
```

| 变量 | 用途 |
| --- | --- |
| `AGENTFLOW_E2E_TOKEN` | 测试账号的 API Token，必须显式提供；不读取其他应用的本机配置 |
| `AGENTFLOW_E2E_BASE` | 验收环境，默认 localhost:8090 |
| `AGENTFLOW_E2E_MODEL` | 显式设为 `1` 启用付费模型测试，默认关闭 |
| `AGENTFLOW_E2E_MODEL_ID` | 真实 Agent 节点使用的模型 ID，例如 `deepseek-v4.1-flash` |
| `AGENTFLOW_E2E_EXECUTOR` | 执行器，默认 `codebuddy` |
| `AGENTFLOW_E2E_SANDBOX` | 沙箱，默认 `local`，仅对可信的验收任务使用 |
| `AGENTFLOW_E2E_PROJECT` | 测试项目，默认 `e2e-auto` |
| `AGENTFLOW_E2E_VIEW_PROJECT` | 第二个页面巡检项目，默认 `welcome` |
| `LOOPFORGE_TEST_USER` / `LOOPFORGE_TEST_PASSWORD` | UI 真实密码登录；用户名默认 admin，本地密码缺省读取 `.local/admin-password` |
| `AGENTFLOW_E2E_KEEP` | 设为 `1` 保留资源用于诊断 |
| `E2E_CHROMIUM` | 可选的 Chromium 路径 |

API Token 和 UI 登录账号应属于同一用户。模型 Key 在平台个人设置中配置，网络环境必须匹配 Key 所属版本。测试不会将模型 Key 写进测试源码。不要公开 `.auth/`、原始失败 trace 或本地凭据文件。

## 覆盖范围

- `api/00-smoke`：健康、匿名和伪造身份拒绝、Token 权限边界及约 40 个读取接口。
- `api/10-catalog`：规则、技能文件、MCP、环境、沙箱、角色和 SOP CRUD，修订/恢复/克隆、引用保护、模板采用、仓库登记。
- `api/20-issues`：需求、评论、审计、澄清门禁、派发、关闭与重开。
- `api/30-runs`：脚本执行、产物与工作区、审批/驳回、返工、重试、暂停/恢复、取消、子 SOP 与 SSE。
- `api/35-runner-mode`：下载、启动并绑定真实 Runner，验证跨节点产物交接和失败场景，结束后恢复默认执行机器。
- `api/36-cloud-pool`：无个人 Runner 时的 Worker/执行池路径与共享沙箱。
- `api/40-model`：真实模型生成文件并通过脚本验证、运行评估、助手问答、会话保存和 SSE。
- `api/45-delivery-model`：四个真实模型角色完成规划、代码修复、独立评审和交付；真实 Python 测试、人工审批及验收后代码哈希检查。
- `api/66-sop-handoff`：真实群聊约束持久化、交接请求幂等、后台摘要归档、返回指定 SOP 节点及人工审批后继续。需要显式启用模型测试。
- `api/50-platform`：知识审核队列、协作与派发、定时任务、助手任务、平台 MCP、凭据不泄露。外部知识库未配置时明确跳过对应检索用例。
- `ui/01-views`：真实密码会话下，在体验项目和测试项目逐页巡检；检查页面内容、控制台和失败请求，不模拟登录接口。
- `ui/02-interactions`：需求创建与真实 AI 分析、页面审批、运行详情页签、设计器编辑保存、可选知识库检索。
- `public/`：公共登录、语言/主题、真实脚本审批，以及首页草稿和真实模型对话的回归，见[运行说明](public/README.md)。

## 单独执行与产物

```bash
node --test --test-concurrency=1 api/40-model.test.mjs
npx playwright test ui/02-interactions.spec.mjs
npm run cleanup -- --all --dry-run
```

HTML 报告与截图位于 `test-results/`。每个文件登记自己创建的资源并逆序清理；`run.sh` 还会执行兜底清理。测试不创建外部 GitHub PR/Issue、不向外部知识库写回、不发送真实企业通知。涉及这些服务的行为需要独立配置和授权，不能用本地通过的结果代替外部实测。


Each `run.sh` invocation exports a unique `AGENTFLOW_E2E_RUN_TAG`. Its final sweep only removes resources belonging to that invocation. To inspect or clean an interrupted run, pass the same tag. `node cleanup.mjs --all --dry-run` lists the whole dedicated test project's leftovers; remove `--dry-run` only when no other suite is running there. Permission tests temporarily change fixture membership, so use separate projects for simultaneous acceptance runs. `api/05-cleanup-scope.test.mjs` protects an unrelated waiting workflow while sweeping another run.
