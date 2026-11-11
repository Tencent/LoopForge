# 部署指南

[English](deployment.md) | 简体中文

## 原生开发

`bash scripts/dev.sh` 会显式生成本地密钥，只启动当前检出目录的 MySQL 服务，构建平台，创建 `admin` 账号，通过显式脚本补齐缺失的可观测生成列，导入 `migrations/seed/*.sql`，并在 8090 端口启动服务。已有的目录定义通过 `INSERT IGNORE` 保留。`.env`、`.local/` 和数据库卷不要纳入版本控制。

原生安装时，API 主机需要 Node.js 22+，并安装你选用的执行器 CLI。需求澄清和定时助手任务也使用 CodeBuddy：

```bash
npm install --global @tencent-ai/codebuddy-code
codebuddy --version
# 可选：Main Bot 和 SOP 角色使用的其他执行器
npm install --global @openai/codex@0.162.1 @anthropic-ai/claude-code@2.1.300
```

确保服务能在 PATH 中找到 `codebuddy` 和 Node.js，或者用 `AGENTFLOW_CODEBUDDY_BIN` 指定可执行文件。模型 Key 及其网络环境在个人设置中配置。免 Key 的脚本体验不需要 CLI。生产 Docker 镜像内置 CodeBuddy 和 Codex。Claude Code 不随镜像再分发：第一次选择它的 Main Bot 对话或 SOP 角色会从 npm 把固定版本的 `@anthropic-ai/claude-code` 安装到 `$LOOPFORGE_DATA_DIR/cli`，因此首次运行要等待下载，之后复用；Docker 沙箱和已连接的执行机器各有自己的 CLI 要求。

前台服务用 Ctrl-C 停止，再次执行同一命令即可启动。MySQL 数据保存在 Compose 卷中。`docker compose stop mysql` 只停止数据库，不会删除数据。

使用自己的 MySQL 时设置 `AGENTFLOW_MYSQL_DSN`。新的账号命令或服务连接会创建缺失的表结构。已有安装的数据迁移请在审阅前置条件后逐个执行，不要对新数据库盲目重放全部历史迁移。

## 容器安装

```bash
./start.sh             # 安装或在后台启动（等同于 bash scripts/install.sh）
./start.sh logs        # 跟踪平台日志
./start.sh stop        # 停止；./start.sh down 删除容器但保留数据卷
./start.sh restart     # 重启平台容器
./start.sh pull        # 刷新镜像并重建有变化的容器
./start.sh dev         # 改为构建当前检出的源码（compose.dev.yaml）
./start.sh release     # 把源码构建切回发布镜像
```

安装脚本会检查 Docker、Compose 和 Python（缺少 Docker 时会询问是否安装；设置 `LOOPFORGE_YES=1` 可免确认），生成带唯一密钥的 `.env`，拉取并启动容器，补齐可观测字段，并导入 `migrations/seed/*.sql`。可以安全地重复执行。默认端口被占用时，在第一次运行前通过环境变量设置 `AGENTFLOW_HTTP_PORT` 或 `LOOPFORGE_MYSQL_PORT`（之后也可以直接改 `.env`）。

以安装所有者身份运行即可，包括普通 Linux 用户。如果该用户不能直接访问 Docker，安装脚本会验证 sudo，并在 Compose、表结构安装和 seed 中统一使用 `sudo -n docker`。每次操作前它会检查 sudo 凭据是否在长时间构建中过期：在终端里会重新提示输入，无人值守时则失败并给出说明。它不要求把用户加入 docker 组，也不需要重新登录。无人值守安装请先执行 `sudo -v`；`LOOPFORGE_YES=1` 只会自动接受安装确认，不会绕过操作系统权限。既没有 sudo 也没有 Docker 权限时会立即失败并给出说明。Rootless Docker 使用当前 context 的 Unix socket；远程 Docker context 需要单独配置部署，因为沙箱工作区是本地 bind mount。

`compose.yaml` 是发布镜像的编排文件，从不构建：`./start.sh`、`scripts/install.sh` 和直接执行 `docker compose --profile platform up` 都会拉取其中固定的 GHCR 发布版本（当前为 `ghcr.io/tencent/loopforge:0.3.4`）。这个版本与检出代码中的迁移和 seed 一一对应，所以安装时请检出发布 tag。只有需要覆盖它时才设置 `LOOPFORGE_IMAGE`（例如设为 `@sha256:` digest）；保存在 `.env` 中的值优先于之后的版本变更。

