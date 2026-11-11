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

`gitsync.WriteDocument`（提交并推送）的代码可以随包留下并保持单元测试，但切片的写路径不调用它。外部仓库写回方式还没定，见第 8 节。

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
| `WriteDocument` | 仅托管来源：写 MySQL 修订并重建该文档的块。Git 来源返回明确的「切片未实现外部写回」，不要尝试推送 |

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
| `knowledge_submissions` | 已有表，切片不改列。发布到托管来源时走 `WriteDocument`；发布到 Git 来源返回未实现 |
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
- 私有 Git 的服务端凭据。切片只克隆不需要凭据的公开 HTTPS。
- 外部仓库的提交、推送或合并请求。
- 审核队列的自动入队、门禁、指纹和墓碑。
- 调用记录持久化。内存里的 `knowledgeActivityLog` 切片可以继续用，重启丢失是已知的，不在切片修。
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

切片验收通过之后，阶段一剩下的是第 5.2 节。本机测试在那之前不要求向量、写回推送或黄金案例。那些仍属于更后面的阶段。

## 8. 冲突与未决

下面各项本文不选替代方案。除特别写明「派发按此实现」的映射外，实现不要自行挑一条做进切片。

1. **历史版本的原文。** 方案既要求服务路径的读原文只读 MySQL、任何实例都能做，又写了外部仓库读取历史 commit 时按需从 Git 取。后者只有持有工作副本的实例能做，和多实例无状态读原文冲突。切片按第 3 节核对当前版本，其他 commit 直接报错。历史版本留在阶段一补全之外，直到这个冲突有结论。
2. **内容面放在哪个 Repo。** 方案写的是扩展 `KnowledgeRepo`，同时要求内容面可以换连接。`AGENTS.md` 又不允许往共享 `deps` 里加第二个数据库。派发按第 4.2 节实现：控制面扩展已有 `KnowledgeRepo`，内容面用新的 `KnowledgeContentRepo` 持有自己的 `*gorm.DB`。这是同时满足两条已定约束的映射，不是改掉其中一条。若要求改成单一 Repo 持有两个连接，先改本节再写代码。
3. **关键词召回是否合格。** 已定默认是 MySQL ngram，本文不判断它够不够用。对照用的 `lex` 只移植、不接线。召回对比的结论若是 ngram 明显更差，只更换第 3 节的关键词适配器。
4. **外部仓库怎么写回。** 直接推送还是合并请求，以及这件事放在阶段一还是阶段三，都还没定。切片对 Git 来源的 `WriteDocument` 返回未实现。不要在切片里做推送，也不要做合并请求。
5. **私有来源的 Git 凭据放哪。** 还没定。切片不新增凭据字段的存储设计，只支持公开 HTTPS 克隆。克隆失败把 Git 的错误摘要写到来源的 `last_error`。
