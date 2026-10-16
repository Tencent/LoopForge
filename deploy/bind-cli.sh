#!/usr/bin/env bash
# AgentFlow 绑定本机 CLI（bind-cli）。
#
# 在你的本地机器或 CVM 上执行，把这台机器的 Codex / Claude Code CLI
# 接入平台：角色绑定这台「执行器」后，任务会路由到本机 CLI 执行，
# CLI 登录凭证始终保留在本机。未绑定的角色仍走云端 API Key 模式。
#
# 用法（绑定码从平台「个人 → 执行器」页面复制的命令中自动携带）：
#   curl -fsSL https://<平台地址>/runners/install.sh | bash -s -- \
#     --server https://workflow.example.com --token <执行器绑定码或接入令牌>
#
# 可选参数：
#   --name <名称>          机器显示名（默认主机名）
#   --concurrency <N>      最大并发任务数（默认 1）
#   --bin-dir <目录>       二进制安装目录（默认 ~/.local/bin）
#   --from <文件>          使用本地已编译的 agentflow-runner 二进制
#   --download-url <URL>   从指定 URL 下载二进制（含 {os} {arch} 占位符）
#   --source <目录>        平台未分发二进制时，从本地 AgentFlow 源码构建（需要 Go）
#   --strict-https         禁止在 HTTPS 网络不可达时降级到 HTTP
#   --service              安装 launchd (macOS) / systemd (Linux) 常驻服务
#   --no-start             只安装配置，不立即启动
#   --no-auto-update       禁止从平台自动更新（自定义二进制默认禁用）
#   --upgrade              原地升级已绑定的 runner，保留配置、机器绑定和工作区
#   --uninstall            卸载：通知平台注销机器、停止服务、删除全部文件和配置
#
# 默认直接从平台下载对应 OS/架构的二进制（由服务端 /runners/agentflow-runner 分发）。
# 脚本安装 agentflow-runner 和跨沙箱 Gate MCP helper、写配置并启动
# daemon。Runner 仅向平台发起出站 HTTPS，不开放任何入站端口。

set -euo pipefail

SERVER=""
TOKEN=""
NAME=""
CONCURRENCY="1"
BIN_DIR="${HOME}/.local/bin"
LOCAL_BINARY=""
DOWNLOAD_URL=""
SOURCE_DIR=""
INSTALL_SERVICE=0
NO_START=0
UNINSTALL=0
UPGRADE=0
STRICT_HTTPS=0
AUTO_UPDATE=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server) SERVER="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --name) NAME="${2:-}"; shift 2 ;;
    --concurrency) CONCURRENCY="${2:-1}"; shift 2 ;;
    --bin-dir) BIN_DIR="${2:-}"; shift 2 ;;
    --from) LOCAL_BINARY="${2:-}"; shift 2 ;;
    --download-url) DOWNLOAD_URL="${2:-}"; shift 2 ;;
    --source) SOURCE_DIR="${2:-}"; shift 2 ;;
    --strict-https) STRICT_HTTPS=1; shift ;;
    --service) INSTALL_SERVICE=1; shift ;;
    --no-start) NO_START=1; shift ;;
    --no-auto-update) AUTO_UPDATE=0; shift ;;
    --upgrade) UPGRADE=1; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done

# 自定义产物的生命周期由提供者管理，避免平台自动覆盖。
if [[ -n "$LOCAL_BINARY" || -n "$DOWNLOAD_URL" || -n "$SOURCE_DIR" ]]; then
  AUTO_UPDATE=0
fi

fail() { echo "[bind-cli] 错误: $*" >&2; exit 1; }
info() { echo "[bind-cli] $*"; }

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64|amd64) ARCH="amd64" ;;
  *) fail "不支持的架构: $ARCH" ;;
esac
[[ "$OS" == "darwin" || "$OS" == "linux" ]] || fail "不支持的系统: ${OS}（Windows 请使用 PowerShell 安装方式）"

