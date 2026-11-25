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
#   --service              安装常驻服务。macOS 用 launchd；Linux 有 systemd 时用用户服务，
#                          没有用户会话时会先拉起 systemd 用户实例。没有 systemd 时改为后台运行。
#   --no-start             只安装配置，不立即启动
#   --no-auto-update       禁止从平台自动更新（自定义二进制默认禁用）
#   --upgrade              原地升级已绑定的 runner，保留配置、机器绑定和工作区
#   --uninstall            卸载：通知平台注销机器、停止服务、删除全部文件和配置
#
# 默认直接从平台下载对应 OS/架构的二进制（由服务端 /runners/agentflow-runner 分发）。
# 脚本安装 agentflow-runner 和跨沙箱 Git/MCP、Gate MCP helper、写配置并启动
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
RUNNER_BUILD_SOURCE=""
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
warn() { echo "[bind-cli] 警告: $*" >&2; }

# systemctl --user 走用户 D-Bus。SSH 进 CVM、以 root 执行，或当前 shell
# 没有登录会话时，这条总线还不存在，会报 Failed to connect to bus。
# 系统总线这时通常仍可用：用它启用 linger 并启动 user@<uid>，再把
# XDG_RUNTIME_DIR 指到 /run/user/<uid>，用户总线就会出现。
prepare_systemd_user_env() {
  local uid runtime
  uid="$(id -u)"
  runtime="/run/user/${uid}"
  if [[ -z "${XDG_RUNTIME_DIR:-}" && -d "${runtime}" ]]; then
    export XDG_RUNTIME_DIR="${runtime}"
  fi
  if [[ -n "${XDG_RUNTIME_DIR:-}" && -z "${DBUS_SESSION_BUS_ADDRESS:-}" && -S "${XDG_RUNTIME_DIR}/bus" ]]; then
    export DBUS_SESSION_BUS_ADDRESS="unix:path=${XDG_RUNTIME_DIR}/bus"
  fi
}

systemd_user_reachable() {
  systemctl --user show-environment >/dev/null 2>&1
}

# 先以当前用户执行。失败后再试免密 sudo：安装命令常通过 curl|bash 进入，
# 没有终端可以输入密码。
run_privileged() {
  if [[ "$(id -u)" -eq 0 ]]; then
    SYSTEMD_ASK_PASSWORD=0 "$@"
    return
  fi
  if SYSTEMD_ASK_PASSWORD=0 "$@" >/dev/null 2>&1; then
    return 0
  fi
  if command -v sudo >/dev/null 2>&1; then
    sudo -n "$@"
    return
  fi
  return 1
}

# 参数 quiet：连上总线，但不打印说明。返回 0 表示 systemctl --user 可用。
ensure_systemd_user_bus() {
  local announce="${1:-announce}" uid runtime user_name linger_ok attempt
  uid="$(id -u)"
  user_name="$(id -un)"
  runtime="/run/user/${uid}"
  linger_ok=0
  command -v systemctl >/dev/null 2>&1 || return 1
  SYSTEMD_ASK_PASSWORD=0 systemctl show --property=Version >/dev/null 2>&1 || return 1
  prepare_systemd_user_env
  if systemd_user_reachable; then
    return 0
  fi
  if [[ "${announce}" == announce ]]; then
    info "当前没有 systemd 用户总线，正在为 ${user_name} 启动用户实例..."
  fi
  if command -v loginctl >/dev/null 2>&1 && run_privileged loginctl enable-linger "${user_name}" >/dev/null 2>&1; then
    linger_ok=1
  fi
  run_privileged systemctl start "user@${uid}.service" >/dev/null 2>&1 || true
  if [[ -z "${XDG_RUNTIME_DIR:-}" || -S "${runtime}/bus" ]]; then
    export XDG_RUNTIME_DIR="${runtime}"
  fi
  attempt=0
  while [[ "${attempt}" -lt 10 ]]; do
    attempt=$((attempt + 1))
    if [[ -S "${runtime}/bus" && -z "${DBUS_SESSION_BUS_ADDRESS:-}" ]]; then
      export DBUS_SESSION_BUS_ADDRESS="unix:path=${runtime}/bus"
    fi
    if systemd_user_reachable; then
      if [[ "${announce}" == announce ]]; then
        if [[ "${linger_ok}" -eq 1 ]]; then
          info "已接通 systemd 用户总线；退出登录后服务仍会保持运行"
        else
          info "已接通 systemd 用户总线"
        fi
      fi
      return 0
    fi
    sleep 0.2
  done
  return 1
}

require_systemd_user_bus() {
  if ensure_systemd_user_bus; then
    return 0
  fi
  if ! command -v systemctl >/dev/null 2>&1 || ! SYSTEMD_ASK_PASSWORD=0 systemctl show --property=Version >/dev/null 2>&1; then
    fail "当前系统没有可用的 systemd，不能安装常驻服务。去掉 --service 可以在当前终端前台运行"
  fi
  if [[ "$(id -u)" -eq 0 ]]; then
    fail "无法启动 systemd 用户实例。请确认 systemd-logind 正在运行后重新安装"
  fi
  fail "无法连接 systemd 用户总线。请在终端执行：sudo loginctl enable-linger $(id -un)，然后再运行安装命令"
}

