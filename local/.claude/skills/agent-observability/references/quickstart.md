## Quickstart

### 安装

将 `templates/settings-hook.json` 合并到 `~/.claude/settings.json` 或 `<project>/.claude/settings.json`。

必须启用 hooks：`SessionStart`、`UserPromptSubmit`、`PreToolUse`、`PostToolUse`、`Stop`。

### 查看输出

完成一次正常会话后检查：
- `.claude/skills/agent-observability/logs/metrics.ndjson`
- `.claude/skills/agent-observability/logs/.state.json`

### 指定数据源路径（可选）

`build_dashboard_data.py` 默认从 `<skill_root>/logs/metrics.ndjson` 与 `<skill_root>/logs/.state.json` 读取，**不传参数时行为完全不变**。

如需从其它位置读取，可用 `--metrics-path` / `--state-path` 覆盖：

```bash
python3 scripts/build_dashboard_data.py \
  --metrics-path ~/Downloads/metrics.ndjson \
  --state-path  ~/Downloads/.state.json \
  --out dashboard/dashboard-data.json
```

注意：

- 两个参数都留空（或省略）时回退到 `<skill_root>/logs/` 下的默认文件。
- 路径支持 `~` 展开（如上例的 `~/Downloads/...`）。
- 输出 `dashboard-data.json` 的 `source.metrics_ndjson` / `source.state_json` 会**如实反映实际读取到的路径**，覆盖后自然指向你给定的文件，便于核对数据来源。

重点字段：
- `event=tool`：工具耗时、`skill`、`rule`
- `event=usage`：`tokens`、`model`、`cost_usd`
- `event=stop`：会话尾部 flush 与总成本

### 导出最慢的工具调用（可选）

`--top-slow N` 会在生成 `dashboard-data.json` 的同时，额外在**终端**打印耗时最长的 N 次 `event=tool` 调用（工具名 + 耗时 ms，按耗时降序）。排查"哪次工具调用拖慢了会话"时不用再去翻原始 ndjson。

```bash
python3 scripts/build_dashboard_data.py --top-slow 5
```

输出示例（排在 `wrote ...` 之后）：

```
top-slow 5 tool calls (by ms):
  1. Bash    4820ms
  2. Grep    1230ms
  3. Read     310ms
```

注意：

- **默认不开启**：不传该参数时不会打印任何额外内容，原有输出一字不变。
- **只读**：结果只打印到终端，不会写进 `dashboard-data.json`——输出结构与不开时完全一致，看板不受影响。
- 只统计 `event=tool` 事件；`ms` 缺失或非数字时按 `0` 兜底，与看板其它统计口径一致。
- `N` 大于实际 tool 事件数时取全部，不报错；没有任何 tool 事件时不打印该段。

### 自定义模型定价覆盖（可选）

内置价格表 `config/pricing.json` 覆盖不到的模型（自部署模型、内部代号、刚发布还没来得及收录的模型），
`cost_usd` 会算不出来。给这些模型补单价不用改内置文件——写一份**只含差异**的覆盖文件即可。

**放哪**：默认 `<project_root>/.claude/agent-observability/pricing.overrides.json`
（`<project_root>` 是 `.claude/` 所在的那一层，例如 `/Users/me/my-project`）。

刻意放在 `skills/` 树**之外**，原因有三：

- `scripts/build-classic-hosts.py` 会整棵同步 `.claude/skills` 到 `.claude/` `.cursor/`，
  放树内会让每次改动都产生 `--check` drift，还会把你的单价复制进生成的宿主包；
- `config/pricing.json` 属于 skill 自带资产，后续更新会把它冲掉，覆盖文件不会；
- 它是纯数据文件，删除或改名即可停用，不需要重启任何东西（hook 每次都是新进程）。

**格式**：键是模型名（匹配时忽略大小写与首尾空格），值是要覆盖的字段。
单位与 `config/pricing.json` 完全一致——**USD / 1M tokens**，可用字段只有四个：
`input`、`output`、`cache_read`、`cache_write`。

```json
{
  "my-model": { "output": 9.0 },
  "gpt-4o": { "output": 99.0 }
}
```

