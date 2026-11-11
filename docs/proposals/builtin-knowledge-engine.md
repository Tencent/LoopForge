# 内置知识引擎：阶段一设计与最小切片

状态：可派发的实现设计。本文只定阶段一里先做的最小切片，不写实现代码。

本文不记录仓库地址、内部基础设施或人员。移植源指只读的既有知识引擎，下文源文件都相对该引擎的 `platform/` 目录。实施任务单给出移植源根目录，不要把该路径写进代码、注释、测试或界面。

## 1. 已定约束

这些约束已经确定，实现不得改口：

- 知识库是平台进程内的能力，不保留独立知识服务，也不做跨服务的项目绑定。移植源里的 `internal/projectkb`、`internal/store/project_bindings.go`、`internal/api/project_bindings.go`，以及 `internal/index/project_sources.go`、`internal/knowledge/project_sources.go`、`internal/search/project_sources_test.go` 整段不移植。
- 不保留外部知识库模式，没有过渡期。外部客户端和「地址加 Token」配置直接删除。已确认没有存量在用这种模式，不迁移旧连接数据。
- 部署按多实例加负载均衡、共用一个 MySQL。服务路径（检索、读当前原文）只读 MySQL，任何一个实例都能做。Git 工作副本是同步缓存，只由持有租约的实例使用，丢了重新克隆。不引入共享文件系统、Elasticsearch 或 PostgreSQL。
- 项目知识库默认由平台托管，事实源是 MySQL 加修订历史，建项目即可用。外部 Git 只是可选来源；对外部来源，MySQL 里是当前已索引版本的服务副本。
- 表分两组，不建跨组外键。内容面可以通过单独的连接配置指向另一个 MySQL，默认与主库相同。
- 范围只有 Markdown。附件不在本阶段。
- 关键词检索先走 MySQL 全文索引（ngram）。内存倒排只作为对照和备选，不作为切片的默认检索。
- 向量、Qdrant、embedding、`kb_grep`、引用记录持久化、自动沉淀、治理面板都不在最小切片。它们仍按原阶段划分，见第 6 节。
- 现有知识中心的审核队列、项目授权、Agent 只读代理和角色开关保留。换掉的是 `Upstream` 后面的远程客户端。
- 迁移只写 `migrations/` 下的 SQL，由安装步骤或运维手动执行。`repo.Open` 只做连接和补缺失表的增量 DDL，不在代码里做数据迁移或 seed。
- 不在 `Store` 上新写领域方法。领域方法挂在领域 Repo 上，由 `newStore` 匿名嵌入后提升。

2026-11-04 与 2026-11-10 的两次代码提交之后，移植源各包的当前行数如下（实现不含测试）。方案里旧的约 3300 行口径作废，以本表为准。

| 包 | 实现行 | 测试行 | 切片里的处置 |
| --- | ---: | ---: | --- |
| `chunk` | 173 | 71 | 原样移植 |
| `lex` | 169 | 73 | 移植为非默认的关键词适配器，不接入默认检索 |
| `gitsync` | 635 | 578 | 改写后移植：只做同步缓存，见 2.2 |
| `index` | 850 | 514 | 重写落库与建索引；frontmatter 解析随包移植 |
| `search` | 266 | 265 | 重写为 MySQL 全文；向量融合不移植 |
| `knowledge`（读边界） | 546 | 905 | 不移植这个包。读原文改为读 MySQL 正文 |
| `embed` | 442 | 158 | 阶段二 |
| `qdrant` | 165 | 45 | 阶段二 |
| `governance` | 637 | 590 | 阶段四。切片只用其中与 frontmatter 校验重复的那部分时，以 `index/frontmatter.go` 为准，不搬治理报告 |
| `mcplite` | 503 | 532 | 不移植。工具面仍由现有代理和 Main Bot 目录承担 |
| `store` | 2182 | 1042 | 不移植。Postgres 模型改写成第 4 节的 MySQL 表 |
| `api`、`kbaccess` | 2662 | 2954 | 不移植。权限用平台项目成员 |
| `projectkb` 与 project binding | — | — | 不移植 |
| `agent`、`worker`、`manageddsh`、`dshclaim`、`client`、`config`、`settings` | — | — | 不移植 |

`store` 的 `documents` 表现在只存路径、标题、行数、指纹和 commit，正文不在库里。`chunks.text` 是切块，不能还原带 frontmatter 的原文。这是必须改掉的前提，不是可选优化。

## 2. 移植时要改写的行为

### 2.1 正文落 MySQL

同步或托管写入的顺序是：读出完整 Markdown，写入内容面的文档正文，再用现有分块规则切出索引块。检索、读原文、以后的精确匹配都只读这两张内容表。索引块可以删掉后从正文重建。

托管库没有 Git commit。修订号按文档计数：每篇文档首次写入从 1 起，同一路径覆盖时只递增这篇文档的号，写在 `knowledge_revisions`；来源上的 `head_revision` 记最近一次写入的号。出处字符串仍是 `source@<版本>:path#Lx-Ly`，托管来源的版本是文档修订号。JSON 字段名保持现有的 `commit`，不新增字段。托管库把修订号的十进制字符串放进 `commit`，例如 `"3"`。Git 来源放 40 位 commit。Agent 把这个字段当不透明版本号使用。

### 2.2 Git 只是同步缓存

`gitsync.Sync`、`CollectMarkdown` 和「把相对路径限制在工作副本内再读文件」留下，供持有租约的实例把变更文件读进内存，然后立刻写入 MySQL。工作副本放在与沙箱工作区无关的独立缓存目录，默认 `$HOME/.agentflow/knowledge-cache/{sourceID}`（本机 HOME 是用户目录，容器部署的 HOME 指向可写数据目录），可用可选的 `AGENTFLOW_KNOWLEDGE_CACHE_DIR` 覆盖；不进数据库，也不进共享卷。缓存丢了可重建，沙箱回收不得触碰它。

从服务路径删除这些调用：

- `knowledge.Reader.Fetch` / `FetchVersion` 通过 `ReadSourceFileAtCommit` 读 Git 对象或工作树。切片的 `Fetch` 只按 `source_id + path` 读 `knowledge_documents.body`，再按行截取。
- 索引过程里「先写库、再从工作树回读正文」的双路径。读进内存的字节就是要落库的正文，不要再打开工作树给检索用。
- `gitsync` 里为特定 Git 包装器准备的 `--task-authorized` / `--user-approved`（`authorizedGitArgs`，测试在 `gitsync_auth_test.go`）。平台用普通 `git`，不移植这层参数。

