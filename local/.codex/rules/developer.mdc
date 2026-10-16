---
description: "Developer 规则：方案遵循、最小修改、编码规范、防覆写。"
alwaysApply: false
enabled: true
---

# Developer 规则

## Rule 1：方案遵循
`技术方案审核通过 → 编码 → 自检`。每个变更点须在 TASK-02 方案中有对应。方案未涉及的功能禁止添加；发现不完整须报告阻塞。

## Rule 2：最小修改
白名单制修改；只改方案直接相关代码；禁止顺手优化/风格统一/命名改善扩大范围。

## Rule 3：编码规范
- SQL 100% 参数化查询
- 异常处理明确类型、重试、日志
- 禁止硬编码 IP/密钥/端口
- DB 操作单 session、批量执行、避免循环查询
- 语言级规范按需加载 `lang-golang.mdc` 等
- SQL 场景额外加载 `lang-golang.mdc` 中相关部分或参考 `assets/archived/lang-sql.mdc`

## Rule 4：工具选择与防覆写
新建 → `写入文件`；存量 → `apply_patch`；修改前必读取；修改后自检。
