# AgentFlow 前端预览与交付物生命周期方案

> 状态：待评审
> 基线：`master@4ee2db5`（2026-09-19）
> 目标读者：负责 Engine、Runner、HTTP API、Studio 和基础设施的开发 Agent
> 核心原则：**Workspace 是生产过程，Preview 是交付资产。**

## 1. 背景

当前 AgentFlow 已支持在 Run 详情中通过内嵌 VS Code 浏览运行工作区，但成功交付后会立即清理工作区。这个行为对后端任务合理；对前端任务而言，如果预览依赖工作区和开发服务器继续存在，成功后预览也会一起消失。反过来，如果为了预览长期保留 workspace，又会把源码、`.git`、依赖、缓存、CLI Home 和 Docker writable layer 一起保留，磁盘成本不可控。

正确的抽象不是“前端 Run 不清理 workspace”，而是：

```text
运行中的 Workspace
      ↓ 交付收尾
提取 Preview Artifact
      ↓
销毁 Workspace / Sandbox
      ↓
保留轻量、受控、有 TTL 的 Preview
```

## 2. 当前 master 的事实

### 2.1 工作区与 UI

- Run 详情已有“工作区”Tab，通过 `/vscode.html?run=...` 嵌入只读 VS Code。
- VS Code 通过 `/api/runs/:id/workspace/*` 读取目录、文件、diff、日志和搜索结果。
- 工作区可能位于控制面，也可能位于 Runner；Runner 工作区通过现有控制通道读取。
- `workspaceCleanedAt` 一旦设置，UI 和 API 都明确显示工作区已删除。

相关代码：

- `web/app.js`：`vscodeWorkbenchURL`、`workspacePaneHTML`。
- `web/vscode.js`：只读 VS Code workbench。
- `internal/httpapi/run_workspace.go`：工作区读取和 Runner 转发。

### 2.2 成功后的清理

- Run 进入 `succeeded` 或 `cancelled` 后立即调用 `cleanupRunWorkspace`。
- 清理先将 `workspace_cleaned_at` 设为当前时间，使工作区立即不可再访问。
- 本地执行时销毁 Docker container、临时 image 和 workspace。
- Runner 执行时创建持久化 `cleanup_run` task，在 Runner 上删除容器和 `runs/<runId>`。
- 失败 Run 保留 workspace，供原地重试；重新派发后会回收旧容器。

相关代码：

- `internal/engine/engine.go`：`finish`、`cleanupWorkspace`。
- `internal/repo/runs.go`：`TerminalWorkspaceCleanupRuns`、`ClaimRunWorkspaceCleanup`。
- `internal/runtime/sandbox_manager.go`：`DestroyRun`。
- `internal/runner/daemon.go`：`executeRunCleanup`。

### 2.3 现有 Artifact 不能承载网站构建产物

现有 `run_artifacts` 面向 Markdown/JSON 等小型不可变报告：

- 内容存储在 MySQL `LONGTEXT`。
- Runner 完成接口最多 64 个文件。
- Runner 单文件最大 1MB。
- `ArtifactContract` 只描述单文件路径。
- 目前没有 blob/object storage 抽象、目录打包上传或二进制下载通道。

因此不能把 `dist/` 简单塞进现有 `run_artifacts.content`。

## 3. 目标

1. 成功 Run 仍能及时删除 workspace 和开发 sandbox。
2. 纯静态前端交付后可以长期或按 TTL 预览。
3. 预览内容和 AgentFlow 主站隔离，不能读取平台 Cookie、LocalStorage 或调用平台 API。
4. 本地执行和 Runner 执行使用同一种 Preview 元数据与存储协议。
5. 预览生成失败默认不改变业务交付结果，但必须可见；工作流可显式配置为必需。
6. 预览有容量、文件数、TTL、闲置停止、按项目配额和审计。
7. 为 SSR、服务型前端和复杂全栈环境保留演进路径，但 MVP 不为此永久保留 workspace。

## 4. 非目标