`./start.sh dev`（或 `LOOPFORGE_BUILD_FROM_SOURCE=1 bash scripts/install.sh`）通过 `compose.dev.yaml` 把当前检出构建为 `loopforge-platform:local`。它会在 `.env` 中写入 `COMPOSE_FILE=compose.yaml:compose.dev.yaml`，因此之后的运行和直接执行的 `docker compose` 命令都会继续使用源码构建，直到执行 `./start.sh release`。源码构建会复用 npm、Go 模块和 Go 编译缓存。

在 `main` 上推送 `vX.Y.Z` tag 后，等该提交的 `platform` workflow 通过，`release` workflow 就会运行。打 tag 的 `compose.yaml` 必须已经固定为 `loopforge:X.Y.Z`。它会把 `ghcr.io/<owner>/loopforge` 发布为 `X.Y.Z`、`X.Y`、`latest` 和 `sha-<commit>`，用 `./start.sh` 对推送的 digest 做冒烟测试，并创建 GitHub Release。Fork 会发布到自己的 owner 下；分发镜像前请为团队设置好 GHCR 包的可见性和访问权限。默认只在镜像缺失时拉取；请使用不可变的 tag 或 digest，或在更新可变 tag 时设置 `LOOPFORGE_PULL_POLICY=always`。已有的 `.env` 值优先；更新安装时请在其中修改保存的镜像或地址。Compose 会在有限时间内等待 API 健康检查通过，再安装表结构扩展和 seed。

在浏览器中打开平台并创建第一个账号，它会获得平台管理权限。在这个账号创建之前，登录页只提供这一步初始化。

初始化期间，请把云安全组或防火墙限制为只允许操作者的 IP 访问。管理员账号创建之前，不要把首个账号的初始化页面暴露给任意互联网客户端。

平台监听 `LOOPFORGE_BIND`，默认为 `0.0.0.0`，安装时不会询问访问地址。安装脚本会打印公网、局域网和本机地址；在腾讯云、阿里云、AWS、GCP 和 Azure 上，它会从云厂商的链路本地元数据服务读取公网 IP，在其他 NAT 环境下请自行使用公网 IP 访问。`LOOPFORGE_PUBLIC_URL` 是可选项，只影响打印出的地址。在云安全组或防火墙中放通 HTTP 端口，或者使用 HTTPS 反向代理。已有的 `.env` 会保留；没有 bind 设置的旧安装现在也监听 `0.0.0.0`。只需本机访问的安装可以设置 `LOOPFORGE_BIND=127.0.0.1`。反向代理需要转发 WebSocket 和流式请求。HTTPS 在代理处终止时，设置 `AGENTFLOW_SECURE_COOKIES=true`。多个 API 实例之间以及重启前后，`AGENTFLOW_SESSION_SECRET` 要保持不变。

### Docker 沙箱

对于以普通主机用户身份运行的原生服务，默认镜像和 root 镜像会以该服务用户的 UID/GID 运行，这样私有工作区无需放宽权限或增加 capability 也能访问。显式指定了非 root 用户的镜像会保留该身份；使用这类镜像时，运维需要自行保证 bind mount 的属主兼容。

Compose 部署会把宿主机 Docker socket 挂载进平台容器，因此内置的 AI SOP 开箱即用。`AGENTFLOW_ISOLATED_IMAGE` 指向已安装的平台镜像：默认沙箱用同一个镜像启动独立的同级容器，不需要再次下载 CLI 或构建镜像。沙箱不挂载 Docker socket。自定义 Dockerfile 仍会单独构建并缓存自己的镜像。运行工作区挂载在 Compose 网络中的 `/workspace`。由于 bind mount 由宿主机 Docker daemon 解析，`LOOPFORGE_DATA_DIR`（由 `scripts/configure.sh` 写入 `.env`，默认 `./.data`）会以相同的绝对路径挂载进平台容器；工作区和 CLI 状态都保存在其下。