# 容器和云开发机里经常装着 systemctl，但 1 号进程不是 systemd。
linux_has_systemd() {
  command -v systemctl >/dev/null 2>&1 || return 1
  SYSTEMD_ASK_PASSWORD=0 systemctl show --property=Version >/dev/null 2>&1
}

shell_quote() { printf '%q' "$1"; }

# 判断 pid 是不是正在执行 run-service.sh 的进程：argv[0] 就是这个脚本，或 argv[0]
# 是 bash/sh、argv[1] 是这个脚本。PID 文件可能是重启前留下的旧值，那个 PID 已被
# 别的进程复用；查看或编辑这个脚本的进程（less、vim …）命令行里也带着路径，都不能算。
is_background_runner_pid() {
  local pid="$1"
  local -a args=()
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  if [[ -r "/proc/${pid}/cmdline" ]]; then
    mapfile -d '' -t args < "/proc/${pid}/cmdline" 2>/dev/null || return 1
  else
    read -r -a args <<< "$(ps -o args= -p "$pid" 2>/dev/null || true)" || true
  fi
  [[ "${#args[@]}" -gt 0 ]] || return 1
  [[ "${args[0]}" == "$BACKGROUND_SCRIPT" ]] && return 0
  case "${args[0]##*/}" in
    bash|sh) [[ "${args[1]:-}" == "$BACKGROUND_SCRIPT" ]] ;;
    *) return 1 ;;
  esac
}

# 只给核对过命令行的 run-service.sh 进程发信号。PID 文件里的旧 PID 不匹配时只删文件；
# PID 文件丢失或过期时，再按 /proc 里的命令行找仍在运行的 run-service.sh。
stop_background_runner() {
  local pid="" proc
  local -a pids=()
  if [[ -f "$BACKGROUND_PID" ]]; then
    pid="$(tr -d '[:space:]' < "$BACKGROUND_PID" 2>/dev/null || true)"
    rm -f "$BACKGROUND_PID"
  fi
  if [[ -n "$pid" ]] && is_background_runner_pid "$pid"; then
    pids+=("$pid")
  fi
  if [[ -n "$BACKGROUND_SCRIPT" && -d /proc ]]; then
    for proc in /proc/[0-9]*; do
      proc="${proc#/proc/}"
      [[ "$proc" == "$pid" || "$proc" == "$$" ]] && continue
      is_background_runner_pid "$proc" && pids+=("$proc")
    done
  fi
  [[ "${#pids[@]}" -gt 0 ]] || return 0
  for proc in "${pids[@]}"; do
    kill -TERM "$proc" >/dev/null 2>&1 || true
  done
  sleep 0.2
  for proc in "${pids[@]}"; do
    pkill -P "$proc" >/dev/null 2>&1 || true
  done
}

# 读当前用户的 crontab 到 CRONTAB_CURRENT。读取成功，或明确是「还没有 crontab」时
# 返回 0；其他失败（权限、spool 读不了等）返回 1 并把原因放进 CRONTAB_ERROR。
# 调用方必须在失败时放弃改写：把读失败当成空表写回，会覆盖用户原有的定时任务。
read_user_crontab() {
  local err_file status=0
  CRONTAB_CURRENT=""
  CRONTAB_ERROR=""
  err_file="$(mktemp "${TMPDIR:-/tmp}/bind-cli-crontab.XXXXXX")" || {
    CRONTAB_ERROR="无法创建临时文件"
    return 1
  }
  CRONTAB_CURRENT="$(crontab -l 2>"$err_file")" || status=$?
  CRONTAB_ERROR="$(head -n 1 "$err_file" 2>/dev/null || true)"
  rm -f "$err_file"
  [[ "$status" -eq 0 ]] && return 0
  CRONTAB_CURRENT=""
  if [[ "$CRONTAB_ERROR" == *"no crontab for"* ]]; then
    CRONTAB_ERROR=""
    return 0
  fi
  [[ -n "$CRONTAB_ERROR" ]] || CRONTAB_ERROR="crontab -l 退出码 ${status}"
  return 1
}

remove_background_reboot_hook() {
  command -v crontab >/dev/null 2>&1 || return 0
  local filtered
  if ! read_user_crontab; then
    warn "读取 crontab 失败（${CRONTAB_ERROR}），未改动 crontab；如有 # agentflow-runner 开机项请手动删除"
    return 0
  fi
  [[ "$CRONTAB_CURRENT" == *"# agentflow-runner"* ]] || return 0
  filtered="$(printf '%s\n' "$CRONTAB_CURRENT" | grep -v '# agentflow-runner' || true)"
  printf '%s\n' "$filtered" | crontab - >/dev/null 2>&1 ||
    warn "写回 crontab 失败，请手动删除 # agentflow-runner 开机项"
}

install_background_reboot_hook() {
  command -v crontab >/dev/null 2>&1 || return 0
  [[ "$BACKGROUND_SCRIPT" != *" "* && "$RUNNER_HOME" != *" "* ]] || return 0
  local entry
  if ! read_user_crontab; then
    warn "读取 crontab 失败（${CRONTAB_ERROR}），为免覆盖已有定时任务，未安装开机自启；runner 本次仍在后台运行"
    return 0
  fi
  entry="@reboot ${BACKGROUND_SCRIPT} >> ${RUNNER_HOME}/runner.log 2>&1 # agentflow-runner"
  {
    if [[ -n "$CRONTAB_CURRENT" ]]; then
      printf '%s\n' "$CRONTAB_CURRENT" | grep -v '# agentflow-runner' || true
    fi
    printf '%s\n' "$entry"
  } | crontab - >/dev/null 2>&1 ||
    warn "写入 crontab 失败，未安装开机自启；runner 本次仍在后台运行"
}