- MVP 不自动猜测任意项目的构建命令。
- MVP 不扫描监听端口并选择“最大的端口”。
- MVP 不把完整 workspace 打包成预览。
- MVP 不直接把容器 IP 暴露给浏览器。
- MVP 不在 AgentFlow 主站同源路径下执行不可信 HTML。
- MVP 不支持永久运行的预览容器。
- MVP 不把 Docker 本地 image 当作跨机器的长期交付资产。

## 5. 分层生命周期

```text
Hot：Run 正在执行
- 完整 workspace
- Sandbox / Docker container
- VS Code
- 可选开发服务器
- 生命周期跟随 Run

Warm：Run 已交付，可交互预览
- workspace 已删除
- static bundle 或 OCI image
- screenshot
- preview metadata
- TTL / quota / idle stop

Cold：预览已过期
- Git commit / MR / patch
- Run logs / telemetry
- 普通不可变 artifacts
- screenshot（可选长期保留）
- Preview bundle/image 已删除
```

建议默认策略：

| 类型 | Warm 资产 | 默认 TTL | 运行资源 |
|---|---|---:|---|
| 后端任务 | 无 | 0 | 成功后立即清理 |
| 静态 SPA | 压缩后的静态 bundle + screenshot | 30 天（硬上限） | 无常驻容器 |
| SSR/Node 服务 | OCI image + screenshot | 3 天 | 按需启动，闲置 10 分钟停止 |
| 复杂全栈 | Preview Environment bundle | 24 小时 | 显式启动、严格配额 |
| 已固定/Pin | 按项目策略保留 | 最大 30 天或管理员策略 | 仍受容量上限 |

所有默认值必须可配置，不应写死在前端。

## 6. 总体架构

```text
WorkflowSnapshot.preview
        |
        v
Run 完成节点执行
        |
        v
Preview Finalizer
  |-- 校验 PreviewContract
  |-- 本地或 Runner 打包
  |-- 上传 PreviewStore
  |-- 可选截图
  |-- 写 run_previews
        |
        v
Preview 状态进入 ready / failed
        |
        v
释放 cleanup hold
        |
        v
现有 workspace/sandbox 清理
        |
        v
Studio 获取短期 Preview Session
        |
        v
独立 preview origin 提供静态内容或反向代理
```

核心新增组件：

1. `PreviewContract`：随 WorkflowSnapshot 冻结的交付契约。
2. `PreviewFinalizer`：Run 成功与 workspace 清理之间的收尾阶段。
3. `PreviewStore`：二进制/目录交付物存储接口。
4. `run_previews`：预览元数据与状态。
5. `Preview Gateway`：独立 origin 的访问入口。
6. `Preview GC`：TTL、容量和孤儿资源清理。
7. Runner 的 preview archive/upload 内置 action。

## 7. PreviewContract

建议在 `domain.WorkflowDefinition` 增加 Run 级别的预览契约，并随 `WorkflowSnapshot` 冻结：

```go
type PreviewContract struct {
    Enabled      bool   `json:"enabled"`
    Kind         string `json:"kind"`          // static；后续 service/environment
    SourcePath   string `json:"sourcePath"`    // run workspace 相对路径
    IndexFile    string `json:"indexFile"`     // 默认 index.html
    SPAFallback  bool   `json:"spaFallback"`
    Required     bool   `json:"required"`
    CaptureShot  bool   `json:"captureScreenshot"`
    RetentionSec int64  `json:"retentionSec,omitempty"`
}
```

MVP 只接受 `kind=static`。

### 7.1 为什么不在 Finalizer 自动运行 npm build

构建属于工作流行为，应由可观测、可重试的 script/agent node 完成。Preview Finalizer 只负责验证和归档声明的目录，不执行模型生成的任意命令。

推荐 SOP：

```text
实现节点
  ↓
测试节点
  ↓
前端构建节点：npm ci && npm run build
  ↓
Preview Finalizer：归档 repo/dist
  ↓
工作区清理
```

这样构建日志和失败原因进入正常 Run 轨迹，构建失败可走现有 retry/onFailure，Finalizer 也不需要重新注入 Git、npm 或业务密钥。