`gitsync.CommitAndPush`（提交并推送写回分支）留到阶段一补全，见第 10 节；切片验收时不调用它。

### 2.3 托管库是事实源

托管来源的 `kind=hosted`，没有远程地址。写入是：校验相对路径必须以 `.md` 结尾、补上移植源 `index/frontmatter.go` 已有的 frontmatter 校验、在同一内容库事务里写正文、追加修订、替换该路径的索引块、更新来源的修订号和篇数。这一步在处理请求的进程里做完，不领取 Git 租约。

创建项目时保证该项目有且只有一个自动创建的项目层知识库，并绑定到该项目。绑定权限是可提交。已有项目（包括安装种子里的欢迎项目）不会走创建接口，所以知识中心打开时再做一次幂等保证，而不是写启动迁移。

### 2.4 不搬进切片的移植源行为

- 向量写入、混合检索、RRF、降级标记的向量分支。切片的 `Search` 固定 `mode=lexical`、`degraded=false`。使用规范里关于「语义检索降级」的句子改成只描述关键词检索，避免 Agent 等一个不会出现的标记。
- `EnsureLexicalIndex` 那种进程内倒排重建。MySQL 全文没有进程内索引。`lex` 包留下是为了对照实验和召回不够时换适配器。
- `ProjectManaged` 短路。那是被否决的项目绑定路线，新代码不要出现这个字段。
- MCP Token、读写 scope、访问矩阵。平台用项目成员和现有代理令牌。

## 3. 模块与 Upstream 替换点

词汇按深模块来用。内置引擎是一个模块：调用方只看到很小的接口，分块、同步、全文查询都在实现里面。

**外部替换点**在已有的 `knowledge.Upstream`。生产环境只有一个适配器，就是内置引擎。再留一个远程客户端会变成第二种部署，与「不保留外部模式」冲突。这个替换点继续存在，是因为 `Service` 的测试要用假实现，不是因为还要换后端。

`Upstream` 仍然只有五个方法，签名不改：

| 方法 | 内置引擎怎么做 |
| --- | --- |
| `Tools` | 返回固定的只读工具说明：`kb_search`、`kb_fetch`、`kb_list_sources`、`kb_get_source`。不向 Agent 列出写工具 |
| `ListSources` | 读控制面来源。不含 Git 地址、凭据和本地路径 |
| `Search` | 在调用方已经收窄的来源集合里做关键词检索 |
| `Fetch` | 读内容面正文并按行截取。见下方 commit 核对 |
| `WriteDocument` | 仅托管来源：写 MySQL 修订并重建该文档的块。Git 来源的审核发布走引擎的 `WriteGitDocument`，见第 10 节；直接调 `WriteDocument` 返回「需经审核队列发布」 |

`kb_get_source` 不进 `Upstream`。它要的是来源状态加文档清单，由 `Service.SourceDetail` 直接读两个 Repo。代理不再把原始工具调用转发给 HTTP 客户端。

`Service` 不再按设置拨号。进程启动时注入这一个 `Upstream`。现有 `svc.dial = ...` 的测试改为注入假 `Upstream`。`NewClient`、客户端缓存和 `internal/knowledge/client.go` 里的远程 MCP 调用删除。`SearchResult`、`Document`、`Source`、`WriteResult` 这些类型留在 `knowledge` 包，因为它们就是工具的 JSON 契约。

**授权语义要改。** 现在 `ScopeSources` 把空的允许列表当成「不限制」。切片里空列表表示该项目没有绑定任何知识库，检索结果为空，读原文返回拒绝。允许列表来自「项目绑定的知识库下面的来源」，不再来自 `KnowledgeSettings.ProjectSources`。请求里点名了未绑定来源，仍然拒绝。SOP 代理和 Main Bot 走同一套收窄，不能各写一份。

**`Fetch` 的 `commit` 入参。** 移植源的工具接受完整 commit，并从 Git 读该版本。LoopForge 现有 `Upstream.Fetch` 没有 commit 参数，代理今天是把参数原样转发出去。切片保持工具入参（`source`、`path`、`start_line`、`end_line`、可选 `commit`），但 `commit` 只做核对：空或等于当前已索引版本就返回正文；其他值返回明确错误「当前只提供已索引版本」，不读 Git。历史版本怎么读见第 8 节，切片不要提前做。

**内部替换点**是关键词索引，两个适配器都真实存在，所以这个替换点成立：

- 默认适配器：对 `knowledge_chunks` 的标题、小节和正文做 MySQL ngram 全文检索。
- 备选适配器：移植的 `lex` 内存倒排。切片不把它接到 `Search`。对照实验若证明 ngram 召回明显更差，只更换这个适配器，不改 `Upstream`、表和工具 JSON。

默认查询用自然语言模式，`ngram_token_size` 固定为 2（MySQL 默认，并在 `compose.yaml` 的 `mysqld` 命令里写明，避免以后镜像默认值变化）。短于 2 个字符的查询匹配不到，界面脚注说明，不要用应用层分词去补。条数默认 10，上限 50。只返回调用方传入的来源。相关度使用全文分数；切片不做项目层压过公共层的排序，因为切片没有公共层。

### 3.1 代理令牌的密钥

现在的密钥是用外部 Token 做 HMAC，再混入 `AGENTFLOW_SESSION_SECRET`（空则回退 `SESSION_SECRET`）。外部 Token 删除后，密钥只来自这个会话密钥：

- 用会话密钥对固定字符串 `agentflow-knowledge-proxy` 做 HMAC-SHA256，得到签名密钥。
- 会话密钥为空则拒绝签发。不在进程内生成随机密钥，否则多实例和其他实例签发的令牌对不上。部署说明已经要求各实例共用同一个 `AGENTFLOW_SESSION_SECRET`。
- 轮换会话密钥会使已发出的代理令牌失效。这取代原先「轮换外部 Token 即作废」的行为。
- `SignProxyToken` / `VerifyProxyToken` 不再读取 `KnowledgeSettings`。现有「改 Token 后旧令牌失败」的测试改成「改会话密钥后旧令牌失败」。

`knowledgeMCPServer` 不再看 `Ready()` 里的地址和 Token。平台 URL 仍按今天的规则拼到 `/runners/knowledge/mcp`。没有可绑定的知识库时照样签发令牌，工具返回空列表，避免角色开关把运行打失败。`knowledgeSkippedWarning` 里「未配置地址/Token」的句子删掉。