launch_background_wrapper() {
  mkdir -p "$RUNNER_HOME"
  if command -v nohup >/dev/null 2>&1; then
    nohup "$BACKGROUND_SCRIPT" >> "${RUNNER_HOME}/runner.log" 2>&1 < /dev/null &
  else
    "$BACKGROUND_SCRIPT" >> "${RUNNER_HOME}/runner.log" 2>&1 < /dev/null &
  fi
  echo "$!" > "$BACKGROUND_PID"
  disown "$!" >/dev/null 2>&1 || true
}

# 没有 systemd 时仍把 runner 留在后台，避免安装停在用户看不懂的报错上。
start_background_runner() {
  local arg
  stop_background_runner
  {
    echo '#!/usr/bin/env bash'
    # @reboot 直接执行本脚本、不经过 launch_background_wrapper，由脚本自己写 PID 文件，
    # 重启后 PID 文件里也是当前进程。
    echo "echo \$\$ > $(shell_quote "$BACKGROUND_PID")"
    echo "trap '' HUP"
    echo 'child=0'
    echo 'trap '\''if [[ "$child" != 0 ]]; then kill "$child" 2>/dev/null || true; fi; exit 0'\'' TERM INT'
    echo "export PATH=$(shell_quote "$USER_PATH")"
    echo "export AGENTFLOW_GATE_MCP_BINARY=$(shell_quote "$GATE_BINARY_PATTERN")"
    echo 'while true; do'
    printf '  %s' "$(shell_quote "$BINARY")"
    for arg in "${RUN_ARGS[@]}"; do
      printf ' %s' "$(shell_quote "$arg")"
    done
    echo ' &'
    echo '  child=$!'
    echo '  wait "$child" || true'
    echo '  sleep 5'
    echo 'done'
  } > "$BACKGROUND_SCRIPT"
  chmod 700 "$BACKGROUND_SCRIPT"
  launch_background_wrapper
  install_background_reboot_hook
  info "安装完成，已在后台运行。日志: ${RUNNER_HOME}/runner.log"
}

restart_background_runner() {
  [[ -x "$BACKGROUND_SCRIPT" ]] || fail "未找到后台启动脚本，无法原地升级"
  stop_background_runner
  launch_background_wrapper
  install_background_reboot_hook
  info "已原地升级并重启后台进程；机器绑定和工作区保持不变"
}

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  arm64|aarch64) ARCH="arm64" ;;
  x86_64|amd64) ARCH="amd64" ;;
  *) fail "不支持的架构: $ARCH" ;;
esac
[[ "$OS" == "darwin" || "$OS" == "linux" ]] || fail "不支持的系统: ${OS}（Windows 请使用 PowerShell 安装方式）"

# Use the Mach-O slices instead of uname. A universal Runner installed under
# Rosetta can later be launched natively by launchd. List the native slice first.
macos_runner_arches() {
  local header="" entry_size little=0 arches
  if [[ -f "$1" ]]; then
    header="$(LC_ALL=C od -An -v -t x1 -N 8 "$1" | tr -d '[:space:]')"
  fi
  case "$header" in
    cffaedfe07000001) printf '%s\n' amd64; return ;;
    cffaedfe0c000001) printf '%s\n' arm64; return ;;
    cafebabe*) entry_size=20 ;;
    cafebabf*) entry_size=32 ;;
    bebafeca*) entry_size=20; little=1 ;;
    bfbafeca*) entry_size=32; little=1 ;;
    *) printf '%s\n' "$ARCH"; return ;;
  esac
  # A fat header is eight bytes. Bound the read to the architecture table
  # and accept only the CPU types for which we distribute Linux companions.
  arches=$(LC_ALL=C od -An -v -t u1 -N 4104 "$1" |
    awk -v stride="$entry_size" -v little="$little" '
      { for (i = 1; i <= NF; i++) bytes[count++] = $i }
      function word(offset,   i, value) {
        for (i = 0; i < 4; i++) value = 256 * value + bytes[offset + (little ? 3-i : i)]
        return value
      }
      END {
        slices = word(4)
        if (slices < 1 || 8 + slices * stride > count) exit 1
        for (slice = 0; slice < slices; slice++) {
          cpu = word(8 + slice * stride)
          if (cpu == 16777223) amd64 = 1
          if (cpu == 16777228) arm64 = 1
        }
        if (amd64 && arm64) print "arm64 amd64"
        else if (amd64) print "amd64"
        else if (arm64) print "arm64"
      }') || arches=""
  # uname reports x86_64 under Rosetta too. Intel Macs can use only amd64.
  if [[ "$arches" == "arm64 amd64" && "$ARCH" == "amd64" &&
        "$(PATH="$PATH:/usr/sbin" sysctl -n hw.optional.arm64 2>/dev/null || true)" != "1" ]]; then
    arches="amd64"
  fi
  printf '%s\n' "${arches:-$ARCH}"
}

