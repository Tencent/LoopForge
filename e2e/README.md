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

### 用当前代码跑

要验证本地还没合并的改动，用 `local.sh`：它用当前检出的代码编译并起一个临时平台，自带 MySQL 容器、测试用户和 Token、`generic-docker`/`local` 沙箱，以及一个本机裸仓库（给需要代码仓库的用例用），跑完全部清理。

```bash
./local.sh                                         # 全部 API 用例
./local.sh api/30-runs.test.mjs api/35-runner-mode.test.mjs
AGENTFLOW_E2E_CODEBUDDY_API_KEY=ck_... ./local.sh api/40-model.test.mjs   # 带真实模型
./local.sh all                                     # API + UI
```

全新库里没有知识库、通用模板和内置角色，依赖这些的断言在本地会跳过。`local.sh` 还会：开启仅限本机的验收登录（`AGENTFLOW_LOCAL_LOGIN_USER`，给只认网页登录态的接口用）、创建第二个非管理员用户 B 及其 Token、在表就绪后执行 `migrations/001_run_obs_generated_columns.sql`（迁移不随启动执行，缺了它运行详情的遥测筛选会 400）。

| 变量 | 用途 |
| --- | --- |
| `AGENTFLOW_E2E_TOKEN` | 测试账号的 API Token，必须显式提供；不读取其他应用的本机配置 |
| `AGENTFLOW_E2E_TOKEN_B` | 第二个**非管理员**用户的 Token，用来验证非成员、viewer、member、maintainer 的越权边界。不设置就跳过这些用例；`local.sh` 会自动设置 |
| `AGENTFLOW_E2E_BASE` | 验收环境，默认 localhost:8090 |
| `AGENTFLOW_E2E_MODEL` | 显式设为 `1` 启用付费模型测试，默认关闭 |
| `AGENTFLOW_E2E_MODEL_ID` | 真实 Agent 节点使用的模型 ID，例如 `deepseek-v4.1-flash` |
| `AGENTFLOW_E2E_EXECUTOR` | 执行器，默认 `codebuddy` |
| `AGENTFLOW_E2E_SANDBOX` | 沙箱，默认 `local`，仅对可信的验收任务使用 |
| `AGENTFLOW_E2E_MODEL_SANDBOX` | 真实模型用例使用的沙箱，镜像里要有执行器 CLI；默认同 `AGENTFLOW_E2E_SANDBOX` |
| `AGENTFLOW_E2E_REPO_CLONE_URL` | 带代码仓库的用例克隆这个仓库（只读，不推送）。不设置就跳过这些用例；`local.sh` 会自动设成本机裸仓库 |
| `AGENTFLOW_E2E_REPO_BRANCH` | 上面仓库的基线分支，默认 `main` |
| `AGENTFLOW_E2E_PROJECT` | 测试项目，默认 `e2e-auto` |
| `AGENTFLOW_E2E_VIEW_PROJECT` | 第二个页面巡检项目，默认 `welcome` |
| `LOOPFORGE_TEST_USER` / `LOOPFORGE_TEST_PASSWORD` | UI 真实密码登录；用户名默认 admin，本地密码缺省读取 `.local/admin-password` |
| `AGENTFLOW_E2E_KEEP` | 设为 `1` 保留资源用于诊断 |
| `AGENTFLOW_E2E_KNOWN_ERRORS` | 正则。已修复但目标环境尚未部署的前端报错，临时忽略，例如 `'Content Security Policy'` |
| `E2E_CHROMIUM` | 可选的 Chromium 路径 |

API Token 和 UI 登录账号应属于同一用户。模型 Key 在平台个人设置中配置，网络环境必须匹配 Key 所属版本。测试不会将模型 Key 写进测试源码。不要公开 `.auth/`、原始失败 trace 或本地凭据文件。

## 覆盖范围