### 3.2 两条工具通道

| 通道 | 切片里的改法 |
| --- | --- |
| SOP 角色 | 机制不变：`internal/engine/assets.go` 在角色 `KnowledgeBase` 为真时注入保留 MCP 和使用规范。代理 `internal/httpapi/knowledge_proxy.go` 改为调用 `Service`，不再 `NewClient` |
| Main Bot | `internal/httpapi/agent_tool_catalog.go` 的 `buildCatalog` 在 `AgentToolAssistant` 下追加只读知识库工具，种类用单独的 `knowledge`，不要塞进 `agentflow` 白名单。`internal/httpapi/agent_tool_mcp.go` 的 `dispatch` 增加分支，进程内调用 `Service`，按 `session.ProjectID` 收窄。不发 HTTP |

Main Bot 不看角色上的知识库开关。会话有项目就能用；没有项目就不要把工具放进目录。澄清模式和定时任务不挂。使用规范复用 `knowledge.UsageRule()` 的正文，追加到 `internal/httpapi/assistant.go` 的 `assistantNativeSystemPrompt`，不要另写一套政策。

Agent 仍然完全只读。写入只有知识中心的托管文档编辑，以及既有审核队列在托管来源上的发布。

### 3.3 包的落点

```
internal/knowledge/                 Service、规则、代理令牌、工具 JSON 类型
internal/knowledge/engine/          Upstream 的唯一适配器
internal/knowledge/engine/chunk/    移植的分块
internal/knowledge/engine/frontmatter/
internal/knowledge/engine/lex/      非默认适配器
internal/knowledge/engine/gitsync/  同步缓存
```

`Service`、HTTP 和引擎调度不引用 `chunk`、`lex`、`gitsync`。同步循环由 `cmd/agentflow/main.go` 在 `executeRuns` 为真时启动（角色 `worker` 和 `all`），与 `StartClaimLoop` 相邻。只跑 `api` 角色的进程不克隆、不索引，但可以检索和读原文。本机 `scripts/dev.sh` 的默认角色是 `all`，一个进程两件事都做。

租约沿用现有条件更新的做法，不新造协调服务：来源行上有 `lease_owner` 和 `lease_until`。循环领取 `status` 为待同步且租约过期的 Git 来源，租约时长与运行租约同一量级（默认 30 秒，执行中续期）。领取失败的实例跳过。同步结束把状态写成就绪或错误，错误文本落在来源上，知识中心能看见。托管来源不进入这个循环。

## 4. 表、迁移与 Repo

下一份脚本是 `migrations/026_builtin_knowledge.sql`。已占用到 `025`；`016`、`018`、`024` 各有两个文件，不要复用这些编号，也不要改历史脚本。

脚本分成两段，段内都不含跨库外键或跨库事务。同一 MySQL 时整份执行。配置了独立内容库时，第一段在主库执行，第二段在内容库执行。

### 4.1 控制面（主库，`KnowledgeRepo`）

`KnowledgeRepo` 已经存在，继续扩展它。不要新建第二个控制面 Repo。

| 表 | 要点 |
| --- | --- |
| `knowledge_bases` | `id` 主键；`project_id` 可空（切片只写本项目的库）；`name`；`layer` 只写 `project`；`owner_user_id`；时间戳。切片不读 `shared` / `platform` |
| `knowledge_sources` | `id` 主键；`base_id` 普通字符串，不建外键；`kind` 为 `hosted` 或 `git`；`name`；`git_url`；`branch`；`path_prefix`（只索引此前缀下的 `.md`）；`head_commit`；`head_revision`（托管库的当前修订号）；`status`；`index_status`；`doc_count`；`last_error`；`last_synced_at`；`lease_owner`；`lease_until` |
| `project_knowledge_bindings` | 主键 `(project_id, base_id)`；`permission` 切片只写 `submit`。无外键 |
| `knowledge_sync_jobs` | 自增主键；`source_id`；`kind`；`status`；起止 commit；变更与删除计数；日志；起止时间 |
| `knowledge_writebacks` | `031`。`id` 主键；`submission_id` 唯一；`source_id`；`path`；`branch`；`commit`；`mr_url`；`mr_provider`；`mr_iid`；`mr_state`；`status`；中文提示与错误；`attempts`；`checked_at`；时间戳 |
| `knowledge_submissions` | 已有表，切片不改列。发布到托管来源时走 `WriteDocument`；发布到 Git 来源走 `WriteGitDocument`，分支与合并请求另存 `knowledge_writebacks` |
| `knowledge_settings` | 删除。`026` 对已有库 `DROP TABLE`。从 `allModels` 去掉 `knowledgeSettingsRow`，并整体删除 `domain.KnowledgeSettings`、`KnowledgeRepo` 的读写方法与平台级写回开关；审核发布只按项目绑定和来源范围授权。新装不再建这张表。`002_knowledge_base.sql` 保持原样 |

`knowledge_bases.project_id` 加普通索引。一个项目在切片里只有一个自动托管库：用唯一索引 `(project_id, layer)` 挡住并发创建。`project_id` 为空时不参与这个唯一约束（MySQL 里多个 NULL 可以并存），所以不要靠它约束以后的公共库。

来源的 Git 地址可以出现在管理接口里，因为这是用户自己登记的公开地址。响应里不要出现本地缓存路径。

### 4.2 内容面（`KnowledgeContentRepo`）

新 Repo，文件 `internal/repo/knowledge_content.go`。它不嵌入 `deps`，自己持有一个 `*gorm.DB`。`repo.Open` 读取 `AGENTFLOW_KNOWLEDGE_MYSQL_DSN`；空则把主库那个 `*gorm.DB` 传给它（同一个连接池）。非空则再 `gorm.Open` 一次，`Store.Close` 只在连接池不同时关闭第二个，避免关两次。这个字段不放进 `deps`。`newStore` 增加内容库参数，匿名嵌入 `*KnowledgeContentRepo`，与其他 Repo 一样靠方法提升。不在 `Store` 上写包装方法。

| 表 | 要点 |
| --- | --- |
| `knowledge_documents` | 主键 `(source_id, path)`；`path` 用 `VARCHAR(512)`，避免 utf8mb4 下索引超长；`title`；`body` 为 `MEDIUMTEXT`（含 frontmatter 的完整 Markdown）；`line_count`；`sha256`；`commit`；`revision`；`updated_at`。无外键 |
| `knowledge_revisions` | `id` 主键；`source_id`；`path`；`revision` 整数；`body`；`author`；`created_at`。唯一 `(source_id, path, revision)`。只给托管库追加，Git 同步不写修订历史 |
| `knowledge_chunks` | 自增主键；`source_id`；`path`；`idx`；`heading`；`title`；`start_line`；`end_line`；`commit`；`revision`；`text`。唯一 `(source_id, path, idx)`。无外键 |