RUNNER_HOME="${HOME}/.agentflow-runner"
ENV_FILE="${RUNNER_HOME}/env"
STATE_FILE="${RUNNER_HOME}/state.json"
BINARY="${BIN_DIR}/agentflow-runner"
GATE_BINARY_PATTERN="${BIN_DIR}/agentflow-gate-mcp-{os}-{arch}"
PLIST="${HOME}/Library/LaunchAgents/com.agentflow.runner.plist"
UNIT="${HOME}/.config/systemd/user/agentflow-runner.service"

# ---------- 卸载 ----------
if [[ "$UNINSTALL" == "1" ]]; then
  info "停止并移除 agentflow-runner..."

  # 先停止 daemon，避免注销 API 返回后存活进程立即用 bootstrap token
  # 重新注册；state/env 暂时保留，供下面的注销请求使用。
  if [[ "$OS" == "darwin" ]]; then
    [[ -f "$PLIST" ]] && launchctl unload "$PLIST" >/dev/null 2>&1 || true
  else
    systemctl --user stop agentflow-runner.service >/dev/null 2>&1 || true
  fi
  pkill -f 'agentflow-runner -server' >/dev/null 2>&1 || true

  # 先从 state.json 读取 server URL 和 session token，直接调 API 注销机器。
  # 这样不依赖 daemon 二进制版本：即使 daemon 是旧版（没有主动注销逻辑），
  # 服务端也能立即感知并删除机器记录。
  if [[ -f "$STATE_FILE" ]]; then
    SERVER_FROM_STATE=$(sed -n 's/.*"serverUrl"\s*:\s*"\([^"]*\)".*/\1/p' "$STATE_FILE" 2>/dev/null || true)
    SESSION_TOKEN=$(sed -n 's/.*"sessionToken"\s*:\s*"\([^"]*\)".*/\1/p' "$STATE_FILE" 2>/dev/null || true)
    if [[ -n "$SESSION_TOKEN" ]]; then
      # 从 env 文件读取 server URL（state.json 里可能没有 serverUrl）
      if [[ -z "$SERVER_FROM_STATE" ]] && [[ -f "$ENV_FILE" ]]; then
        SERVER_FROM_STATE=$(sed -n 's/^AGENTFLOW_SERVER_URL=//p' "$ENV_FILE" 2>/dev/null | head -1 || true)
      fi
      if [[ -n "$SERVER_FROM_STATE" ]]; then
        info "通知平台注销机器..."
        curl -fsSL -X POST -H "Authorization: Bearer $SESSION_TOKEN" \
          -H "Content-Type: application/json" -d '{}' \
          "$SERVER_FROM_STATE/runners/deregister" >/dev/null 2>&1 \
          && info "平台已注销机器" \
          || info "通知平台失败（可能是网络问题，服务端会通过心跳超时自动清理）"
      fi
    fi
  fi

  if [[ "$OS" == "darwin" ]]; then
    if [[ -f "$PLIST" ]]; then
      launchctl unload "$PLIST" >/dev/null 2>&1 || true
      rm -f "$PLIST"
      info "已移除 launchd 服务"
    fi
  else
    systemctl --user disable --now agentflow-runner.service >/dev/null 2>&1 || true
    rm -f "$UNIT"
    systemctl --user daemon-reload >/dev/null 2>&1 || true
    info "已移除 systemd 服务"
  fi
  # 回收异常退出时可能遗留的 runner 专属容器和临时镜像。名称严格
  # 限定为 agentflow-rt-*，不会碰同机控制面或用户自己的容器。
  if command -v docker >/dev/null 2>&1; then
    while IFS= read -r container; do
      [[ -n "$container" ]] || continue
      docker rm -f "$container" >/dev/null 2>&1 || true
    done < <(docker ps -aq --filter 'name=^/agentflow-rt-' 2>/dev/null || true)
    while IFS= read -r image; do
      [[ -n "$image" ]] || continue
      docker rmi -f "$image" >/dev/null 2>&1 || true
    done < <(docker images --format '{{.Repository}}' 2>/dev/null | awk '/^agentflow-tmp-rt-/' || true)
    info "已清理 runner Docker 沙箱和临时镜像"
  fi
  rm -f "$BINARY"
  rm -f "${BIN_DIR}"/agentflow-gate-mcp-darwin-amd64 \
    "${BIN_DIR}"/agentflow-gate-mcp-darwin-arm64 \
    "${BIN_DIR}"/agentflow-gate-mcp-linux-amd64 \
    "${BIN_DIR}"/agentflow-gate-mcp-linux-arm64
  rm -rf "$RUNNER_HOME"
  info "已删除二进制 ${BINARY}"
  info "已删除配置目录及全部工作区 ${RUNNER_HOME}"
  info "卸载完成。"
  exit 0