### 7.2 路径规则

- `SourcePath` 必须是 run workspace 内的相对路径。
- 多仓库任务必须明确带目录，例如 `frontend/dist`。
- 禁止绝对路径、`..`、空路径和符号链接越界。
- 归档时拒绝 symlink、hardlink、device、socket 和 FIFO。
- 必须存在 `IndexFile`。
- 默认最大 20,000 个文件、压缩包 100MB、展开后 500MB；可配置但受平台硬上限约束。

## 8. 数据模型

新增独立表，不复用 `run_artifacts.content`：

```sql
CREATE TABLE run_previews (
  id VARCHAR(64) PRIMARY KEY,
  run_id VARCHAR(64) NOT NULL,
  project_id VARCHAR(64) NOT NULL DEFAULT '',
  kind VARCHAR(32) NOT NULL,
  status VARCHAR(32) NOT NULL,
  source_path VARCHAR(1024) NOT NULL DEFAULT '',
  storage_key VARCHAR(1024) NOT NULL DEFAULT '',
  content_sha256 CHAR(64) NOT NULL DEFAULT '',
  compressed_size BIGINT NOT NULL DEFAULT 0,
  expanded_size BIGINT NOT NULL DEFAULT 0,
  file_count INT NOT NULL DEFAULT 0,
  entry_file VARCHAR(512) NOT NULL DEFAULT 'index.html',
  spa_fallback TINYINT(1) NOT NULL DEFAULT 0,
  owner_runner_id VARCHAR(64) NOT NULL DEFAULT '',
  screenshot_key VARCHAR(1024) NOT NULL DEFAULT '',
  error_text TEXT,
  last_accessed_at DATETIME(3) NULL,
  expires_at DATETIME(3) NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uq_run_previews_run (run_id),
  KEY idx_run_previews_status_expiry (status, expires_at),
  KEY idx_run_previews_project (project_id, created_at)
);
```

状态建议：

```text
pending -> archiving -> ready -> expired -> deleting -> deleted
pending/archiving -> failed
pending -> skipped   # 工作区没有前端工程，不失败主流程
```

后续服务型预览再增加 `starting -> running -> stopped`。不要把容器 ID、动态端口当作长期真值；它们属于短期 Preview Session。

## 9. PreviewStore

定义独立接口：

```go
type PreviewStore interface {
    Put(ctx context.Context, key string, src io.Reader, size int64, sha256 string) error
    Open(ctx context.Context, key string) (io.ReadSeekCloser, PreviewObject, error)
    Delete(ctx context.Context, key string) error
}
```

实现顺序：

1. `DBPreviewStore`
   - tar.zst 存在 MySQL `preview_blobs`，按 SHA-256 去重。
   - 任意 API 实例按请求从库中取出成员，不依赖本机目录或 `AGENTFLOW_PREVIEW_ROOT`。
2. COS/S3 PreviewStore
   - 后续生产可选。
   - 支持分段上传、服务端加密和生命周期规则。
   - 数据库存 metadata，不保存大对象正文。

相同内容可通过 SHA-256 去重，但删除需要引用计数或在 GC 中确认没有其他 ready preview 引用。

## 10. 成功收尾与 workspace cleanup hold

当前 `finish(succeeded)` 会立即调用 cleanup。需要在该位置引入 Preview Finalizer，避免归档和删除竞态。

### 10.1 无 PreviewContract

保持当前行为：`succeeded -> immediate cleanup`。

### 10.2 有 static PreviewContract

```text
Run 标记 succeeded
  -> 创建 run_preview(status=pending)
  -> 建立 preview cleanup hold
  -> 异步/任务化归档
  -> ready 或 failed
  -> 释放 hold
  -> 调用现有 cleanupRunWorkspace
```

不要长期增加新的 Run 业务状态；Run 可以已经 `succeeded`，预览单独显示“生成中”。但 workspace 清理查询必须识别 hold。