**合并是字段级的**：上面这份只改 `output`——`my-model` 的 `input`/`cache_read`/`cache_write`
沿用内置值（内置表里没有 `my-model` 时，未覆盖的字段按 0 计），`gpt-4o` 的 `input` 仍是内置的 `2.5`。
可以新增内置表里不存在的模型，但**不能删除**内置的模型或字段（合并只能加不能减）。

**两个环境变量**（都支持 `~` 展开）：

| 环境变量 | 作用 | 优先级 |
|---|---|---|
| `AOBS_PRICES_PATH` | **替换**整张基础表（不再读内置 `config/pricing.json`） | 低 |
| `AOBS_PRICING_OVERRIDES_PATH` | 在上面那张基础表之上**叠加**一份补丁，指向任意路径 | 高 |

两者同时设置时，覆盖文件里的字段最终生效。

> ⚠️ **只对新事件生效，不回溯**：`cost_usd` 在 hook 期就写进了 `metrics.ndjson`，
> 补价之后只有**之后新产生**的 usage 事件按新价格计算，已经落盘的历史成本不会被重算。

**出问题会静默降级**：覆盖文件不存在、JSON 非法、顶层不是对象、字段值不是数字，
四种情况都自动退回纯内置价格表——不报错、不打印、hook 照常退出 0
（否则一个手误的格式问题会让整条 hook 链路不可用）。所以"改了没生效"通常意味着文件没被读到，
先跑一次诊断：

```bash
python3 - <<'PY'
import sys
sys.path.insert(0, "scripts")
from core import emitter
print("path   :", emitter.resolve_overrides_path())
print("loaded :", emitter.load_price_overrides())
print("gpt-4o :", emitter.load_prices().get("gpt-4o"))
PY
```

`path` 是实际读取的位置（不是你以为的那个），`loaded` 为空说明文件没读到或全被判为坏字段。

**看板上的提示**：「建议关注」卡片第 4 条「模型定价」会列出**完全没命中价格表**的模型
（模型名 + 缺少成本的 usage 事件数）。模糊匹配（最长子串）命中的模型能算出成本，不会出现在里面。
处置方式就是把该模型写进覆盖文件；所有模型都有价时这条显示 green。

### 常见问题

- 没有日志：检查 hook 命令路径。
- `ms` 为空：通常是 pre/post 未配对。
- 没有 `usage`：`transcript_path` 缺失、tail 无 usage，或增量已去重。
- AgentLens 未启用：检查 `.state.json` 的 `_agentlens.enabled` / `last_error`。

### 已知问题（未修复）

