# WeKnora 知识库连接器接入指南

本指南说明如何在 LoopForge **「我的知识库」** 中接入已有 WeKnora 知识库服务，并在 AI 对话与 SOP 流程中只读检索与引用文档。

---

## 一、在 WeKnora 准备知识库与 API Key

1. 登录您的 WeKnora 控制台，创建知识库并完成 Embedding 模型绑定与文档上传（确保文档状态为 **已解析**）。
2. 在 WeKnora 控制台中创建或获取访问所需的 **`API Key`**（用于 `X-API-Key` 请求头鉴权）。

---

## 二、在 LoopForge 添加 WeKnora 连接器

1. 登录 LoopForge 控制台，进入左侧边栏 **「我的知识库」**。
2. 点击 **「新建连接器」**，在类型下拉框中选择 **`WeKnora`**。
3. 填写连接配置：
   - **名称**：例如 `研发团队 WeKnora`
   - **服务地址（Endpoint）**：填写 LoopForge 服务端可访问的 WeKnora 服务地址（例如 `https://weknora.example.com` 或内网服务地址）
   - **API Key**：粘贴 WeKnora 的 `X-API-Key`
4. 点击 **「测试连接」**，确认探测成功并列出可见知识库后，可按需限定允许访问的知识库白名单（`sourceScope`）与只读工具列表（`list_knowledge_bases`、`search_knowledge`、`list_documents`、`read_document`）。
5. 点击 **「保存」** 完成添加。

---

## 三、在对话与 SOP 中绑定使用

1. **AI 工作台对话**：点击对话输入框下方的 **「知识库」** 按钮，勾选刚创建的 WeKnora 连接器（可选择全部知识库或仅勾选指定知识库），发送问题后模型将自动检索并生成带 `[1]` 引用角标的回答。
2. **SOP 流程节点**：在流程设计器中选中目标 **Agent 节点**，点击 **「关联知识库」** 绑定该 WeKnora 连接器，运行 SOP 时节点将通过平台只读 MCP 代理安全检索 WeKnora 文档并记录引用统计。