访问 Docker socket 等同于拥有宿主机 root 权限，因此有两项保护措施：平台在沙箱容器中运行每个任务，包括配置为 `local` 驱动的 SOP（`AGENTFLOW_LOCAL_SANDBOX=docker`）；只有平台管理员才能把仓库指向宿主机源码路径。即便如此，能运行 SOP 的人都可以在这台主机的容器里执行代码，所以只给你信任的人开账号，也不要把服务暴露给不可信的网络。如果不想挂载 socket，从 `compose.yaml` 中删除 `docker.sock` 卷和 `AGENTFLOW_LOCAL_SANDBOX` 设置，再为 AI SOP 连接一个单独的、具备 Docker 的 Worker 或执行机器；这时只含脚本的欢迎 SOP 会在平台容器内运行。

### 慢网络下的克隆与镜像构建限制

克隆仓库和构建自定义沙箱镜像时会持续报告进度，出错时保留 git 或 Docker 输出的最后几行。在平台主机上，进度记录在运行详情中；在执行机器上，进度写入 runner 日志，只有最终错误会上报给平台。一个步骤在其停滞窗口内没有任何输出，会被视为卡住并停止；持续有输出的步骤可能很慢，但只会被总时长限制停止。所有设置都使用 Go 时长格式，例如 `90s` 或 `20m`；`0` 表示关闭该限制。每台主机读取自己的环境变量，因此执行机器上也要设置克隆限制。

| 设置 | 默认值 | 作用于 |
|---|---|---|
| `AGENTFLOW_GIT_CLONE_STALL_TIMEOUT` | `5m` | 没有输出的 `git clone`。平台从仓库的本地源码路径克隆时不生效，因为最后的 repack 阶段没有输出。 |
| `AGENTFLOW_GIT_CLONE_TIMEOUT` | 平台上关闭，执行机器上为 `10m` | 整个 `git clone`。执行机器沿用之前固定的 10 分钟限制作为默认值。 |
| `AGENTFLOW_DOCKER_BUILD_STALL_TIMEOUT` | `10m` | 没有输出的 `docker build`。安静的 `RUN` 步骤（例如长时间编译）也算没有输出。 |
| `AGENTFLOW_DOCKER_BUILD_TIMEOUT` | 关闭 | 整个 `docker build`。 |

以这种方式停止的克隆不会重试，因为重试仍会卡在同样的网络上。执行主机访问 GitHub 或 Debian 镜像源很慢时，请为 git 配置代理（`git config --global http.proxy`），并在 Dockerfile 中使用就近的软件包镜像源，而不是调大这些限制。

## 账号

在浏览器中创建的第一个账号成为平台管理员。之后，只要 `AGENTFLOW_ALLOW_SIGNUP` 为 `true`（默认），能访问登录页的人都可以注册为普通用户。注册用户可以创建需求并在你的 Worker 上运行 SOP，因此对不可信网络可达的安装，请设置 `AGENTFLOW_ALLOW_SIGNUP=false`，改为显式创建账号。

平台管理员在 **平台管理 → 成员账号** 中创建账号：填写登录名和初始密码，可选授予管理权限，再把密码交给同事。同一页面列出所有账号，并可重置密码账号的密码；通过 GitHub 或网关登录的账号不能在这里设置密码。新账号与自助注册一样会加入欢迎项目。

LoopForge 假设使用者是可信团队。每个已登录账号都能列出所有项目、需求和运行。项目设置和仓库只能由项目所有者或平台管理员修改，个人凭据只能由本人修改。不要用一个安装实例来隔离互不信任的租户。

没有浏览器时，原生安装可以导出 `.env` 中的值，然后执行显式的账号命令：

```bash
set -a; source .env; set +a
read -r -s -p 'Password: ' account_password; echo
printf '%s\n' "$account_password" | go run ./cmd/loopforge-account -user alice
unset account_password
```

只有授予平台管理权限时才使用 `-admin`。再次执行该命令会轮换该账号的密码。密码哈希使用 bcrypt。浏览器会话使用签名的 HttpOnly、SameSite Cookie，七天后过期。不会隐式地给任何具名个人授予管理员角色；其他管理员可以在现有的平台管理界面中管理。

## Worker 与执行机器

上游进程角色仍为 `-role api`、`-role worker` 和 `-role all`。API 和 Worker 实例必须共享 MySQL 数据库，并使用与各自部署相符的配置。拆分控制与执行时，设置 `AGENTFLOW_CONTROL_TOKEN`、`AGENTFLOW_CONTROL_URL` 和 `AGENTFLOW_WORKER_URL`。把 `AGENTFLOW_PLATFORM_MCP_URL` 设为执行容器实际可访问的平台地址。