建议增加通用字段 `runs.workspace_cleanup_not_before`，同时 `cleanupWorkspace` 在 claim 前检查：

- 是否有 `pending/archiving` preview。
- 是否超过 preview finalization 最大时限。
- 是否已有清理任务在执行。

默认 finalization 超时建议 15 分钟。超时后：

- `Required=false`：preview 标记 failed，继续清理，Run 保持 succeeded。
- `Required=true`：应通过显式构建/验证节点在 Run 成功前失败，不建议在已 succeeded 后反转状态。

为避免服务重启后泄漏，startup/sweep 必须恢复 pending preview、超时 hold，以及 ready/failed 后遗漏的 workspace cleanup。

## 11. 本地执行归档

控制面本地 sandbox：

1. 从 `Manager.RunWorkspace(runID)` 定位 workspace。
2. 在受限 walker 中读取 `SourcePath`。
3. 校验文件数、类型和展开大小。
4. 以确定顺序打包为 `tar.zst`，mtime/uid/gid 归一化，保证相同内容 hash 稳定。
5. 流式写入 `PreviewStore`，不把整个 bundle 读入内存。
6. 更新 preview metadata 为 ready。
7. 清理 workspace/container。

归档代码不能调用 shell `tar` 处理不可信路径，优先使用 Go `archive/tar` 和受控压缩库。

## 12. Runner 执行归档

现有 Runner artifact 完成接口只支持 JSON 字符串和 1MB 单文件，不能复用。

新增 Runner 内置 action：`archive_preview`。

任务携带：

```go
type RunnerPreviewArchiveSpec struct {
    PreviewID   string
    RunID       string
    SourcePath  string
    IndexFile   string
    MaxFiles    int
    MaxBytes    int64
    UploadURL   string
    UploadToken string
}
```

流程：

1. Engine 在 run 成功后向原 Runner 创建 `archive_preview` task。
2. Runner 在 `runs/<runId>` 下安全解析 SourcePath。
3. Runner 流式生成归一化 `tar.zst`。
4. Runner 使用一次性 upload token 上传到控制面专用端点。
5. 控制面边接收边计算 hash、限制压缩大小并写 PreviewStore。
6. 完成后 preview 进入 ready，Engine 再排队现有 `cleanup_run`。
7. 失败可重试，任务 ID 必须确定性且幂等。

建议端点：

```text
PUT  /runners/previews/:previewId/content
POST /runners/previews/:previewId/complete
```

要求：

- 使用 Runner session + preview upload token 双重约束。
- token 绑定 previewId、runId、runnerId、最大字节和过期时间。
- 不把大文件放进 runner_tasks JSON payload。
- 上传中断不得留下可见的 ready 对象。
- 先完成 PreviewStore 原子提交，再更新数据库。

Runner 离线时，preview 显示“等待执行机器上线”。超过 finalization timeout 后按非必需失败处理并释放 cleanup hold，不得为了预览无限保留 Runner workspace。

## 13. Preview Gateway 与安全边界

### 13.1 路径托管，不用独立 Origin

未配置 wildcard DNS 的部署不能使用 `https://<token>.preview.example.com/`。当前 MVP 使用同 Host 路径：

```text
https://loopforge.example.com/preview/<session-token>/
```

不要再引入 `AGENTFLOW_PREVIEW_ENABLED`、`AGENTFLOW_PREVIEW_ROOT`、`AGENTFLOW_PREVIEW_BASE_DOMAIN`。预览默认开启；静态包进 MySQL；`AGENTFLOW_PREVIEW_PUBLIC_URL` 只覆盖对外 origin（例如反代后的 `https://loopforge.example.com`）。

预览 HTML 仍是不可信内容。用短期 token、CSP `sandbox` + `connect-src`、以及运行时拦截 fetch/XHR 来约束它能访问的后端。主站 API 不在预览 token 路径下。

### 13.2 访问会话

Studio 调用 `POST /api/runs/:id/preview/session`。服务端验证用户对 Run 的读取权限后创建短期随机 session，返回完整 preview URL。

建议：