全文索引不靠 gorm tag。脚本第二段在表创建之后执行：

```sql
ALTER TABLE knowledge_chunks
  ADD FULLTEXT INDEX ft_knowledge_chunks_text (title, heading, text) WITH PARSER ngram;
```

`autoMigrateIncremental` 只会补缺失的表和列，补不出 ngram 解析器。模型要注册进 `allModels()`，这样缺表的环境能启动；体验和测试仍必须执行 `026`，否则检索没有全文索引。新表加入 `needsUTF8MB4Table`，SQL 使用 `utf8mb4` / `utf8mb4_unicode_ci`。

内容行只用 `source_id` 字符串指向控制面。删除来源时由 `Service` 按顺序删内容、再删控制面记录，不做数据库级联。

### 4.3 `AGENTS.md` 要更新

要更新。领域表今天没有知识库，但代码里已经有 `KnowledgeRepo`。实现时补两行：

| 领域 Repo | 拥有 |
| --- | --- |
| `KnowledgeRepo` | 知识库、来源、项目绑定、同步任务、审核队列。主库 |
| `KnowledgeContentRepo` | 文档正文、托管修订、索引块。独立的 gorm 连接，由 `newStore` 注入，不写入 `deps`；默认与主库同一个连接池 |

并在 Repo 规则里加一句：内容面禁止外键指向控制面；控制面也不存正文。handler 规则不用新增 struct：管理接口仍是 `knowledgeHandlers`，新文件可以是 `internal/httpapi/knowledge_bases.go`。`projectsHandlers` 增加对 `knowledge.Service` 的显式字段，在新建项目成功后调用保证托管库，不把这个方法放进 `ProjectsRepo`。

## 5. 最小切片与本机验收

### 5.1 切片必须有的

体验闭环是：建项目，知识库里有内容，知识中心能管理，Agent 和 Main Bot 都能检索并读到同一份原文。

- 第 4 节的表、两个 Repo、`026` 脚本。
- 托管库：建项目自动出现；知识中心能新建和覆盖一篇 Markdown；检索能命中；读原文的行号与正文一致；出处里的版本是修订号。
- 一个 Git 来源：登记公开地址、分支和路径前缀，同步进 MySQL，检索和读原文不再碰工作副本。同步状态和错误在知识中心可见。
- `Upstream` 五个方法的切片行为，以及代理令牌改用会话密钥。
- SOP：角色打开知识库开关后，工具仍走 `/runners/knowledge/mcp`，后面是内置引擎。
- Main Bot：目录里有这四个只读工具，调用按当前项目收窄。
- 删除地址、Token、默认来源、按项目来源和 `Ready()` 对它们的依赖。检查连接接口改为返回本机索引状态，不再请求外部 MCP。
- 知识中心界面从「连接配置」改成「本项目的知识库、来源、新建文档、同步、试搜、读原文」。审核标签留着，不作为体验主路径。

界面沿用 `design.md` 知识中心一节的面板、标签和抽屉。该节现在写的是外部地址、打开控制台和按项目勾选来源，与本设计冲突。实现时改 `design.md` 这一节，使页头说明、指标和「连接」标签描述跟托管库一致；不要保留「打开控制台」。未配置外部地址时隐藏按钮的规则删除。

### 5.2 留到阶段一补全，不进切片

方案里阶段一的其余部分，等这条体验路径走通再做：

- 公共知识库、平台知识库，以及「相关度相同时项目层在前」。
- 一个知识库挂多个 Git 来源的完整管理，以及把项目里已登记的代码仓库当成来源。
- 私有 Git 的服务端凭据已在阶段一补全，见第 9 节。
- 外部仓库的提交、推送与合并请求：已实现，见第 10 节。
- 审核队列的自动入队、门禁、指纹和墓碑。
- 调用记录持久化：已实现。控制面表 `knowledge_activity`（`027`）按项目保留最近
  200 条，`/api/knowledge/usage` 的 `activity` 从库读取；SOP 代理与 Main Bot 两条
  通道都记录，写入失败不影响工具调用。
- 历史 commit 的原文。见第 8 节。

阶段二及以后仍不提前：向量与可选 compose profile、`kb_grep`、出处结构化字段、引用表、自动沉淀、治理面板、对外 MCP。

### 5.3 本机怎么起

在本工作区根目录执行。不要使用不带 `dev` 的 `./start.sh`：那会拉取已发布镜像，里面没有这个功能。

本机进程，和现有 `scripts/dev.sh` 一样（只起本仓库的 MySQL，编译并前台运行平台，默认端口 8090）：

```bash
bash scripts/dev.sh
```

实现完成前，MySQL 健康之后还要手动执行一次：

```bash
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/026_builtin_knowledge.sql
```

切片的安装任务把这一行加进 `scripts/dev.sh`，放在现有 observability 脚本之后、种子 SQL 之前。这是安装脚本在调用，不是 `repo.Open` 在跑迁移。

容器体验必须是源码构建：

```bash
./start.sh dev
```

然后执行上面同一条 MySQL 导入。端口看 `.env` 里的 `AGENTFLOW_HTTP_PORT`，默认 8090。`./start.sh dev` 会把 `COMPOSE_FILE` 记进 `.env`，之后的 `docker compose` 才会带上 `compose.dev.yaml`。

`compose.yaml` 的 MySQL `command` 在现有字符集参数后加上 `--ngram_token_size=2`。改的是服务器启动参数，默认值本来就是 2，不会重建已有业务表。全文索引仍然要靠 `026` 创建。

账号：`scripts/dev.sh` 会创建 `admin`，密码在 `.local/admin-password`。容器方式按 `docs/deployment.md`，用浏览器里的第一个账号。

### 5.4 公开样例与验收

样例仓库用 `https://github.com/Tencent/LoopForge`，分支 `main`，路径前缀 `docs/`。它是公开 HTTPS，不需要凭据，内容就是本仓库已发布的文档，可以用网页上的同一文件核对行号。只索引此前缀下的 Markdown。

验收看行为，不看内部类型：