个人执行机器在执行机器页面通过短时有效的绑定码连接。安装脚本由 `/runners/install.sh` 提供。`AGENTFLOW_RUNNER_BINARY` 可以指向带 `{os}` 和 `{arch}` 占位符的二进制地址。容器镜像内置 Linux/macOS amd64/arm64 的 runner 和 gate 二进制。原生源码安装可以按需用 Go 构建。

平台管理员可以在 **平台管理 → 平台共享 CodeBuddy Key** 中共享一个 CodeBuddy API Key：选择签发它的网络环境，以及成员可以使用的模型（第一个为默认模型）。没有自己 Key 的成员会用它对话和运行 SOP，并且只能使用这些模型；配置了其他模型的角色会改用默认模型运行，并在运行中注明。有自己 Key 的成员继续用自己的 Key 和任意模型。定时任务、需求澄清、报告、机器人和运行评估仍需要个人 Key。共享 Key 和个人 Key 一样存储在数据库中，保存后不会再显示。在没有配置共享 Key 时，`AGENTFLOW_ASSISTANT_SPONSOR_LOGIN`（把某个账号的 Key 借出、仅限 `deepseek-v4.1-flash`）仍然生效。

模型服务的凭据请在平台界面中配置，不要配置在主机全局的 CLI 配置里。上游运行时会把凭据绑定到负责的操作者。首页输入框和 SOP 角色分别支持 CodeBuddy、Codex 和 Claude Code。在首页模型选择器旁选择执行器；它的 API Key（以及可选的 Codex/Claude base URL）来自你的个人设置。切换执行器会保留草稿；保存的对话会记录所用的执行器。Main Bot 的写操作仍经过平台工具网关及其审批检查。需求澄清、定时助手任务和通知继续使用 CodeBuddy。真实模型测试需要显式开启；只有需要加载本地凭据的验收套件才设置 `AGENTFLOW_LIVE_TESTS=1`。

## 可观测字段

基础表创建之后，对 Compose 数据库显式执行 `bash scripts/install-observability-schema.sh`。它会根据上游迁移 `001_run_obs_generated_columns.sql` 补齐缺失的 `run_telemetry.adapter` 和 `run_logs.level` 列及其索引。本地开发安装脚本会在安装模板之前调用这一步。可以重复执行，不会重复添加已有的列。应用启动时不会执行数据迁移。

使用外部 MySQL 时，请先审阅该迁移，确认列是否已存在，再执行对应语句。存储型生成列可能会重建大表，请据此安排执行时间。

## 更新已有的公共 SOP

所有内置通用 SOP 都可以不绑定仓库。要在已有安装上更新原始目录说明，显式执行：

```bash
docker compose exec -T mysql sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -u loopforge loopforge' < migrations/016_public_optional_repositories.sql
```

这段 SQL 可以重复执行，只修改仍与原始公共 seed 一致的说明字段，会保留运维的修改和项目内的副本。新安装已经从公共目录 seed 中获得这些说明；应用启动时不会迁移数据。

## GitHub 凭据

用户可以在个人凭据中或添加仓库时选择 **连接 GitHub**，复制显示的 PIN 码，然后在 GitHub 上批准。平台会把授权保存到该登录用户已有的 Git 凭据槽位。仓库选择器会列出该账号拥有的、共享给它的、或通过所属组织可访问的仓库，支持分页和在已加载仓库中搜索，并把所选仓库真实的默认分支填入表单。仍然可以手动填写 PAT。

新安装使用内置的 LoopForge OAuth App。用户不需要创建应用、配置 Client ID 或申请 PAT：选择 **连接 GitHub**，复制 PIN 码，在 GitHub 上批准 LoopForge 应用即可。GitHub 的确认步骤无法绕过；设备授权并不是零交互登录。

应用配置属于部署层面，不在个人凭据中显示。需要使用自己应用的运维可以注册一个 OAuth App，启用 **Device Flow**，在支持刷新之前保持关闭 token 过期，然后在 `.env` 中用 `AGENTFLOW_GITHUB_CLIENT_ID` 设置其公开的 Client ID。不需要 client secret。部署环境变量会覆盖内置应用和任何数据库覆盖值。仅管理员可用的 `PUT /api/auth/github/config` 接口仍可用于显式的数据库覆盖；空的 Client ID 表示显式关闭设备授权，而不是静默恢复默认值。已有的显式配置会保留。