- token 至少 128 bit 随机值，数据库只存 hash。
- 默认 30 分钟过期，可在 iframe 活跃时续期。
- token 绑定 previewId，不放用户信息。
- 设置 `Referrer-Policy: same-origin`：带 token 的预览地址只会作为 Referer 发给本站，第三方和 `backendUrl` 收不到。网关据此把预览页发往根路径、而 history 垫片没有接住的整页导航（新标签页打开的链接、POST 表单、不支持 Navigation API 的浏览器里的 `location.href`）303 回 `/preview/<token>/` 下；页面自带的 `<meta name="referrer">` 一律改写为 `same-origin`。
- 访问记录只记 token hash 前缀，不记完整 URL。
- 会话可带 `backendUrl`；网关注入并拦截跨站/`/api` 请求转到该 origin。

### 13.3 iframe

```html
<iframe
  src="/preview/<token>/"
  sandbox="allow-scripts allow-forms allow-modals allow-downloads allow-same-origin"
  referrerpolicy="no-referrer"
></iframe>
```

路径托管下必须开 `allow-same-origin`，否则 Vite ESM 无法加载。不要添加 `allow-top-navigation`。剪贴板、摄像头、麦克风和定位默认不授权。

代价是预览与平台同源：预览脚本可以操作外层 Studio 页面（读写 DOM、借 `parent.fetch` 以当前登录用户身份调平台接口），CSP 的 `connect-src` 只约束预览文档自身。

### 13.4 静态响应

- 禁止目录列表。
- 精确 MIME 类型，并设置 `X-Content-Type-Options: nosniff`。
- HTML 只允许配置的 AgentFlow 主站 `frame-ancestors`。
- 静态 hash 资源可 immutable cache，HTML 使用 no-cache。
- `SPAFallback=true` 时，未命中文件且 Accept 包含 HTML 才回退 index。
- 在 `</head>` 前注入 history 垫片：SPA 路由按 `/` 写入的地址会被收回 `/preview/<token>/` 下。网关扫描预览包，只有用到 `pushState`/`replaceState` 的包才在启动时把地址临时换成去掉前缀的路径，直到路由第一次写 history 或注册 `popstate` 监听（兜底为 load 后 10 秒）；前进后退时只在事件分发期间去掉前缀。没有路由的包地址始终带前缀。
- 垫片把发往前缀外同源路径的整页跳转直接改到前缀下：左键点击链接（所有浏览器），以及 `location.href`/`replace`/`reload()` 和 GET 表单（Navigation API）。离开前先把当前历史记录恢复成带前缀的地址，后退时不会回到平台。
- 拒绝编码穿越、双重编码、NUL 和软链接。
- 不允许预览内容通过响应头设置 Cookie。
- screenshot/preview fetch 不携带平台凭据。

路径托管下 `location.pathname` 只有一个值：应用要的是 `/about`，地址栏、刷新和 Referer 要的是 `/preview/<token>/about`。垫片只能在一段时间内二选一，已知限制：

- 业务代码在启动后直接读 `location.pathname`，读到的是带前缀的路径。
- 有路由的包在路由启动前，独立窗口地址栏显示去掉前缀的地址，这时用浏览器刷新或复制地址会落到平台；load 后 10 秒仍未启动的路由会读到带前缀的路径。
- 启动期间在新标签页打开链接、提交 POST 表单，以及不支持 Navigation API 的浏览器里的脚本跳转，仍会落到平台。

独立 preview host 可以自然解决绝对 `/assets/*`、前端路由和 service worker base path 问题，比 path-based proxy 兼容性更高。

## 14. Studio 交互

Run 详情 Tab：

```text
[概览] [实时轨迹] [Worker日志] [工作区] [前端预览] [智能评测]
```

状态：

- 未配置：不展示 Tab，或显示“该 SOP 未声明前端预览”。
- `pending/archiving`：显示生成进度和来源路径。
- `ready`：显示 iframe、独立窗口、刷新和复制预览地址。
- `failed`：显示脱敏错误，不影响普通交付物查看。
- `expired`：显示 screenshot、过期时间和“重新构建”入口（后续能力）。
- Runner 离线：显示不可用原因和最终超时时间。