RUNNER_HOME="${HOME}/.agentflow-runner"
ENV_FILE="${RUNNER_HOME}/env"
STATE_FILE="${RUNNER_HOME}/state.json"
BINARY="${BIN_DIR}/agentflow-runner"
GATE_BINARY_PATTERN="${BIN_DIR}/agentflow-gate-mcp-{os}-{arch}"
PLIST="${HOME}/Library/LaunchAgents/com.agentflow.runner.plist"
UNIT="${HOME}/.config/systemd/user/agentflow-runner.service"
BACKGROUND_SCRIPT="${RUNNER_HOME}/run-service.sh"
BACKGROUND_PID="${RUNNER_HOME}/service.pid"

# ---------- 卸载 ----------
if [[ "$UNINSTALL" == "1" ]]; then
  info "停止并移除 agentflow-runner..."

  # 先停止 daemon，避免注销 API 返回后存活进程立即用 bootstrap token
  # 重新注册；state/env 暂时保留，供下面的注销请求使用。
  if [[ "$OS" == "darwin" ]]; then
    [[ -f "$PLIST" ]] && launchctl unload "$PLIST" >/dev/null 2>&1 || true
  else
    stop_background_runner
    ensure_systemd_user_bus quiet || true
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
    if [[ -f "$BACKGROUND_SCRIPT" || -f "$BACKGROUND_PID" ]]; then
      stop_background_runner
      remove_background_reboot_hook
      rm -f "$BACKGROUND_SCRIPT"
      info "已停止后台运行"
    fi
    if [[ -f "$UNIT" ]]; then
      ensure_systemd_user_bus quiet || true
      systemctl --user disable --now agentflow-runner.service >/dev/null 2>&1 || true
      rm -f "$UNIT"
      systemctl --user daemon-reload >/dev/null 2>&1 || true
      info "已移除 systemd 服务"
    fi
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
  if [[ "$OS" == "darwin" ]]; then
    read -r -a runner_arches <<< "$(macos_runner_arches "$BINARY")"
    for runner_arch in "${runner_arches[@]}"; do
      rm -f "${BIN_DIR}/agentflow-runner-linux-${runner_arch}"
    done
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

# ---------- 0.5 Agent 浏览器 ----------
# docker 沙箱自带浏览器 sidecar；本机执行（runner/local/seatbelt）需要宿主 Chromium。
find_chromium() {
  local name path
  for name in chromium chromium-browser google-chrome-stable google-chrome; do
    if path="$(command -v "$name" 2>/dev/null)"; then printf '%s\n' "$path"; return 0; fi
  done
  for path in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" "/Applications/Chromium.app/Contents/MacOS/Chromium" \
    "$HOME"/.cache/ms-playwright/chromium-*/chrome-linux*/chrome \
    "$HOME"/Library/Caches/ms-playwright/chromium-*/chrome-mac*/"Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing"; do
    if [[ -x "$path" ]]; then printf '%s\n' "$path"; return 0; fi
  done
  return 1
}
case "$(printf '%s' "${AGENTFLOW_BROWSER:-}" | tr '[:upper:]' '[:lower:]')" in
  off|0|false|no)
    info "AGENTFLOW_BROWSER=${AGENTFLOW_BROWSER}，跳过 Agent 浏览器检测"
    ;;
  *)
    info "检测 Agent 浏览器..."
    if CHROMIUM_PATH="$(find_chromium)"; then
      info "  发现 Chromium: ${CHROMIUM_PATH}"
    elif command -v npx >/dev/null 2>&1; then
      info "  未找到 Chromium，安装 Playwright Chromium 到用户缓存（无需 sudo）..."
      # A stalled download must not hang the whole bind; it only costs browser tools.
      install_limit=""
      if command -v timeout >/dev/null 2>&1; then install_limit="timeout 600"; fi
      PLAYWRIGHT_DOWNLOAD_HOST="${PLAYWRIGHT_DOWNLOAD_HOST:-https://npmmirror.com/mirrors/playwright}" $install_limit npx -y playwright install chromium >/dev/null 2>&1 || true
      if CHROMIUM_PATH="$(find_chromium)"; then
        info "  已安装 Chromium: ${CHROMIUM_PATH}"
      else
        echo "[bind-cli] 提示: Chromium 安装失败，本机执行的任务将没有浏览器工具（docker 沙箱不受影响）" >&2
      fi
    else
      echo "[bind-cli] 提示: 未找到 Chromium 且无 npx，本机执行的任务将没有浏览器工具；安装 Chrome 后重新执行本脚本即可" >&2
    fi
    ;;
esac

# ---------- 1. 安装二进制 ----------
download_runner_binary() {
  local target_os="$1" url
  if [[ -n "$DOWNLOAD_URL" && ( "$target_os" == "$OS" || "$DOWNLOAD_URL" == *"{os}"* ) ]]; then
    url="${DOWNLOAD_URL//\{os\}/$target_os}"
    url="${url//\{arch\}/$ARCH}"
  else
    url="${SERVER}/runners/agentflow-runner?os=${target_os}&arch=${ARCH}"
  fi
  info "从 ${url} 下载 agentflow-runner（${target_os}/${ARCH}）..."
  curl -fsSL "$url" -o "$TEMP_BINARY" && [[ -s "$TEMP_BINARY" ]]
}

