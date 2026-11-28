# 在桌面 VS Code 打开任务工作区

桌面 VS Code、网页 IDE 和 Agent 访问同一个 Run 的 Docker 工作区。桌面连接通过平台现有的 HTTPS 入口传输 SSH 流量。执行机器主动出站，任务沙箱通过 Docker exec 的标准输入和输出收发数据。平台和沙箱均无需为 SSH 新建监听端口。

## 平台配置

受支持的工作区默认提供桌面连接。HTTPS 代理需允许 WebSocket upgrade，并把桌面和 Runner 的 SSH 路由交给同一个 API 进程。Runner 工作区需要同一版本的 Runner 和 Linux helper。

首版支持 API 本地执行和通过 Runner 执行的 Docker 工作区。Runner 使用共享工作区布局。API 与执行引擎分离部署时，绑定在其他平台进程的工作区暂不可连接。入口遵循现有的工作区写权限，个人工作区仅向所有者开放。会话保活会刷新工作区的使用时间，工作区回收或权限失效会结束连接。

## 桌面配置

1. 安装桌面 VS Code、[Remote - SSH](https://marketplace.visualstudio.com/items?itemName=ms-vscode-remote.remote-ssh) 和同版本的 `agentflow-runner`。作为桌面连接程序使用时，仅需这个二进制。
2. 在 LoopForge 创建个人 API Token，并准备本地 OpenSSH 密钥及同名 `.pub` 文件。以下命令读取 Token 并把配置写到本机应用配置目录。它会打印一个 SSH 配置块，将配置块放到 `~/.ssh/config` 中通用的 `Host *` 条目之前。

   ~~~sh
   read -rs loopforge_desktop_token
   printf '%s\n' "$loopforge_desktop_token" | agentflow-runner desktop-ssh configure \
     -server https://loopforge.example \
     -identity ~/.ssh/id_ed25519
   unset loopforge_desktop_token
   ~~~

3. 在 LoopForge 会话的右侧面板点击「IDE」，再选「本地 Visual Studio Code」。VS Code 会打开沙箱中的 `/workspace`。从 LOOP 运行详情打开工作区时，可以使用工作区工具栏的「在 VS Code 打开」。

桌面连接程序通过平台验证 Run 的 SSH 主机密钥。Token 用于 HTTPS 请求，保存在本机权限为 `0600` 的配置文件中。浏览器启动链接只包含平台来源和运行 ID。

如果 VS Code Server 尚未安装，Remote - SSH 需要在沙箱内下载它。首版支持默认的 TCP 转发模式，转发目标限于沙箱本机地址。桌面会话使用现有沙箱用户和权限。

源码试用可以用 `go build -o ./bin/agentflow-runner ./cmd/agentflow-runner` 构建本机连接程序。桌面写回验收使用了 `"remote.SSH.useExecServer": false`，可以在独立的 VS Code 配置中复现。