工作区与预览必须明确区分：

- “工作区”：运行过程、只读代码、跟随 workspace 生命周期。
- “前端预览”：交付结果、独立资产、跟随 Preview TTL。

前端不要根据 Run 状态猜测预览是否存在，统一读取 `GET /api/runs/:id/preview`。

## 15. Screenshot

Screenshot 是预览的降级展示，不应依赖 workspace 长期存在。建议在 preview ready 后由独立 screenshot worker：

1. 使用 Playwright 打开 Preview Gateway URL。
2. 等待 network idle 或最多 15 秒。
3. 使用固定 viewport（例如 1440×900）。
4. 保存 PNG/WebP 到 PreviewStore。
5. 写 `screenshot_key`。

截图失败不影响 preview ready。即使 bundle 过期，截图可以跟随 Run 更长时间保留。不要在 Agent 运行容器内安装浏览器来截图，这会增加 sandbox image 和 writable layer。

## 16. 服务型预览：Phase 2

静态预览稳定后再支持 Next.js SSR、Nuxt SSR 和 Node 服务。

### 16.1 交付物

不要保留原 sandbox container。应生成并推送 OCI image：

```text
workspace
  -> docker/buildkit build
  -> push preview registry
  -> record immutable image digest
  -> delete workspace and build container
```

必须配置专用 registry，不能仅保留 Runner 本地 image；否则 Runner 卸载、换机或磁盘清理后预览不可恢复。

可增加：

```go
type ServicePreviewContract struct {
    DockerfilePath string
    BuildContext   string
    ContainerPort  int
    HealthPath     string
    StartupTimeout int
    IdleTimeout    int
}
```

### 16.2 按需启动

用户打开预览：

1. 创建 `PreviewRuntimeSession`。
2. 选择原 Runner 或专用 preview runner。
3. pull 固定 digest。
4. 启动无凭据、只读 rootfs、资源受限的 container。
5. 等待健康检查。
6. Preview Gateway 代理 HTTP/WebSocket。
7. 无访问 10 分钟后停止 container。

### 16.3 Runner 隧道

远程 Runner 的容器 IP 对浏览器不可达。应扩展现有 Runner WebSocket control channel，增加多路复用 preview tunnel：

```text
Browser
 -> Preview Gateway
 -> runner control websocket
 -> Runner local reverse proxy
 -> preview container:port
```

协议需要 request/session ID、HTTP headers/body stream、WebSocket binary stream、背压、取消和限速。不要把完整 HTTP body 塞进单条 JSON control message。

本地控制面执行可以使用 `httputil.ReverseProxy`；两条路径应共享同一授权和 header 清洗策略。

### 16.4 代理要求

- 支持 WebSocket Upgrade。
- 清洗 hop-by-hop headers、`Set-Cookie`、`Location` 和外部重定向。
- 限制请求/响应 body、连接数、带宽和最长连接时间。
- 默认禁用出站网络或使用预览专用 egress policy。
- 不注入 Run 的 Git、CodeBuddy、Taihu 或 MCP 凭据。
- 容器使用独立网络、非 root、drop capabilities、no-new-privileges。

## 17. Full-stack Preview Environment：Phase 3

前端 + 后端 + MySQL/Redis 已经是临时部署，不是普通前端预览，必须显式 opt-in：

- 独立 Preview Environment 定义。
- 使用合成测试数据或声明式 seed，禁止复制生产数据库。
- 默认 TTL 24 小时。
- 项目级并发和总容量限制。
- 到期强制销毁计算、volume、DB 和缓存。
- 仅保留 screenshot、metadata、日志和构建摘要。

该阶段建议使用 Kubernetes/OpenSandbox，而不是继续扩展单机 Docker 特例。

## 18. API 建议