1. `bash scripts/dev.sh` 或 `./start.sh dev` 之后，执行 `026`（脚本已代劳则不必再执行），浏览器能打开知识中心，页面上没有地址和 Token 输入框。
2. 新建一个项目。知识中心显示一个空的项目知识库，来源种类是托管。再打开安装时已有的欢迎项目，也会看到它的托管库，且重复打开不会多出第二个。
3. 在新建项目的托管库写一篇含独特词的 Markdown。试搜这个词，打开原文，行号和正文与刚写入的内容一致，出处里的版本是修订号而不是 Git commit。覆盖该文档后再读，修订号增加，检索仍只看到新正文。
4. 把样例仓库按 5.4 的地址登记为 Git 来源并同步。成功后篇数大于 0。检索一个 `docs/` 里稳定存在的词（例如部署文档中的 `AGENTFLOW_MYSQL_DSN`），打开原文，与 GitHub 上 `main` 分支该文件的对应行一致。同步过程中停掉平台再启动，工作副本目录删掉后再次同步仍能检索，说明副本不是事实源。
5. 项目 A 的试搜和读原文不能看到项目 B 的托管文档。SOP 角色只在知识库开关打开时拿到工具；关掉开关则运行资产里没有 `agentflow-knowledge`。Main Bot 在项目 A 的会话里能检索到第 3 步的独特词，在项目 B 的会话里不能。
6. 写工具对 Agent 不可见。代理令牌在 `AGENTFLOW_SESSION_SECRET` 更换后失效。
7. `go test ./internal/knowledge/... ./internal/httpapi/ -count=1` 通过。全文检索用例在未设置 `AGENTFLOW_TEST_MYSQL_DSN` 时跳过；`make test-mysql` 会跑到它们。默认测试用假的关键词适配器覆盖授权和读原文，不要求开发机器常驻 MySQL。

## 6. 实现子任务

按依赖排序。每一项都先把对应的移植源测试改写成当前仓库的测试，再写实现。纯函数测试不连数据库。需要全文索引或真实 SQL 的测试遵循 `AGENTFLOW_TEST_MYSQL_DSN` 缺失则 `t.Skip`，与 `internal/repo` 现有集成测试相同。

| 编号 | 目标 | 依赖 | 可否并行 | 源与目标文件 | 测试 |
| --- | --- | --- | --- | --- | --- |
| S1 | 控制面与内容面的表、模型和 Repo；`Open` 能接可选的内容库 DSN；`Close` 不重复关闭 | 无 | 否，后续都用这些类型 | 新 `migrations/026_builtin_knowledge.sql`。改 `internal/domain/knowledge.go`、`internal/repo/models.go`、`internal/repo/automigrate.go`、`internal/repo/knowledge.go`、`internal/repo/repo.go`。新 `internal/repo/knowledge_content.go` | Repo 测试：创建库、绑定、写正文与修订、删除来源时没有外键可依赖；相同 DSN 与空 DSN 走同一个连接池。全文索引是否存在用 `026` 之后的 MySQL 测试断言 |
| S2 | 分块与 frontmatter 校验 | 无 | 可与 S1、S3、S4 并行 | 移植 `internal/chunk/chunk.go`、`internal/index/frontmatter.go` 到 `internal/knowledge/engine/chunk` 与 `frontmatter` | 原测试改写后行为不变：标题、行号、非法 frontmatter 被拒绝 |
| S3 | 内存倒排作为未接线的适配器 | 无 | 可与 S1、S2、S4 并行 | 移植 `internal/lex/lex.go` | 原 `lex_test.go`。不要求检索接口调用它 |
| S4 | Git 同步缓存 | 无 | 可与 S1–S3 并行 | 移植 `internal/gitsync/gitsync.go` 中的克隆、拉取、列举 Markdown、工作副本内读文件。不移植 `gitsync_auth_test.go` 对应的包装器参数 | 用临时本地 Git 仓库改写 `gitsync_test.go` 里与同步缓存有关的用例。断言路径逃逸失败。不访问网络，不出现内部主机名 |
| S5 | 引擎实现托管库的五个 `Upstream` 方法 | S1、S2 | S2 完成后即可，不必等 S4 | 新 `internal/knowledge/engine/` 下的引擎与 MySQL 关键词适配器。`Service` 改为注入 `Upstream` 可以放在 S7，本项先把引擎自己测通 | 假适配器测试授权收窄和按行读取。MySQL 测试：写入一篇中文 Markdown，ngram 能命中连续二字，单字查询不报错但可以没有命中，覆盖后旧正文不再命中 |
| S6 | Git 来源同步、租约和 worker 循环 | S1、S4、S5 | 与 S7、S8 的托管部分并行 | `internal/knowledge/engine` 的同步；`KnowledgeRepo` 的领取方法；`cmd/agentflow/main.go` 在 `executeRuns` 时启动 | 两个逻辑上的领取者只有一个能同步。同步结果只存在内容表。删除工作副本后仍能 `Fetch`。`api` 角色不启动循环 |
| S7 | 删除外部客户端、接入配置和旧密钥 | S5 | 可与 S6 并行 | 删除 `internal/knowledge/client.go` 的远程调用。改 `token.go`、`service.go`、`rules.go`、`internal/httpapi/knowledge.go`、`knowledge_proxy.go`、`internal/engine/assets.go`、`internal/domain/knowledge.go`，以及这些包里断言地址和 Token 的测试 | 现有知识库测试改为：没有地址和 Token 也能检索；错误密钥拒绝代理；写工具不出现在 Agent 的工具列表；Git 来源的 `WriteDocument` 返回未实现且不创建提交 |
| S8 | 知识中心的管理接口和界面 | S5；同步按钮依赖 S6 | 托管文档部分可与 S6 并行 | `internal/httpapi/knowledge.go` 与新的 `knowledge_bases.go`。`web/knowledge.js`、`web/knowledge.css`。改 `design.md` 知识中心一节 | HTTP 测试覆盖创建文档、试搜、读原文、拒绝其他项目。浏览器侧至少断言页面不再提交 `url` / `token` |
| S9 | 建项目与打开知识中心时幂等创建托管库 | S5 | 可与 S6–S8 并行 | `internal/httpapi/projects.go` 注入 `knowledge.Service`。知识中心读取接口调用同一个保证函数 | 并发创建同一个项目不会有两个托管库。欢迎项目在第一次打开知识中心后恰好有一个 |
| S10 | Main Bot 工具与提示词 | S7 | 在 S7 之后 | `internal/httpapi/agent_tool_catalog.go`、`agent_tool_mcp.go`、`assistant.go` | 目录在助手模式包含四个只读工具，澄清模式和定时任务不包含。调用使用会话项目。系统提示包含使用规范里的工具名，且没有写工具 |
| S11 | 让第 5.3 节的命令成立 | S1 | 可与 S5 之后的任务并行 | `compose.yaml` 的 `ngram_token_size`；`scripts/dev.sh` 执行 `026` | 安装测试若会检查 compose 命令，补上这个参数仍能启动 MySQL。不新增第二套启动方式 |