fi

if [[ "$UPGRADE" == "1" ]]; then
  [[ -f "$ENV_FILE" && -f "$STATE_FILE" && -f "$BINARY" ]] || fail "未发现完整的 runner 安装，无法原地升级"
  if [[ -z "$SERVER" ]]; then
    SERVER=$(sed -n 's/^AGENTFLOW_SERVER_URL=//p' "$ENV_FILE" | head -1)
  fi
else
  [[ -n "$TOKEN" ]] || fail "必须提供 --token（执行器绑定码，或管理员下发的接入令牌）"
fi
[[ -n "$SERVER" ]] || fail "必须提供 --server <平台地址或主机名>"

# 默认优先 HTTPS。仅连接、DNS、超时或 TLS 层失败时允许降级；如果 HTTPS
# 已经可达但返回 HTTP 错误，则不降级，避免把服务端故障误判成网络隔离。
probe_server() {
  curl -fsS --connect-timeout 3 --max-time 8 "$1/runners/health" >/dev/null 2>&1
}

resolve_server() {
  local requested="${SERVER%/}" host https_url http_url status
  case "$requested" in
    http://*)
      SERVER="$requested"
      echo "[bind-cli] 警告: 使用明文 HTTP，绑定码和任务通信仅适用于可信内网" >&2
      return
      ;;
    https://*) host="${requested#https://}" ;;
    *://*) fail "--server 仅支持 http://、https:// 或不带协议的主机名" ;;
    *) host="$requested" ;;
  esac
  [[ -n "$host" ]] || fail "--server 主机名不能为空"
  https_url="https://${host}"
  http_url="http://${host}"
  info "探测平台连接：优先 ${https_url}"
  if probe_server "$https_url"; then
    SERVER="$https_url"
    return
  else
    status=$?
  fi
  if [[ "$STRICT_HTTPS" == "1" ]]; then
    fail "HTTPS 连接失败（curl exit ${status}），且已启用 --strict-https"
  fi
  case "$status" in
    5|6|7|28|35|60) ;;
    *) fail "HTTPS 已响应但健康检查失败（curl exit ${status}），为避免不安全降级已停止" ;;
  esac
  info "HTTPS 网络不可达，尝试降级到 ${http_url}"
  if ! probe_server "$http_url"; then
    fail "HTTPS 与 HTTP 均无法连接平台，请检查 DNS、防火墙和服务端口"
  fi
  SERVER="$http_url"
  echo "[bind-cli] 警告: 已自动降级到明文 HTTP；仅应在可信隔离网络中使用" >&2
}

resolve_server

# ---------- 0. 预检本机 CLI ----------
info "检测本机 Agent CLI..."
for cli in claude codex codebuddy; do
  if command -v "$cli" >/dev/null 2>&1; then
    info "  发现 $cli: $(command -v "$cli")（$("$cli" --version 2>/dev/null | head -1 || echo 版本未知)）"
  else
    echo "[bind-cli] 提示: 未找到 ${cli}，该 CLI 不会接入平台（可先完成 CLI 登录后重新执行本脚本）" >&2
  fi
done