```text
GET    /api/runs/:id/preview
POST   /api/runs/:id/preview/session
POST   /api/runs/:id/preview/rebuild       # 后续
DELETE /api/runs/:id/preview/session/:sid
DELETE /api/runs/:id/preview               # 管理员/拥有者
```

返回示例：

```json
{
  "id": "preview-run-123",
  "runId": "run-123",
  "kind": "static",
  "status": "ready",
  "entryFile": "index.html",
  "fileCount": 142,
  "compressedSize": 5242880,
  "expandedSize": 18350080,
  "screenshotUrl": "/api/runs/run-123/preview/screenshot",
  "expiresAt": "2026-09-26T10:00:00Z",
  "lastAccessedAt": "2026-09-19T10:00:00Z"
}
```

Preview 内容不走 `/api`；由独立 preview host 根据 session token 解析。

## 19. GC、配额与幂等

后台周期任务处理：

- `expires_at <= now` 的 ready/failed preview。
- 数据库无引用的 PreviewStore 对象。
- uploading 临时文件。
- 已停止但未删除的 runtime container。
- registry 中无引用的 preview image。
- 超时的 workspace cleanup hold。

删除顺序：`标记 deleting -> 停止 runtime -> 删除 blob/image/screenshot -> 标记 deleted`。每一步必须幂等，可在进程重启后继续。

至少提供：

- 单 preview 压缩/展开大小。
- 单项目 preview 总字节。
- 单用户并发归档数。
- 单项目运行中 service preview 数。
- Runner 上传速率。
- Preview Gateway 并发连接和带宽。
- 最大 TTL 与 Pin 权限。

超过配额时 preview 失败，但默认不回滚已成功的业务 Run。

## 20. 可观测性

Run timeline 事件：

```text
preview.archive.queued
preview.archive.started
preview.archive.uploading
preview.archive.ready
preview.archive.failed
preview.session.started
preview.session.stopped
preview.expired
preview.deleted
```

指标：

- preview 生成成功率和耗时 P50/P95。
- bundle 压缩/展开大小。
- 每项目存储用量。
- session 启动耗时、活跃 session、闲置停止次数。
- Gateway HTTP 5xx / WebSocket 断开。
- preview hold 延迟 workspace 清理的时长。

日志不得包含完整访问 token、敏感 header 或预览页面正文。

## 21. 分阶段实施

### Phase 0：契约和存储基础

- 增加 `PreviewContract`、`run_previews`、`preview_blobs`、Repo CRUD。
- 实现 MySQL `PreviewStore`，不要本机目录开关。
- 预览默认开启，不增加 `AGENTFLOW_PREVIEW_ENABLED`。
- 无契约时不改当前 cleanup 行为。

### Phase 1：静态预览 MVP

- 仅支持 `kind=static`。
- 实现本地 Preview Finalizer。
- 实现 Runner `archive_preview` 和流式上传。
- 增加 cleanup hold、超时和启动恢复。
- 实现路径 `/preview/{token}/` 静态服务，不用独立 origin。
- Studio 增加“前端预览”Tab。
- 暂不截图、不支持运行时容器。

### Phase 1.5：截图与 GC

- screenshot worker。
- TTL 和配额 GC。
- Run 概览显示截图。
- 预览过期后仍展示 screenshot。

### Phase 2：服务型预览

- OCI registry 交付。
- Preview runtime session。
- HTTP/WebSocket proxy。
- Runner multiplex tunnel。
- idle stop 和资源限制。

### Phase 3：复杂环境

- 声明式依赖服务和 seed。
- Kubernetes/OpenSandbox 调度。
- volume/DB 生命周期和成本治理。

## 22. 建议代码拆分

新增：

```text
internal/domain/preview.go
internal/repo/previews.go
internal/preview/store.go
internal/preview/store_local.go
internal/preview/archive.go
internal/preview/service.go
internal/httpapi/run_preview.go
internal/httpapi/preview_gateway.go
internal/runner/preview_archive.go
```

修改：