- `api/00-smoke`：健康、匿名和伪造身份拒绝、Token 权限边界及约 40 个读取接口。
- `api/10-catalog`：规则、技能文件、MCP、环境、沙箱、角色和 SOP CRUD，修订/恢复/克隆、引用保护、模板采用、仓库登记。
- `api/20-issues`：需求、评论、审计、澄清门禁、派发、关闭与重开、删除（已有成功运行或执行中拒绝，取消的执行记录随需求一起删除）。
- `api/30-runs`：脚本执行、产物与工作区、审批/驳回、返工、重试、暂停/恢复、取消、子 SOP 与 SSE。
- `api/30-runs`、`api/35-runner-mode` 里的“带代码仓库的子 SOP”：父流程写文件 → 子 SOP 读到并再写一个 → 父流程读到；同时检查节点没有提交、远端没被推送，分别覆盖平台本机和执行机器。
- `api/35-runner-mode`：下载、启动并绑定真实 Runner，验证跨节点产物交接和失败场景，结束后恢复默认执行机器。
- `api/36-cloud-pool`：无个人 Runner 时的 Worker/执行池路径与共享沙箱。
- `api/40-model`：真实模型生成文件并通过脚本验证、子 SOP 里的 Agent 修改父流程的代码、运行评估、助手问答、会话保存和 SSE；助手在项目对话里新建项目时草稿不归属当前项目，确认执行草稿后下一轮不重复写入。
- `api/45-delivery-model`：四个真实模型角色完成规划、代码修复、独立评审和交付；真实 Python 测试、人工审批及验收后代码哈希检查。
- `api/50-platform`：知识审核队列、协作与派发、定时任务、助手任务、平台 MCP、凭据不泄露。外部知识库未配置时明确跳过对应检索用例。
- `api/60-spaces`：需求会话的创建校验、编辑与置顶、主时间线与话题消息、关联需求、产物版本与沙箱化预览、成员与邀请、只读面板。
- `api/62-integrations`：通知机器人（Secret 只写不回显）、外部数据源与采集 Token、业务域与项目归属历史、IM 账号绑定（只认网页登录态）。全程不连接外部 IM。
- `api/64-auth-permissions`：本机验收登录（仅回环 + 同源）、个人 Token 只能用登录态管理、登出、非成员/viewer/member/maintainer 的读写边界、助手附件仅上传者可见。
- `api/68-workspace-edit`：人工接管已停止的运行工作区：运行中不可写、viewer 不可接管、`expectedSha256` 防覆盖与路径校验、其他成员接力编辑、diff 投影、审批点上 `resume` 不能跳过审批。
- `api/69-assistant-sessions`：AI 会话保存（作者不可伪造）、改名、关联需求（乐观并发）、分支与编辑、归档、删除，项目会话的共享与权限。
- `ui/01-views`：真实密码会话下，在体验项目和测试项目逐页巡检；检查页面内容、控制台和失败请求，不模拟登录接口。
- `ui/02-interactions`：需求创建与真实 AI 分析、页面审批、运行详情页签、设计器编辑保存、可选知识库检索。
- `ui/03-workbench-settings`：AI 工作台会话列表（打开、搜索、改名、归档、删除）、个人凭证向导（格式校验、保存、清除）、Bot 新建与删除、viewer/member 看到的 Bot 页面权限、领域管理编辑与重复标识拦截。
- `public/`：公共登录、语言/主题、真实脚本审批，以及首页草稿和真实模型对话的回归，见[运行说明](public/README.md)。

## 单独执行与产物

```bash
node --test --test-concurrency=1 api/40-model.test.mjs
npx playwright test ui/02-interactions.spec.mjs
npm run cleanup -- --all --dry-run
```

HTML 报告与截图位于 `test-results/`，也可以用 `--output=<目录>` 写到别处。每个文件登记自己创建的资源并逆序清理；`run.sh` 还会执行兜底清理。测试不创建外部 GitHub PR/Issue、不向外部知识库写回、不发送真实企业通知。涉及这些服务的行为需要独立配置和授权，不能用本地通过的结果代替外部实测。


Each `run.sh` invocation exports a unique `AGENTFLOW_E2E_RUN_TAG`. Its final sweep only removes resources belonging to that invocation. To inspect or clean an interrupted run, pass the same tag. `node cleanup.mjs --all --dry-run` lists the whole dedicated test project's leftovers; remove `--dry-run` only when no other suite is running there. Permission tests temporarily change fixture membership, so use separate projects for simultaneous acceptance runs. `api/05-cleanup-scope.test.mjs` protects an unrelated waiting workflow while sweeping another run.