# ---------- 1. 安装二进制 ----------
install_binary() {
  mkdir -p "$BIN_DIR"
  TEMP_BINARY=$(mktemp "${BIN_DIR}/.agentflow-runner.XXXXXX")
  if [[ -n "$LOCAL_BINARY" ]]; then
    [[ -f "$LOCAL_BINARY" ]] || fail "本地二进制不存在: $LOCAL_BINARY"
    cp "$LOCAL_BINARY" "$TEMP_BINARY"
  elif [[ -n "$DOWNLOAD_URL" ]]; then
    URL="${DOWNLOAD_URL//\{os\}/$OS}"
    URL="${URL//\{arch\}/$ARCH}"
    info "从 ${URL} 下载二进制..."
    curl -fsSL "$URL" -o "$TEMP_BINARY"
  else
    # 默认：平台直接分发对应 OS/架构的二进制，无需本机 Go 工具链。
    info "从 ${SERVER} 下载 agentflow-runner（${OS}/${ARCH}）..."
    if curl -fsSL "${SERVER}/runners/agentflow-runner?os=${OS}&arch=${ARCH}" -o "$TEMP_BINARY"; then
      :
    else
      # 平台未配置分发：回退到本地源码构建（--source 或当前目录为源码 checkout）。
      AUTO_UPDATE=0
      SRC="${SOURCE_DIR:-$PWD}"
      if [[ ! -d "$SRC/cmd/agentflow-runner" ]]; then
        fail "平台未提供二进制且未找到源码。解决方式任选其一：
  1) 平台管理员设置 AGENTFLOW_SOURCE_ROOT（源码目录）或 AGENTFLOW_RUNNER_BINARY（预编译产物）后重启服务
  2) 本命令追加 --source <AgentFlow 源码目录>（需要本机安装 Go）
  3) 本命令追加 --from <已编译的 agentflow-runner 二进制路径>"
      fi
      command -v go >/dev/null 2>&1 || fail "本机未安装 Go，无法从源码构建；请使用 --from 提供二进制"
      info "从源码 ${SRC} 构建（${OS}/${ARCH}）..."
      (cd "$SRC" && CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build -o "$TEMP_BINARY" ./cmd/agentflow-runner) \
        || fail "源码构建失败；请检查 Go 版本（需 1.22+）或改用 --from"
    fi
  fi
  [[ -s "$TEMP_BINARY" ]] || fail "下载或构建的 runner 二进制为空"
  chmod +x "$TEMP_BINARY"
  mv -f "$TEMP_BINARY" "$BINARY"
  TEMP_BINARY=""
}

TEMP_BINARY=""
trap '[[ -z "$TEMP_BINARY" ]] || rm -f "$TEMP_BINARY"' EXIT
install_binary

install_gate_binary() {
  local target_os="$1" target_arch="$2" target
  target="${GATE_BINARY_PATTERN//\{os\}/$target_os}"
  target="${target//\{arch\}/$target_arch}"
  info "从 ${SERVER} 下载 Gate MCP（${target_os}/${target_arch}）..."
  if curl -fsSL "${SERVER}/runners/agentflow-gate-mcp?os=${target_os}&arch=${target_arch}" -o "$target"; then
    chmod +x "$target"
    return
  fi
  local src="${SOURCE_DIR:-$PWD}"
  [[ -d "$src/cmd/agentflow-gate-mcp" ]] || fail "平台未提供 Gate MCP ${target_os}/${target_arch} 二进制，且未找到本地源码"
  command -v go >/dev/null 2>&1 || fail "本机未安装 Go，无法构建 Gate MCP；请让平台配置 AGENTFLOW_GATE_MCP_BINARY"
  info "从源码 ${src} 构建 Gate MCP（${target_os}/${target_arch}）..."
  (cd "$src" && CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -o "$target" ./cmd/agentflow-gate-mcp) \
    || fail "Gate MCP 源码构建失败"
  chmod +x "$target"
}

# Host sandbox uses the native helper. Docker always uses Linux; on a macOS
# runner both artifacts are therefore required.
install_gate_binary "$OS" "$ARCH"
if [[ "$OS" != "linux" ]]; then
  install_gate_binary linux "$ARCH"
fi

if [[ "$UPGRADE" == "1" ]]; then
  if [[ "$OS" == "darwin" && -f "$PLIST" ]]; then
    launchctl unload "$PLIST"
    launchctl load "$PLIST"
    info "已原地升级并重启 launchd 服务；机器绑定和工作区保持不变"
  elif [[ "$OS" == "linux" && -f "$UNIT" ]]; then
    systemctl --user restart agentflow-runner.service
    info "已原地升级并重启 systemd 服务；机器绑定和工作区保持不变"
  else
    info "已原地升级 runner；请重启前台运行的旧进程以启用新版"
  fi
  exit 0
fi