- `internal/domain/workflow.go`：增加 `PreviewContract`。
- `internal/domain/run.go`：可增加轻量 preview summary。
- `migrations/schema.sql`、`internal/repo/models.go`、`internal/repo/automigrate.go`：表结构。
- `internal/engine/engine.go`：成功收尾先创建 preview finalization，再按 hold 清理。
- `internal/repo/runs.go`：cleanup eligibility/hold 查询。
- `internal/engine/runner_dispatch.go`：创建 preview archive task。
- `internal/domain/runner.go`：新增 action/spec，不复用文本 artifact map。
- `internal/runner/daemon.go`：执行安全目录归档与流式上传。
- `internal/httpapi/server.go`：管理 API、Runner upload、preview host 路由。
- `web/app.js`、`web/platform.css`：预览 Tab 和状态 UI。
- `docs/architecture.md`、`README.md`：生命周期和部署要求。

## 23. 测试矩阵

### 23.1 归档安全

- 正常 Vite/React `dist/`。
- SPA history fallback。
- 空目录、缺 index、超大文件、超多文件。
- symlink/hardlink/pipe/device。
- `../`、绝对路径、URL 编码穿越、双重编码。
- 打包过程中 workspace 被修改或删除。
- 相同内容生成相同 hash。
- 归档不包含 `.git`、目录外文件和凭据。

### 23.2 生命周期

- 无 preview 的成功 Run 保持立即清理。
- preview ready 后才清理 workspace。
- preview failed/timeout 后释放 hold。
- cancelled Run 不生成 preview 并立即清理。
- failed Run 保持现有 retry workspace。
- 服务重启恢复 pending archive 和 cleanup。
- 重复 finalizer、Runner completion、GC 均幂等。

### 23.3 Runner

- archive task 只能访问 `runs/<runId>`。
- Runner/Run/Preview/token 不匹配拒绝。
- 上传中断与重试。
- 超限时服务端中止读取。
- Runner 离线后超时释放 workspace。
- preview 成功后才下发 `cleanup_run`。

### 23.4 Gateway 安全

- Preview 与主站 origin 不同。
- 预览脚本无法调用 AgentFlow API。
- session token 过期、撤销、猜测失败。
- 无目录列表、无 symlink 逃逸。
- MIME、nosniff、CSP、frame-ancestors、Referrer-Policy。
- 主站 Cookie 不发送到 preview host。
- 跨项目用户无法创建访问 session。

### 23.5 UI

- building/ready/failed/expired 状态。
- iframe 与独立窗口。
- 工作区已清理但预览仍可用。
- preview 过期但 screenshot 可见。
- Run 切换时销毁旧 iframe/session。

## 24. 验收标准

静态 MVP 默认开启前必须满足：

1. 成功前端 Run 的 workspace 和 Docker sandbox 仍按现有策略删除。
2. `dist/` 在 workspace 删除后仍可预览。
3. 本地与 Runner Run 都能生成相同类型的 preview。
4. 预览页面与主站严格跨 origin，无法读取或使用主站身份。
5. 绝对 `/assets/*`、SPA 路由和常见静态框架可正常工作。
6. 归档失败不会无限阻塞 workspace 清理。
7. 大文件、路径穿越、symlink 和压缩炸弹被拒绝。
8. TTL 到期会回收 blob，数据库状态可审计。
9. feature flag 关闭后完全恢复当前行为。
10. 不把 preview 二进制写入 MySQL `run_artifacts.content`。

## 25. 给实施 Agent 的执行顺序

1. 先实现 `PreviewContract`、`run_previews` 和 `PreviewStore`，不要先做 iframe。
2. 为当前 `finish -> cleanup` 写失败测试：有 pending preview 时 workspace 不得被删除。
3. 完成本地 static archive，并验证 ready 后触发现有 cleanup。
4. 实现独立 preview origin 和安全测试，再接 Studio Tab。
5. 实现 Runner 流式 archive/upload，禁止走现有 1MB artifact JSON。
6. 增加 GC、配额和重启恢复。
7. 静态链路稳定后再设计 service preview tunnel；不要在 MVP 中混入动态端口扫描和 Docker 运行时代理。
