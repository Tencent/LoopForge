# LoopForge 知识库用户与运维指南

> 本文档基于 LoopForge「我的知识库」连接器架构编写，涵盖四大原生知识库（Obsidian、Notion、思源笔记、飞书知识库）、WeKnora 与通用 MCP 连接器的接入、绑定与排障。

---

## 1. 进入「我的知识库」与添加连接器

1. 登录 LoopForge 控制台，在左侧边栏点击 **「我的知识库」**（`data-view="knowledge"`）。
2. 页面提供四大主力原生连接器快捷卡片及扩展连接器入口：
   - **Obsidian**：接入本地 Obsidian Local REST API（默认 `https://127.0.0.1:27124`），填写 API Key 即可只读检索与读取笔记（自动排除 `.obsidian`、`.git` 等隐藏配置目录）。
   - **Notion**：使用 Notion Internal Integration Token 接入已授权的 Database 与 Page（自动将 Notion Block 转换为 Markdown）。
   - **思源笔记（SiYuan）**：接入思源本地内核（默认 `http://127.0.0.1:6806`），使用思源 `api.token` 鉴权。
   - **飞书知识库（Feishu）**：使用飞书企业自建应用的 `App ID` 与 `App Secret` 接入已授权给应用的知识空间（Wiki）与云文档。
   - **WeKnora / 通用 MCP**：在新建连接器下拉框中选择 `WeKnora`（填写服务地址与 `X-API-Key`）或 `自定义 MCP`（支持 Bearer Token、自定义 Header 与 OAuth 2.0 授权）。
3. 填写参数后可点击 **「测试连接」** 探测连通性与可见知识库/工具列表，确认无误后点击 **「保存」**。

---

## 2. 在 AI 工作台与 SOP 流程中使用知识库

1. **AI 工作台（对话）**：
   - 在输入框工具栏点击 **「知识库」** 选择器，勾选当前会话要启用的连接器及知识来源范围（全部可读来源 `all` 或指定来源白名单 `allowlist`）。
   - 也支持在提问时通过 `@角色`、`@SOP` 或 `@需求` 自动合并已开启知识库的角色或 SOP 节点所绑定的个人连接器。
   - 模型检索并引用知识后，回答正文中会展示 `[1]`、`[2]` 等引用角标，点击角标可展开底部 **「参考来源」** 卡片并定位到对应证据摘要与原文链接。
2. **SOP 流程设计**：
   - 在 SOP 画布中选中 **Agent 节点**，在右侧面板点击 **「关联知识库」** 选择要绑定的连接器与来源白名单；或在 **「角色定义」** 中打开角色的 **「接入知识库」** 开关。
   - 运行详情与产物面板会实时展示每次知识库调用轨迹、命中文档及最终被产物引用的证据。
3. **用量统计与审计**：
   - 在 **「我的知识库」** 页面下方可查看各连接器与热门文档的调用次数、命中次数、被引用次数（`citedCount`）及近期调用明细。

---

## 3. 核心环境变量与验证命令

| 环境变量 | 作用说明 |
|---|---|
| `LOOPFORGE_KNOWLEDGE_SECRET` / `AGENTFLOW_SESSION_SECRET` | 连接器凭据 AES-GCM 加密、OAuth State 签名与 `afkb1.` 代理令牌签发密钥（生产环境必填） |
| `LOOPFORGE_PUBLIC_BASE_URL` | 平台对外访问基准 URL（用于生成固定可信的 OAuth 回调地址 `redirect_uri`） |
| `AGENTFLOW_PLATFORM_MCP_URL` | 平台内部/外部基准 URL，供远程或容器 Runner 访问 `/runners/knowledge/mcp` |

### 本地开发与测试命令

```bash
# 编译并重启本地开发容器
./start.sh dev

# 查看平台容器最近日志
docker compose logs --tail 80 platform
```