# ---------- 2. 写入配置 ----------
mkdir -p "$RUNNER_HOME" "$RUNNER_HOME/workspace"
chmod 700 "$RUNNER_HOME"
cat > "$ENV_FILE" <<EOF
AGENTFLOW_SERVER_URL=${SERVER}
AGENTFLOW_RUNNER_BOOTSTRAP_TOKEN=${TOKEN}
AGENTFLOW_RUNNER_NAME=${NAME:-$(hostname)}
AGENTFLOW_RUNNER_CONCURRENCY=${CONCURRENCY}
AGENTFLOW_GATE_MCP_BINARY=${GATE_BINARY_PATTERN}
EOF
chmod 600 "$ENV_FILE"
info "配置已写入 ${ENV_FILE}（权限 600）"
export AGENTFLOW_GATE_MCP_BINARY="$GATE_BINARY_PATTERN"

# ---------- 3. 注册 / 启动 ----------
RUN_ARGS=(-server "$SERVER" -token "$TOKEN" -name "${NAME:-$(hostname)}" -concurrency "$CONCURRENCY" -state "$STATE_FILE")
if [[ "$AUTO_UPDATE" == "0" ]]; then
  RUN_ARGS+=(-auto-update=false)
fi

# 用户当前 PATH：launchd/systemd 的服务环境只有 /usr/bin:/bin，不含
# ~/.local/bin、Homebrew、nvm 等 CLI 安装目录，必须显式带入。
USER_PATH="${PATH}"

if [[ "$NO_START" == "1" ]]; then
  info "已完成安装与配置（--no-start，未启动）。手动启动："
  info "  $BINARY ${RUN_ARGS[*]}"
  info "常驻方式：追加 --service 重新执行本脚本；卸载：追加 --uninstall（--purge 删除全部配置）"
elif [[ "$INSTALL_SERVICE" == "1" ]]; then
  if [[ "$OS" == "darwin" ]]; then
    cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.agentflow.runner</string>
  <key>ProgramArguments</key><array>
    <string>${BINARY}</string>
EOF
    for arg in "${RUN_ARGS[@]}"; do printf '    <string>%s</string>\n' "$arg" >> "$PLIST"; done
    # plist 值需要 XML 转义（& 等）
    ESCAPED_PATH=$(printf '%s' "$USER_PATH" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g')
    ESCAPED_GATE_PATTERN=$(printf '%s' "$GATE_BINARY_PATTERN" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g')
    cat >> "$PLIST" <<EOF
  </array>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>${ESCAPED_PATH}</string>
    <key>AGENTFLOW_GATE_MCP_BINARY</key><string>${ESCAPED_GATE_PATTERN}</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>${RUNNER_HOME}/runner.log</string>
  <key>StandardErrorPath</key><string>${RUNNER_HOME}/runner.log</string>
</dict></plist>
EOF
    launchctl unload "$PLIST" >/dev/null 2>&1 || true
    launchctl load "$PLIST"
    info "launchd 服务已安装并启动（日志: ${RUNNER_HOME}/runner.log）"
  else
    mkdir -p "$(dirname "$UNIT")"
    {
      echo "[Unit]"
      echo "Description=AgentFlow CLI Runner"
      echo "After=network-online.target"
      echo ""
      echo "[Service]"
      echo "Environment=PATH=${USER_PATH}"
      echo "Environment=AGENTFLOW_GATE_MCP_BINARY=${GATE_BINARY_PATTERN}"
      printf 'ExecStart=%s' "$BINARY"
      for arg in "${RUN_ARGS[@]}"; do printf ' %s' "$arg"; done
      echo ""
      echo "Restart=always"
      echo "RestartSec=5"
      echo ""
      echo "[Install]"
      echo "WantedBy=default.target"
    } > "$UNIT"
    systemctl --user daemon-reload
    systemctl --user enable agentflow-runner.service
    systemctl --user restart agentflow-runner.service
    info "systemd 服务已安装并启动（journalctl --user -u agentflow-runner -f 查看日志）"
  fi
  info "卸载方式：curl -fsSL ${SERVER}/runners/install.sh | bash -s -- --uninstall"
else
  info "前台启动 agentflow-runner（Ctrl+C 停止；常驻请加 --service）..."
  exec "$BINARY" "${RUN_ARGS[@]}"
fi