- **工具失败事件的 `raw_response` 经常缺失（PostToolUse 与 transcript 落盘之间的时序竞争）**：
  真实 CodeBuddy CLI 场景下实测复现过——一次会故意制造失败的 `Bash` 调用（`exitCode=1`，
  transcript 里 `function_call_result.providerData.toolResult.rawResponse` 确实带了
  `is_error:true`/`exitCode:1`/`tool_error_code`），但最终写进 `metrics.ndjson` 的
  `tool` 事件的 `tool_details` 里完全没有 `raw_response` 字段。
  用实测时间戳定位到根因：PostToolUse hook 记录这次调用的时间是 `ts=...396.524`，
  但 transcript 里 `function_call_result` 真正落盘的时间是 `timestamp=...396.609`——
  **晚了 85ms**。hook 触发时去扫 transcript 文件的那一刻，CLI 还没来得及把执行结果
  那条记录写盘，`find_current_tool_context()`（`core/collector.py`）只能看到
  `function_call`（请求），看不到 `function_call_result`（结果）。已经单独验证过
  `merge_tool_records()` / `tool_details_from_record()` 的合并逻辑本身没问题——
  只要数据真的已经在文件里，能正确解析出完整 `raw_response`；问题纯粹是读的时机
  比 CLI 写盘早了一步。`collect_transcript_entries()` 用的是持久化的增量字节 offset
  游标，错过这次窗口后不会在后续调用里回头补扫，所以这次机会永久丢失，直接后果是
  `build_dashboard_data.py` 的 `tool_call_failed()`（无论怎么改判定逻辑）都拿不到
  数据，"工具失败率"/`failures` 列表对这类快速失败调用会漏检。
  复现方式：让 CodeBuddy 执行一个必然快速失败的命令（如 `ls /path/does/not/exist`，
  越快的命令越容易复现，因为 hook 触发与 transcript 落盘之间的竞争窗口更紧张），
  对比 `metrics.ndjson` 里该 `tool` 事件的 `tool_details.raw_response` 是否存在，
  和对应 transcript `.jsonl` 里 `function_call`/`function_call_result` 两条记录各自
  的 `timestamp` 先后。

  **影响范围（已精确定位，不是猜测）**：`raw_response` 在 `build_dashboard_data.py`
  里只有两处消费者——`tool_call_failed()`（喂给 `build_daily_and_sessions()` 的
  `day.failures` 计数和 `session.status`）和 `build_failures()`（"工具失败面板"的
  数据源）。真实数据统计过：107 次 tool 事件里只有 2 次带 `raw_response`，且只有
  `Bash` 调用会带这个字段（Read/Edit/Write/SendMessage 等其它工具从不带，不受
  此问题影响）。缺失时 `tool_call_failed()` 默认判"未失败"，所以效果是**恒定
  漏报，不会误报**——工具失败率/失败面板/会话状态列显示的"正常"可能掩盖了真实
  发生过的 Bash 失败。turns、duration、dispatch、cost、token、skill/rule 命中、
  devflow 阶段耗时、会话列表本身完全不受影响，是纯观测盲区，不影响 devflow 实际
  执行行为。

  **三次修复尝试均已失败，均已回滚（详见下方"已尝试且已放弃的修复方向"）**：
  真正阻塞方向 A 的证据很反常——三次独立测试里，预算从 160ms 加到 500ms 再加到
  2000ms，实测缺口每次都精确地"比预算多几十到一百多毫秒"（206/564/2151ms），
  不像是在等一个独立发生的固定延迟，更像是**hook 自己的同步等待在阻塞 CLI 落盘**
  ——等得越久，结果来得越晚。这个因果关系还没验证清楚，在验证清楚之前，继续在
  hook 里加同步等待大概率是死路，不建议再尝试。

### 已尝试且已放弃的修复方向（供以后参考，避免重复踩坑）

1. **方向 A：PostToolUse 里同步有界重试**（`claim=False` 轮询直到等到 `raw_response`
   或超时，只对 `tool_name=="Bash"` 生效）。三次真实端到端验证，预算 160ms/500ms/
   2000ms 全部失败，且"缺口≈预算+常数"的规律强烈暗示等待本身可能在拖慢 CLI
   落盘（见上文）。不建议在搞清楚这层因果关系之前继续加大预算。
2. **方向 B：推迟到 `pending_tool_emits` 重试队列，下次 hook 触发或 session Stop
   时再补**（`claim=False` 探测 + 延后 `claim=True`，避免过早消费掉 call_id）。
   逻辑和单元测试都通过，但端到端验证暴露了一个更深的、独立于这次修复的既有
   架构问题：`find_current_tool_context()`（工具上下文查询）和
   `emit_transcript_events()`（usage token 扫描）共用同一个持久化字节偏移游标
   （`core/state.py` 里只按 `(sid, transcript_path)` 区分，不分用途）。
   `emit_transcript_events()` 每次 `handle_post()` 都无条件推进这个游标，一旦
   推过某段内容，后续任何工具上下文重试在这段范围内都会**彻底找不到任何数据**
   （不只是缺 `raw_response`，连 `call_id`/`arguments` 都没了）——比不修复更糟。
   真实验证过两次：两条端到端测试调用最终落盘时 `tool_details`完全是空的。
   要让方向 B 真正可行，必须先给工具上下文查询一个独立于 usage 扫描的游标，
   这是范围更大、需要认真设计的改动，还没有细化方案。

以上两个方向的实现和回滚记录详见会话 memory（`project-agent-observability-raw-response-race`）。