S2、S3、S4 在 S1 进行时就可以由不同的人同时做。S5 是关键路径。S6、S7、S8 的托管部分、S9、S11 在 S5 之后可以并行。S10 最后做。

不要把 `embed`、`qdrant`、`governance`、`mcplite`、`api`、`projectkb` 拆进这些任务。

## 7. 阶段一补全时再打开的原阶段清单

切片验收通过之后，阶段一剩下的是第 5.2 节。写回推送已按第 10 节补全；向量、自动沉淀与治理仍属于更后面的阶段。

## 8. 冲突与未决

下面各项本文不选替代方案。除特别写明「派发按此实现」的映射外，实现不要自行挑一条做进切片。

1. **历史版本的原文。** 方案既要求服务路径的读原文只读 MySQL、任何实例都能做，又写了外部仓库读取历史 commit 时按需从 Git 取。后者只有持有工作副本的实例能做，和多实例无状态读原文冲突。切片按第 3 节核对当前版本，其他 commit 直接报错。历史版本留在阶段一补全之外，直到这个冲突有结论。
2. **内容面放在哪个 Repo。** 方案写的是扩展 `KnowledgeRepo`，同时要求内容面可以换连接。`AGENTS.md` 又不允许往共享 `deps` 里加第二个数据库。派发按第 4.2 节实现：控制面扩展已有 `KnowledgeRepo`，内容面用新的 `KnowledgeContentRepo` 持有自己的 `*gorm.DB`。这是同时满足两条已定约束的映射，不是改掉其中一条。若要求改成单一 Repo 持有两个连接，先改本节再写代码。
3. **关键词召回是否合格。** 已定默认是 MySQL ngram，本文不判断它够不够用。对照用的 `lex` 只移植、不接线。召回对比的结论若是 ngram 明显更差，只更换第 3 节的关键词适配器。
4. **外部仓库怎么写回。** 已定：提交到新分支并推送，再按托管平台创建合并请求；不支持的平台只推分支并给出中文提示。由持有来源租约的实例执行，工作副本仍是缓存。实现见第 10 节。
5. **私有来源的 Git 凭据。** 阶段一补全采用来源级服务凭据，见第 9 节。


## 9. 私有 Git 来源凭据（阶段一补全）

登记来源支持 HTTPS 用户名与令牌、SSH 部署密钥，SSH 可用 `ssh://git@host/path` 或 `git@host:path`。凭据独立于用户、项目仓库凭据。既有 UsersRepo、项目仓库与通知机器人凭据没有通用加密工具，因此来源凭据新增 AES-256-GCM 加密存储，写入控制面 `knowledge_sources.credential_ciphertext`，迁移为可重复执行的 `029_knowledge_git_credentials.sql`。

加密密钥从服务的 `AGENTFLOW_SESSION_SECRET`（兼容 `SESSION_SECRET`）按独立用途派生，source ID 作为认证附加数据。多实例使用相同服务密钥，备份数据库时保留该密钥。更换密钥后需重新配置来源凭据。凭据保存 `canWrite` 标记，默认为 false，本次同步只使用读取操作。

`POST /api/knowledge/bases/sources` 接受可选 `credential`。省略时保留已有凭据，提供时原子替换来源与凭据。`PUT /api/knowledge/bases/sources/credential` 单独替换凭据并重新排队同步。只有知识库所属项目的 Owner、非 viewer 成员或平台管理员可管理来源与凭据。接口和界面仅返回 `credentialConfigured`，不读取已有用户名、令牌、私钥或密文。删除来源同时删除同一行上的密文。

HTTPS 通过临时 askpass 文件读取用户名和令牌，SSH 使用临时私钥和明确配置的 `known_hosts` 记录验证服务器。临时目录 0700、密钥文件 0600，命令结束后删除。子进程只接收文件路径，环境采用白名单，禁用个人凭据 helper、SSH agent、全局 Git/SSH 配置及代理。HTTPS URL 不含 userinfo，`.git/config` 只写无凭据地址。私有来源按密文指纹分开缓存，替换凭据产生新指纹，旧租约退出期间新同步可独立克隆；相同凭据的后续同步仍复用缓存。

两种协议都校验 DNS 的全部结果并拒绝私网和保留地址。HTTPS 保留 `http.curloptResolve` 固定解析、禁用重定向和代理，SSH 使用已校验 IP 的 `HostName` 固定连接以及原始域名的 `HostKeyAlias` 验证主机。HTTP 明文地址在登记时拒绝。

Git stderr 只用于分类，日志、错误响应与同步记录均保存分类后的提示。鉴权失败、仓库不存在、分支不存在及 SSH 主机公钥错误分别显示中文提示。有些托管服务对无权访问返回 404，此时按服务器提供的“仓库不存在”信号提示，并提醒检查凭据访问范围。


## D11 扩展：公共知识库与项目加载

项目库继续由项目拥有；平台管理员创建 `layer=shared`、`project_id=NULL` 的公共库并管理其来源。公共库含独立托管来源，也可登记 Git 来源。知识中心的「公共知识库管理」在独立管理范围中维护公共库，项目侧只显示已加载库的来源与文档。

- `POST /api/knowledge/bases/shared`：管理员创建公共库（`name`）。
- `GET /api/knowledge/bases/bindings?projectId=…`：公共库目录与当前项目的 `loaded` 状态，不含未绑定来源。
- `PUT /api/knowledge/bases/bindings`：项目 Owner、`maintainer` 或平台管理员提交 `projectId`、`baseId`、`enabled`；绑定权限固定为 `read`，取消加载不删除内容。
- 管理端已有 `/api/knowledge/bases` 及来源、文档接口在空 `projectId` 时只允许平台管理员，操作范围限公共库；带项目时只能维护该项目自身知识库。

知识中心、SOP 代理与 Main Bot 每次调用均实时计算「本项目库 + 已绑定公共库」。其他项目库即便被错误绑定也不可见；无项目的 Agent 不获得公共库读取权限。公共库的项目绑定只授予读取，不允许项目侧修改正文、来源、凭据或同步状态，审核发布也按库归属验证写权限。

