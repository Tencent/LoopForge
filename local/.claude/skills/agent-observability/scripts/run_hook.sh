#!/bin/sh
set -eu

PROJECT_DIR="${CODEBUDDY_PROJECT_DIR:-$(cd "$(dirname "$0")"/../../../.. && pwd -P)}"
PYTHON_BIN="$PROJECT_DIR/.venv/bin/python"
MAIN_PY="$PROJECT_DIR/.codebuddy/skills/agent-observability/scripts/main.py"

# 只有当 .venv 里可用 zhiyanllm 时才优先使用该解释器；
# 否则回退到已安装 zhiyanllm 的系统 python3。
if [ -x "$PYTHON_BIN" ]; then
  if "$PYTHON_BIN" -c "import zhiyanllm" 2>/dev/null; then
    exec "$PYTHON_BIN" "$MAIN_PY" "$@"
  fi
fi

exec python3 "$MAIN_PY" "$@"
