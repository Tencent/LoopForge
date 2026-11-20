# 资产进化 API

进化中心按项目查看运行问题，生成角色 Prompt／规则正文的候选，通过历史回放、人工确认的灰度和发布后观察迭代资产。默认自治级别为 L2。

## 首版范围与权限

- 项目成员可读；诊断、生成、队列处理及发布需要项目写权限。
- 仅当前项目的私有角色和规则允许生成、试用和发布。共享／平台资产可以诊断；需要修改时先复制为项目私有资产。已有共享资产提案仍可取消，历史版本仍可按原权限人工回滚。
- 回放只接受未绑定仓库、由角色和审批节点组成的流程。脚本、命令、子 SOP、角色生命周期脚本、命令／MR 验证、节点外部凭据映射均拒绝。会检查当前工作流定义，不能用旧快照绕过。
- 不支持的样本在调用改进器前使提案失败，不会静默丢弃该样本后宣称验证通过。候选生成期间的资产变化仍按原有 CAS／基线检查处理。
- 回放仅解析模型凭据，清除 Git、部署扩展凭据、所有 MCP 及其配置文件；关闭自动浏览器 MCP。执行机器必须使用个人模型 Key，不能使用本机登录凭据。业务运行与真实灰度保留原有能力。
- 这些限制关闭平台已知的外部写入通道，**不提供任意 CLI 命令的网络出口隔离**。仅回放可信的文档流程；需要网络隔离或 Git／MR 回放的任务仍不在首版范围内。

## 接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/evolution/overview?projectId=…&days=14` | 概览、资产健康度、队列与进行中的修改 |
| POST | `/api/evolution/diagnose` | 检查最近运行，正文 `{projectId, days}` |
| GET | `/api/evolution/findings?projectId=…` | 问题记录 |
| GET | `/api/evolution/findings/{id}/runs?cluster=…` | 问题分组的原始运行证据 |
| POST | `/api/evolution/findings/{id}/dismiss` | 忽略问题，正文 `{reason}` |
| POST | `/api/evolution/findings/{id}/snooze` | 暂缓问题，正文 `{days}` |
| GET | `/api/evolution/proposals?projectId=…` | 提案列表 |
| POST | `/api/evolution/proposals` | 从 `{findingId, model?}` 或 `{projectId, assetKind, assetId, model?}` 生成候选 |
| GET | `/api/evolution/proposals/{id}` | 候选正文、回放与观察结果 |
| GET | `/api/evolution/proposals/{id}/replays/{rid}/compare` | 原运行与回放的节点对比 |
| POST | `/api/evolution/proposals/{id}/approve` | `{mode:"canary",percent,minRuns,note?}` 开始灰度；`{mode:"release",note?}` 发布 |
| POST | `/api/evolution/proposals/{id}/canary/traffic` | `{percent}` 调整新运行的灰度比例（1–99） |
| POST | `/api/evolution/proposals/{id}/reject` | `{note?}` 拒绝提案或停止灰度 |
| POST | `/api/evolution/queue/{id}/start` | 重新授权后读取最新正文，生成并验证下一项 |
| POST | `/api/evolution/queue/{id}/reject` | 取消排队 |
| GET | `/api/evolution/policies?projectId=…` | 自治策略 |
| PUT | `/api/evolution/policies/{kind}/{id}` | `{level}` 设置 L0–L3 |
| GET | `/api/evolution/assets/{kind}/{id}/versions` | 正文版本、当前 Hash 与漂移状态 |
| POST | `/api/evolution/assets/{kind}/{id}/rollback` | `{version,note?}` 人工回滚 |
| GET | `/api/evolution/samples?projectId=…` | 钉选回归样本 |
| POST | `/api/runs/{id}/feedback` | `{detail,nodeId?}` 提交人工反馈 |
| POST / DELETE | `/api/runs/{id}/pin-sample` | `{note?}` 钉选／移除回归样本 |
| GET | `/api/runs/{id}/asset-pins` | 运行启动时的资产 Hash 与版本记录 |

`assetKind`／`kind` 仅支持 `agent`、`rule`。生成接口新提案返回 `202`；同一问题已有当前提案时返回 `200`；资产被其他改进占用时返回 `202` 的 `{status:"queued",queueId,queue}`。同一资产的生成、验证、灰度和发布后观察串行，下一项不会自动启动。

概览新增 `autoEnabled` 表示部署是否启用自动进化。健康度中的 `readOnlyReason` 表示资产仅供诊断。前端应按这两个字段禁用不适用的操作，服务端同时强制校验。

未授权返回 `401/403`，找不到资源返回 `404`，不支持的资产范围、禁用的自动进化、过期基线或并行状态冲突返回 `409`。回放范围检查发生在异步候选任务中时，错误记录在提案的 `error`，状态为 `failed`。

## L3 与部署

`AGENTFLOW_EVOLUTION_AUTO_ENABLED` 默认关闭。关闭时 API 拒绝新设置 L3，已有 L3 策略也不会自动开始灰度、发布或回滚；仍可人工操作。启用需要在所有 API／执行实例一致设置为 `true`。

1. 先停止生成和灰度操作，等待业务运行结束，停止旧执行实例。不要让旧版本执行新版本的回放或灰度运行。
2. 如之前试用过旧版进化，先人工检查并取消共享资产的当前提案／灰度，完成其回放清理；保留历史数据。
3. 首次安装可由增量 DDL 创建缺失表；手工建表时按 `043_asset_evolution.sql`、`045_evolution_asset_queue.sql` 顺序执行。后者先输出历史并行冲突，发现记录应先处理；再进行结构升级。已有同类迁移无需重复改数据。
4. `044_product_delivery_review_rework.sql` 是独立、手工执行的数据变更：通用产品交付评审不通过时回到方案设计，最多返工两次。它不会自动运行，不修改已采用的项目 SOP 副本。
5. 所有平台和 Runner 实例统一升级后，先保持 L2，用项目私有资产与无仓库文档流程验收。L3 如需使用，再单独显式启用。

迁移以文件全名为准；039–041 替代开发分支早期的 027／029／033 进化脚本编号。回滚二进制不会删除新增表，也不会撤销已发布的资产内容；需要撤销正文时先在进化中心人工回滚。