结果、原文与工具来源元数据通过 `base_id`、`base_name`、`layer` 标明知识库与层级，版本出处字符串保持不变。Service 在检索器返回后按分数降序，以各组最高分为固定锚点将相差不超过 5% 的结果归为一组，组内稳定地将项目层排在公共层之前；关键词与后续混合检索共用此策略。明显更相关的公共结果不被较弱项目结果越过。

控制面沿用已有表，手动执行可重复的 `030_shared_knowledge.sql` 将历史公共绑定权限归一为 `read`；无需内容面迁移。检索、原文、来源的权限回归覆盖 HTTP 管理 API 与两条 Agent 通道；排序用实际 Service/Engine 接口验证，浏览器验证管理员建库、项目加载与只读浏览。

## 10. Git 来源写回（阶段一补全 1f-4）

审核队列把条目发布到 Git 来源时，平台按以下顺序执行。托管来源不受影响，仍直接写 MySQL。

1. 校验来源为 `git`、路径在来源的 `path_prefix` 内，并解密来源凭据。凭据必须已标记 `canWrite`，否则返回中文提示，不推送。
2. 领取来源写回租约（`knowledge_sources.lease_owner/lease_until`，与同步互斥）。其他实例正在同步或写回时返回「该来源正在同步或写回，请稍后重试」。租约默认 60 秒并按 TTL/3 续期。
3. 刷新来源缓存工作副本，丢弃崩溃写入留下的未提交改动。缓存仍只是可重建副本，事实源是 Git 仓库。
4. 从来源分支创建或重建写回分支 `kb/writeback-<审核条目 ID>`，只提交审核路径这一个文件，作者固定为 `LoopForge Knowledge <knowledge@loopforge.local>`，再推送到远端。重试沿用同一分支并重建为单个提交；推送使用 `--force-with-lease`，租约排除并发写者。
5. 按托管平台创建合并请求。GitHub（`github.com`）调用 REST `POST /repos/{owner}/{repo}/pulls`；GitLab（`gitlab.com` 与自建 GitLab）调用 `POST /projects/{urlencoded path}/merge_requests`。GitLab 使用 `PRIVATE-TOKEN`（Personal 或 Project Access Token）；GitHub 使用 Bearer 令牌。部署密钥、没有合并请求权限的令牌或其他平台（Gitee、Codeberg、Bitbucket 等）没有可用 HTTP 令牌时只推分支，返回中文提示让用户在仓库网页创建。合并请求状态沿用交付能力的口径（`opened` / `merged` / `closed`），但不写入 `mr_delivery_states`：那张表绑定 Gongfeng 项目与需求 ID，知识写回没有需求上下文，单独记录在 `knowledge_writebacks` 才不会把知识库混进交付统计。
6. 控制面写 `knowledge_writebacks`：分支、提交、合并请求链接/编号/状态与中文提示。审核条目读取时叠加写回视图，知识中心审核页可见分支、合并请求链接与状态。
7. worker 的同步循环每 30 秒刷新未终结的写回：优先向托管平台查询合并请求状态，API 不可用时再用缓存工作副本探测提交是否已进入来源分支。检测到合并后把来源置为待同步；同步器按已有文档指纹只读取新增或变化的文件，删掉已移除的路径，其他文档和索引块保持不动。压缩合并（squash）会改写提交，祖先探测认不出；这类平台若不支持查询合并请求状态，由用户在合并后手动触发同步。

安全边界：凭据只通过临时 askpass / 私钥文件传给 Git 子进程，并走白名单环境；`.git/config` 只保存无凭据地址。Git stderr 和托管平台响应正文都不进入日志、错误或 API 响应，只保留分类后的中文。合并请求接口禁用重定向、限制响应大小，并对只推送分支的平台给出明确说明。

迁移：`migrations/031_knowledge_writeback.sql` 可重复执行，只建控制面表，不含跨库外键。内容面的增量写入由 `KnowledgeContentRepo.ApplyGitKnowledge` 完成。

## 11. 自动沉淀（阶段三）

实现落在 `internal/knowledge/sediment.go` 与 `internal/knowledge/engine` 的
`RetractDocument`，迁移是 `migrations/032_knowledge_sediment.sql`（可重复
执行，只动控制面）。后台循环在 worker 角色启动，扫描最近的 `knowledge.md`
运行产物；同一个产物用一个内容无关的稳定 ID 入队，重复扫描只落一行。

闭环信号取自 LoopForge 已有事件：运行 `succeeded`、运行事件
`gate.approved`、需求 `done/closed/completed`、需求交付合并请求状态
`merged`。产物所属节点有 Agent 时按该角色的知识库开关判断；脚本/命令节点
没有角色，按同一次运行里是否至少有一个开启知识库的角色判断。

门禁逐项保存在 `KnowledgeSubmission.gateChecks`：

- `evidence`：有闭环事件，且正文与标题达到可复用长度。LoopForge 不保存
  会话里的逐字引用，因此不再照搬 dsh 的“三类证据原文必须逐字命中”；闭环
  事件本身是运行证据。
- `sensitive`：知识库配置的正则规则；未配置时使用平台内置的通用规则
  （令牌、口令、私钥、JWT、邮箱、手机号、IPv4、提示词注入），不包含任何
  内部名词。
- `fingerprint`：正文归一化后的 SHA-256，与同项目待审核/已写回的条目比较。
- `tombstone`：同项目同指纹的撤回墓碑。

发布模式在 `knowledge_bases.sediment_publish_mode`：`review`（默认）只入队，
`auto` 在四项门禁全过后调用已有 `Publish`，因此托管来源直接写 MySQL、Git
来源仍走写回分支与合并请求。`Publish` 在真正写回前重新检查墓碑与敏感规则，
自动沉淀条目还重算证据与指纹。

撤回对托管来源删除正文、修订与索引块，写 `knowledge_tombstones` 并写回
墓碑；同一项目再次产出相同正文时墓碑门禁失败，不会写回。Git 来源的事实源
在仓库，平台返回中文提示要求通过删除合并请求撤回，避免只删平台副本。
向量索引是可选缓存，`Engine.SetVectorDeindexer` 提供窄回调；向量分支合并后
在回调里调用已有的 Qdrant 删除接口，检索融合层不改。

## 12. 长期治理（阶段四）