build_runner_binary() {
  local target_os="$1" source="$2" command="${3:-agentflow-runner}" output="$TEMP_BINARY"
  [[ "$output" == /* ]] || output="$PWD/$output"
  info "从源码 ${source} 构建 ${command}（${target_os}/${ARCH}）..."
  (cd "$source" && CGO_ENABLED=0 GOOS="$target_os" GOARCH="$ARCH" go build -o "$output" "./cmd/$command")
}

publish_runner_binary() {
  [[ -s "$TEMP_BINARY" ]] || fail "下载或构建的 runner 二进制为空"
  chmod +x "$TEMP_BINARY"
  mv -f "$TEMP_BINARY" "$1"
  TEMP_BINARY=""
}

stage_runner_binary() {
  mkdir -p "$BIN_DIR"
  TEMP_BINARY=$(mktemp "${BIN_DIR}/.agentflow-runner.XXXXXX")
  if [[ -n "$LOCAL_BINARY" ]]; then
    [[ -f "$LOCAL_BINARY" ]] || fail "本地二进制不存在: $LOCAL_BINARY"
    cp "$LOCAL_BINARY" "$TEMP_BINARY"
  elif [[ -n "$DOWNLOAD_URL" ]]; then
    download_runner_binary "$OS"
  else
    # 默认：平台直接分发对应 OS/架构的二进制，无需本机 Go 工具链。
    if download_runner_binary "$OS"; then
      :
    else
      # 平台未配置分发：回退到本地源码构建（--source 或当前目录为源码 checkout）。
      AUTO_UPDATE=0
      RUNNER_BUILD_SOURCE="${SOURCE_DIR:-$PWD}"
      if [[ ! -d "$RUNNER_BUILD_SOURCE/cmd/agentflow-runner" ]]; then
        fail "平台未提供二进制且未找到源码。解决方式任选其一：
  1) 平台管理员设置 AGENTFLOW_SOURCE_ROOT（源码目录）或 AGENTFLOW_RUNNER_BINARY（预编译产物）后重启服务
  2) 本命令追加 --source <AgentFlow 源码目录>（需要本机安装 Go）
  3) 本命令追加 --from <已编译的 agentflow-runner 二进制路径>"
      fi
      command -v go >/dev/null 2>&1 || fail "本机未安装 Go，无法从源码构建；请使用 --from 提供二进制"
      build_runner_binary "$OS" "$RUNNER_BUILD_SOURCE" \
        || fail "源码构建失败；请检查 Go 版本（需 1.22+）或改用 --from"
    fi
  fi
  [[ -s "$TEMP_BINARY" ]] || fail "下载或构建的 runner 二进制为空"
}

is_linux_runner_binary() {
  local binary="$1" machine size layout offset table_size
  case "${2:-$ARCH}" in
    amd64) machine=62 ;;
    arm64) machine=183 ;;
    *) return 1 ;;
  esac
  size=$(wc -c < "$binary") || return 1
  local decode_bytes='
    { for (i = 1; i <= NF; i++) bytes[count++] = $i }
    function little_endian(offset, width,   i, value) {
      for (i = width - 1; i >= 0; i--) value = 256 * value + bytes[offset + i]
      return value
    }'
  # ELF64 starts with 64 bytes, followed by a table of 56-byte program headers.
  layout=$(LC_ALL=C od -An -v -t u1 -N 64 "$binary" |
    awk -v machine="$machine" -v size="$size" "$decode_bytes"'
      END {
        if (count != 64 || bytes[0] != 127 || bytes[1] != 69 ||
            bytes[2] != 76 || bytes[3] != 70 || bytes[4] != 2 ||
            bytes[5] != 1 || bytes[6] != 1 || (bytes[7] != 0 && bytes[7] != 3)) exit 1
        type = little_endian(16, 2)
        offset = little_endian(32, 8)
        num = little_endian(56, 2)
        if ((type != 2 && type != 3) || little_endian(18, 2) != machine ||
            little_endian(54, 2) != 56 || num == 0 || num * 56 > 65536 ||
            offset < 64 || num * 56 > size - offset) exit 1
        printf "%.0f %d\n", offset, num * 56
      }') || return 1
  read -r offset table_size <<< "$layout"
  # The header can be valid even when the server only has part of the file.
  LC_ALL=C od -An -v -t u1 -j "$offset" -N "$table_size" "$binary" |
    awk -v size="$size" -v table_size="$table_size" "$decode_bytes"'
      END {
        if (count != table_size) exit 1
        for (header = 0; header < count; header += 56) {
          type = little_endian(header, 4)
          flags = little_endian(header + 4, 4)
          offset = little_endian(header + 8, 8)
          file_size = little_endian(header + 32, 8)
          if (type != 0 && file_size > 0 && (offset > size || file_size > size - offset)) exit 1
          if (type != 1) continue
          if (file_size > little_endian(header + 40, 8)) exit 1
          if (file_size > 0 && flags % 2 == 1) executable = 1
        }
        if (!executable) exit 1
      }'
}

is_usable_docker_runner_helper() {
  # The runtime copies the source into the sandbox with executable mode.
  [[ -r "$1" && -f "$1" ]] || return 1
  # A custom installer may also provide an executable script wrapper.
  [[ "$(LC_ALL=C od -An -v -t x1 -N 2 "$1" | tr -d '[:space:]')" == 2321 ]] ||
    is_linux_runner_binary "$1" "${2:-$ARCH}"
}

stage_docker_runner_binary() {
  local source="${SOURCE_DIR:-$PWD}"
  if [[ -n "$RUNNER_BUILD_SOURCE" ]]; then
    build_runner_binary linux "$RUNNER_BUILD_SOURCE" && is_linux_runner_binary "$TEMP_BINARY"
    return
  fi
  if download_runner_binary linux && is_linux_runner_binary "$TEMP_BINARY"; then
    return 0
  fi
  [[ -d "$source/cmd/agentflow-runner" ]] && command -v go >/dev/null 2>&1 &&
    build_runner_binary linux "$source" && is_linux_runner_binary "$TEMP_BINARY"
}

install_docker_runner_binary() {
  # A custom host runner may have a matching helper managed by its installer.
  # A per-OS download URL can replace the helper when it is the selected source.
  if [[ -n "$LOCAL_BINARY" || -n "$RUNNER_BUILD_SOURCE" ||
        ( "$DOWNLOAD_URL" != *"{os}"* && ( -n "$DOWNLOAD_URL" || -n "$SOURCE_DIR" ) ) ]] &&
      is_usable_docker_runner_helper "$DOCKER_RUNNER_BINARY"; then
    info "保留自定义 Runner 的 Linux Git/MCP helper：${DOCKER_RUNNER_BINARY}"
    return
  fi
  local helper_dir
  helper_dir=$(dirname "$DOCKER_RUNNER_BINARY")
  if [[ -d "$DOCKER_RUNNER_BINARY" || "$DOCKER_RUNNER_BINARY" -ef "$BINARY" ]] ||
     ! mkdir -p "$helper_dir" || ! TEMP_BINARY=$(mktemp "${helper_dir}/.agentflow-runner.XXXXXX"); then
    warn "无法准备 Linux Git/MCP helper 的安装位置：${DOCKER_RUNNER_BINARY}"
    return
  fi
  if stage_docker_runner_binary; then
    publish_runner_binary "$DOCKER_RUNNER_BINARY"
    return
  fi
  rm -f "$TEMP_BINARY"
  TEMP_BINARY=""
  warn "无法获取有效的 Linux/${ARCH} Git/MCP helper，Docker 工作区需要该文件：${DOCKER_RUNNER_BINARY}"
}

TEMP_BINARY=""
STAGED_RUNNER=""
trap '[[ -z "$TEMP_BINARY" ]] || rm -f "$TEMP_BINARY"; [[ -z "$STAGED_RUNNER" ]] || rm -f "$STAGED_RUNNER"' EXIT
stage_runner_binary
STAGED_RUNNER="$TEMP_BINARY"
TEMP_BINARY=""

saved_runner_binary_pattern() {
  awk 'sub(/^AGENTFLOW_RUNNER_BINARY=/, "") { print; exit }' "$ENV_FILE"
}

normalize_runner_binary_pattern() {
  local pattern="$1" working_dir="$2"
  # Keep the template expansion in sync with runtime.DockerRunnerHelperPath.
  pattern="${pattern#"${pattern%%[![:space:]]*}"}"
  pattern="${pattern%"${pattern##*[![:space:]]}"}"
  if [[ "$pattern" == *"{os}"* && "$pattern" != /* ]]; then
    pattern="${working_dir%/}/$pattern"
  fi
  printf '%s\n' "$pattern"
}

installed_macos_runner_binary_pattern() {
  local pattern working_dir
  if [[ -f "$PLIST" ]] &&
     pattern=$(/usr/bin/plutil -extract EnvironmentVariables.AGENTFLOW_RUNNER_BINARY raw "$PLIST" 2>/dev/null); then
    working_dir=$(/usr/bin/plutil -extract WorkingDirectory raw "$PLIST" 2>/dev/null) || working_dir="/"
    normalize_runner_binary_pattern "$pattern" "${working_dir:-/}"
  else
    pattern=$(saved_runner_binary_pattern)
    normalize_runner_binary_pattern "$pattern" "$PWD"
  fi
}

configure_macos_docker_runner_helper() {
  local pattern
  pattern=$(normalize_runner_binary_pattern "${AGENTFLOW_RUNNER_BINARY-$1}" "$PWD")
  if [[ "${#runner_arches[@]}" -gt 1 && "$pattern" == *"{os}"* && "$pattern" != *"{arch}"* ]]; then
    fail "通用 macOS Runner 需要为每种架构安装 Linux Git/MCP helper，请在 AGENTFLOW_RUNNER_BINARY 中添加 {arch}"
  fi
  export AGENTFLOW_RUNNER_BINARY="$pattern"
}

macos_docker_runner_helper_path() {
  local target_arch="$1" pattern="$2" target="${BIN_DIR}/agentflow-runner-linux-${1}"
  if [[ "$pattern" == *"{os}"* ]]; then
    target="${pattern//\{os\}/linux}"
    target="${target//\{arch\}/$target_arch}"
  fi
  printf '%s\n' "$target"
}

canonical_helper_target() {
  local target="$1" dir
  dir=$(dirname "$target")
  (mkdir -p "$dir" && cd -P "$dir" && printf '%s/%s\n' "$PWD" "${target##*/}")
}

check_macos_helper_paths() {
  local host_target target resolved runner_arch previous_target="" previous_resolved=""
  host_target=$(canonical_helper_target "$BINARY")
  for runner_arch in "${runner_arches[@]}"; do
    target=$(macos_docker_runner_helper_path "$runner_arch" "$AGENTFLOW_RUNNER_BINARY")
    # Compare physical parents even on fresh installs, where -ef cannot
    # compare inodes yet. Unavailable helper destinations are warned about
    # by the installer when it tries to stage the companion.
    resolved=$(canonical_helper_target "$target" 2>/dev/null) || resolved=""
    if [[ "$target" -ef "$BINARY" ]] || [[ -n "$resolved" && "$resolved" == "$host_target" ]]; then
      fail "Linux Git/MCP helper 路径与 macOS Runner 相同：${target}"
    fi
    if [[ -n "$previous_target" && "$target" -ef "$previous_target" ]] ||
       [[ -n "$resolved" && "$resolved" == "$previous_resolved" ]]; then
      fail "两种架构的 Linux Git/MCP helper 路径相同，请使用独立的架构目录"
    fi
    previous_target="$target"
    previous_resolved="$resolved"
  done
}

save_runner_binary_pattern() {
  [[ "$(saved_runner_binary_pattern)" != "$AGENTFLOW_RUNNER_BINARY" ]] || return 0
  local tmp
  tmp=$(mktemp "${RUNNER_HOME}/.env.XXXXXX")
  if ! { sed '/^AGENTFLOW_RUNNER_BINARY=/d' "$ENV_FILE" &&
         printf 'AGENTFLOW_RUNNER_BINARY=%s\n' "$AGENTFLOW_RUNNER_BINARY"; } > "$tmp" ||
     ! mv -f "$tmp" "$ENV_FILE"; then
    rm -f "$tmp"
    fail "无法保存 Git/MCP helper 路径配置"
  fi
}

if [[ "$OS" == "darwin" ]]; then
  read -r -a runner_arches <<< "$(macos_runner_arches "$STAGED_RUNNER")"
  required_arch="${runner_arches[0]}"
  previous_pattern=""
  require_replacement_helper=0
  if [[ "$UPGRADE" == "1" ]]; then
    previous_pattern=$(installed_macos_runner_binary_pattern)
    read -r -a previous_arches <<< "$(macos_runner_arches "$BINARY")"
    # The old universal binary may be running its Intel slice under Rosetta.
    for previous_arch in "${previous_arches[@]}"; do
      previous_helper=$(macos_docker_runner_helper_path "$previous_arch" "$previous_pattern")
      if [[ ! "$previous_helper" -ef "$BINARY" ]] &&
         is_usable_docker_runner_helper "$previous_helper" "$previous_arch"; then
        require_replacement_helper=1
        break
      fi
    done
  fi
  configure_macos_docker_runner_helper "$previous_pattern"
  check_macos_helper_paths
fi

install_gate_binary() {
  local target_os="$1" target_arch="$ARCH" optional="${2:-0}" target
  local src="${SOURCE_DIR:-$PWD}" error
  target="${GATE_BINARY_PATTERN//\{os\}/$target_os}"
  target="${target//\{arch\}/$target_arch}"
  info "从 ${SERVER} 下载 Gate MCP（${target_os}/${target_arch}）..."
  TEMP_BINARY=$(mktemp "${BIN_DIR}/.agentflow-gate-mcp.XXXXXX")
  if curl -fsSL "${SERVER}/runners/agentflow-gate-mcp?os=${target_os}&arch=${target_arch}" -o "$TEMP_BINARY" &&
     [[ -s "$TEMP_BINARY" ]]; then
    publish_runner_binary "$target"
    return
  fi
  if [[ ! -d "$src/cmd/agentflow-gate-mcp" ]]; then
    error="平台未提供 Gate MCP ${target_os}/${target_arch} 二进制，且未找到本地源码"
  elif ! command -v go >/dev/null 2>&1; then
    error="本机未安装 Go，无法构建 Gate MCP；请让平台配置 AGENTFLOW_GATE_MCP_BINARY"
  elif build_runner_binary "$target_os" "$src" agentflow-gate-mcp && [[ -s "$TEMP_BINARY" ]]; then
    publish_runner_binary "$target"
    return
  else
    error="Gate MCP ${target_os}/${target_arch} 源码构建失败"
  fi
  rm -f "$TEMP_BINARY"
  TEMP_BINARY=""
  if [[ "$optional" == "1" ]]; then
    warn "$error"
  else
    fail "$error"
  fi
}

# Install the primary slice's companions before touching the optional slice.
if [[ "$OS" == "darwin" ]]; then
  for ARCH in "${runner_arches[@]}"; do
    DOCKER_RUNNER_BINARY=$(macos_docker_runner_helper_path "$ARCH" "$AGENTFLOW_RUNNER_BINARY")
    optional_gate=0
    [[ "$ARCH" == "$required_arch" ]] || optional_gate=1
    install_gate_binary darwin "$optional_gate"
    install_gate_binary linux "$optional_gate"
    install_docker_runner_binary
    if [[ "$ARCH" == "$required_arch" && "$require_replacement_helper" == "1" ]] &&
       ! is_usable_docker_runner_helper "$DOCKER_RUNNER_BINARY"; then
      fail "缺少升级所需的 Linux/${ARCH} Git/MCP helper（${DOCKER_RUNNER_BINARY}），已保留当前 Runner 和服务"
    fi
  done
else
  install_gate_binary "$OS"
fi

TEMP_BINARY="$STAGED_RUNNER"
STAGED_RUNNER=""
publish_runner_binary "$BINARY"

if [[ "$UPGRADE" == "1" ]]; then
  if [[ "$OS" == "darwin" ]]; then
    save_runner_binary_pattern
  fi
  if [[ "$OS" == "darwin" && -f "$PLIST" ]]; then
    if [[ "$AUTO_UPDATE" == "0" ]]; then
      arg_count=$(/usr/bin/plutil -extract ProgramArguments raw -expect array "$PLIST")
      if [[ "$arg_count" -eq 0 ]] ||
         [[ "$(/usr/bin/plutil -extract "ProgramArguments.$((arg_count - 1))" raw "$PLIST")" != "-auto-update=false" ]]; then
        /usr/bin/plutil -insert ProgramArguments -string -auto-update=false -append "$PLIST"
      fi
    fi
    if [[ "$AGENTFLOW_RUNNER_BINARY" == *"{os}"* ]]; then
      if ! /usr/bin/plutil -type EnvironmentVariables "$PLIST" >/dev/null 2>&1; then
        /usr/bin/plutil -insert EnvironmentVariables -dictionary "$PLIST"
      fi
      /usr/bin/plutil -replace EnvironmentVariables.AGENTFLOW_RUNNER_BINARY -string "$AGENTFLOW_RUNNER_BINARY" "$PLIST"
    elif /usr/bin/plutil -type EnvironmentVariables.AGENTFLOW_RUNNER_BINARY "$PLIST" >/dev/null 2>&1; then
      /usr/bin/plutil -remove EnvironmentVariables.AGENTFLOW_RUNNER_BINARY "$PLIST"
    fi
    launchctl unload "$PLIST"
    launchctl load "$PLIST"
    info "已原地升级并重启 launchd 服务；机器绑定和工作区保持不变"
  elif [[ "$OS" == "linux" && -f "$UNIT" ]] && linux_has_systemd; then
    require_systemd_user_bus
    systemctl --user restart agentflow-runner.service
    info "已原地升级并重启 systemd 服务；机器绑定和工作区保持不变"
  elif [[ "$OS" == "linux" && -x "$BACKGROUND_SCRIPT" ]]; then
    restart_background_runner
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
AGENTFLOW_RUNNER_BINARY=${AGENTFLOW_RUNNER_BINARY:-}
EOF
chmod 600 "$ENV_FILE"
info "配置已写入 ${ENV_FILE}（权限 600）"
export AGENTFLOW_GATE_MCP_BINARY="$GATE_BINARY_PATTERN"

# ---------- 3. 注册 / 启动 ----------
RUN_ARGS=(-server "$SERVER" -token "$TOKEN" -name "${NAME:-$(hostname)}" -concurrency "$CONCURRENCY" -state "$STATE_FILE")
if [[ "$AUTO_UPDATE" == "0" ]]; then
  RUN_ARGS+=(-auto-update=false)
fi
RUNNER_LAUNCH=("$BINARY")
if [[ "$OS" == "darwin" && "${#runner_arches[@]}" -gt 1 ]]; then
  # Use the required native slice for foreground and manual starts, even
  # when the shell runs under Rosetta and the optional amd64 files are absent.
  RUNNER_LAUNCH=(/usr/bin/arch -arm64 "$BINARY")
fi

# 用户当前 PATH：launchd/systemd 的服务环境只有 /usr/bin:/bin，不含
# ~/.local/bin、Homebrew、nvm 等 CLI 安装目录，必须显式带入。
USER_PATH="${PATH}"

if [[ "$NO_START" == "1" ]]; then
  info "已完成安装与配置（--no-start，未启动）。手动启动："
  runner_command=""
  if [[ "${AGENTFLOW_RUNNER_BINARY:-}" == *"{os}"* ]]; then
    runner_command="AGENTFLOW_RUNNER_BINARY=$(shell_quote "$AGENTFLOW_RUNNER_BINARY") "
  fi
  for arg in "${RUNNER_LAUNCH[@]}" "${RUN_ARGS[@]}"; do
    runner_command+="$(shell_quote "$arg") "
  done
  info "  ${runner_command% }"
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
    ESCAPED_RUNNER_PATTERN=$(printf '%s' "$AGENTFLOW_RUNNER_BINARY" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g')
    cat >> "$PLIST" <<EOF
  </array>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>${ESCAPED_PATH}</string>
    <key>AGENTFLOW_GATE_MCP_BINARY</key><string>${ESCAPED_GATE_PATTERN}</string>
    <key>AGENTFLOW_RUNNER_BINARY</key><string>${ESCAPED_RUNNER_PATTERN}</string>
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
  elif linux_has_systemd; then
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
    require_systemd_user_bus
    systemctl --user daemon-reload
    systemctl --user enable agentflow-runner.service
    systemctl --user restart agentflow-runner.service
    info "systemd 服务已安装并启动（journalctl --user -u agentflow-runner -f 查看日志）"
  else
    start_background_runner
  fi
  info "卸载方式：curl -fsSL ${SERVER}/runners/install.sh | bash -s -- --uninstall"
else
  info "前台启动 agentflow-runner（Ctrl+C 停止；常驻请加 --service）..."
  exec "${RUNNER_LAUNCH[@]}" "${RUN_ARGS[@]}"
fi