授权流程申请 OAuth `repo` 权限，以支持私有仓库克隆、推送、Issue 和 PR 操作。GitHub 的 OAuth 权限覆盖所有可访问的仓库；在 LoopForge 中选择仓库只是把它绑定到项目，并不会缩小 OAuth 授权范围。组织的 SSO 或第三方应用限制可能需要组织批准。参见 [GitHub 设备授权文档](https://docs.github.com/zh/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow)。待完成的授权会过期，并且只保存在 API 内存中：多个 API 实例之后请为 `/api/auth/github/*` 配置会话亲和，API 重启后需要重新授权。Token 和 GitHub 设备码不会发送到浏览器。

GitHub Issue 导入和 PR 状态同步使用项目所有者的 Git token。Git 操作和可选的 GitHub MCP 集成使用负责用户的 token。公开读取可能不需要 token；私有仓库和写操作需要仓库权限。GitHub MCP 必须显式安装，并且只把凭据发送到其官方 HTTPS 端点。

平台创建的 Git 提交使用负责的本地账号名，以及不可投递的 `@users.noreply.loopforge.invalid` 邮箱。本地账号并不代表企业邮箱或经过验证的 GitHub 身份。

## CodeBuddy 认证环境

在个人设置中选择 API Key 所属的网络环境；「获取 API Key」链接会打开对应版本的 Key 页面。某个版本的 Key 发送到另一个版本时会返回 HTTP 401；第三方模型服务（DeepSeek、OpenAI 等）的 Key 不是 CodeBuddy Key。该设置按用户保存；公开安装不会默认企业环境。

| 版本 | 存储值 | Key 页面 |
| --- | --- | --- |
| 国际版 | 空 | https://www.codebuddy.ai/profile/keys |
| 中国版 | `internal` | https://copilot.tencent.com/profile/ |
| iOA | `ioa` | https://tencent.sso.copilot.tencent.com/profile/keys |

`PUT /api/users/:id/credentials` 只接受这些值（也识别 `public` 和 `China`），其他值一律以 HTTP 400 拒绝，因为 CLI 遇到无法识别的值会静默回落到国际版。

其他专属或自托管配置见 [CodeBuddy 官方身份文档](https://www.codebuddy.ai/docs/cli/iam)。用 `AGENTFLOW_ASSISTANT_MODEL` 和 `AGENTFLOW_ASSISTANT_MODELS` 配置模型目录；普通角色可以独立选择模型。

## 隔离容器验收

执行 `python3 scripts/test-install.py -v` 运行安装脚本的回归测试，覆盖 sudo 选择、直接访问 Docker、无人值守时的权限失败、重复安装、镜像模式和访问地址。有本地构建的平台镜像时，`LOOPFORGE_INSTALL_TEST_IMAGE=your-image bash scripts/test-install-live.sh` 会在随机端口上的隔离 Compose 项目中，验证真实的 root 和 UID 65534 安装、非回环地址的 HTTP 访问、首个账号创建和重复安装。这个真机测试需要 Linux root 测试机来降权运行；结束后会删除自己的容器和卷。如果还要在一次性的 UID 1000 安装容器中测试真实的 sudo，用 `--build-arg LOOPFORGE_TEST_BASE=your-image` 构建 `scripts/testdata/install-sudo.Dockerfile`，再把这个测试镜像作为 `LOOPFORGE_INSTALL_SUDO_TEST_IMAGE` 传入。生产安装脚本本身不需要 root。

安装好 `e2e/` 依赖和 Chromium 后，在仓库根目录执行 `bash scripts/test-container.sh`。它会构建生产镜像，在 8093/33317 端口创建隔离的 Compose 项目和数据库，创建测试账号，显式导入可观测字段基线和公共目录，检查内置 CLI 和跨平台的 runner/gate 二进制，并运行公共浏览器测试套件。结束后会删除其容器和卷。端口被占用时，用 `LOOPFORGE_CONTAINER_TEST_PORT` 和 `LOOPFORGE_CONTAINER_MYSQL_PORT` 覆盖。这项干净安装检查会禁用模型调用。