范围是阶段四里不依赖引用表的部分。平台只让问题被看见，不自动改知识内容；
更新和下线都进入审核队列，复用已有写回与撤回路径。

### 12.1 元数据与生命周期

索引写入内容面时解析文档头部的 YAML frontmatter，把 `owner`、`status`、
`updated` 落到 `knowledge_documents` 的三列。托管文档不自动补 frontmatter，
没有头或字段缺失时按缺失处理；字段类型不符合预期时只丢弃该字段，不影响
正文写入。`status` 归一化为 `draft`、`verified`、`deprecated`、`archived`，
常见中英文别名收敛到这四个值，未知值保留小写原文并按中性处理。

检索器不感知生命周期。`Service` 收到检索结果后按来源/路径回查元数据：归档
文档从结果中移除，废弃文档分数乘以 0.25 后重新稳定排序，并在命中 JSON 里
带 `doc_status` 供界面显示。该后处理在 `Service.Search` 与
`TrialSearch` 两个入口共用，SOP 代理和 Main Bot 走同一条路径。

### 12.2 健康检查

健康检查是纯规则包：过期、同库重复标题、库内相对 Markdown 链接死链、缺少
负责人。过期阈值默认 180 天；缺少 `updated` 也会以“无法判断是否过期”列入
过期项。死链只检查同来源内、以 `.md` 结尾的相对链接，跳过外链、图片、锚点
和代码块，避免把仓库里实际存在的非 Markdown 资源误报。重复按归一化标题在
同一知识库内分组。

worker 角色启动后立即扫描一次，之后每 24 小时扫描全部知识库；知识中心提供
「立即检查」按当前项目范围手动触发。每次扫描按知识库整批替换问题行并更新
最近扫描摘要，0 个问题也保留摘要。结果存 `knowledge_governance_issues` 和
`knowledge_governance_scans`，只存定位和中文说明，不存正文。

### 12.3 反馈与知识缺口

反馈表 `knowledge_feedback` 统一承载三处输入：知识中心试搜/读原文的
「有用 / 没用」、Main Bot 回答旁的「有用 / 没用」、Agent 的标记问题工具。
反馈可关联来源/路径，也可以只针对回答；`kind=issue` 时必须有说明。写入
失败不回滚用户的检索或对话。

知识缺口表 `knowledge_gaps` 按项目加归一化查询合并计数，保留首次和最近
出现时间。只有检索成功且原始命中为 0 时记录，检索错误、空查询和没有可查
来源不记录。知识中心试搜、SOP 代理和 Main Bot 的检索都经过 `Service.Search`
或 `TrialSearch`，因此不会漏记。

### 12.4 Agent 工具与处置入口

知识库工具面在四个只读工具之外增加 `kb_report_issue`，参数是
`source`、`path`、`issue`。它只写反馈，不修改正文，也不出现在写回路径上；
SOP 代理与 Main Bot 目录共用同一份工具定义和授权收窄。

治理面板对问题文档提供「发起更新」和「下线」。两者只创建 pending 审核
条目：更新带入当前正文供审核人编辑，发布走已有托管写入或 Git 写回分支；
下线条目带 `action=retract`，审核通过后删除托管正文、修订与索引并写墓碑，
不跑沉淀门禁，避免因内容本身敏感或质量差而无法下线。Git 来源不能由平台
直接删除事实源，面板不显示下线，服务端也拒绝。

### 12.5 迁移与未做项

`migrations/033_knowledge_governance.sql` 可重复执行：控制面建四张表，内容
面用 information_schema 判断后补三列。历史文档没有元数据列时由内容 Repo
读取正文兜底解析，下一次覆盖写入落列。

引用次数和从未命中的文档已接上 `knowledge_citations`：按文档统计最近 90 天的
检索、读取和精确匹配；有正文但窗口内没有引用的文档算从未命中。没有引用表时
面板不用 0 冒充统计结果。

## 13. 授权审查修复与 037 迁移

项目知识管理接口显式检查当前用户：Owner、成员（含 viewer）和平台管理员可读；
写操作排除 viewer 和非成员。审核列表按用户可读项目过滤，详情与写操作再次检查
条目所属项目；公共库的空项目管理范围只允许平台管理员。服务层另按来源的实际
知识库归属检查写入、治理处置和撤回执行，项目加载公共库只授予读取与问题反馈。
SOP 代理和 Main Bot 继续使用令牌/会话里的项目，工具参数不能切换项目。

新来源 ID 使用 `hosted:<baseID>` 或 `git:<baseID>\0<url>\0<branch>\0<prefix>`
作 SHA-256 种子，保留前 128 位并加类型前缀，项目与公共库共用规则。
项目基座保留现有短 ID；加前缀后超过 64 字节时用 `project:<projectID>` 的
128 位 hash。公共基座也使用有界 hash。创建项目拒绝与已有知识库 ID 冲突。
已有 Git 来源 ID 不改名：ID 是加密凭据的 AES-GCM 附加认证数据；重复登记只在
已核实归属的库内，按完整 Git 配置复用原 ID，不能把其他库的来源认领过来。

已有托管来源在升级前手动执行 `037_knowledge_source_namespace.sql`，覆盖正文、
修订、索引块、审核 payload 和控制面引用；异常超长项目的基座引用也同时改名。
执行期间停止 API 和 worker，成功后重启。该脚本可重复执行，并保留
`knowledge_id_migration_037` 映射以便中断后恢复；内容先更新、控制面后更新。
同库直接执行整份；同一 MySQL 服务的独立内容库先设置
`@knowledge_content_schema` 为内容库名再执行整份。

内容库在另一台服务器时按脚本的三个标记分段执行：先在控制库执行 `PLAN`，
把 `knowledge_id_migration_037` 表（结构和数据）导入内容库；在内容库执行
`CONTENT`（`@knowledge_content_schema` 使用内容库名）；确认完成后在控制库
执行 `CONTROL`。失败后按相同顺序重跑，使用原映射，不重新生成或清空映射表。
历史聊天中已经呈现的旧出处字符串不重写；新调用使用新 ID。若启用了向量索引，
清除旧托管 source 的缓存点并重新启用相关库的向量索引，按内容库重建缓存。
迁移不修复已经发生的来源劫持；若发现来源归属与备份不符，需从可信备份恢复。

MR API 与 Git 共用公网地址校验；HTTP 连接直接拨已校验 IP，保留原始 TLS
主机名，禁用代理和重定向。Git 缓存新目录及复用的来源目录为 0700。
