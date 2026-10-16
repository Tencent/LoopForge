---
description: "测试工程师规则：根据实际变更和项目现有体系补充、执行并报告必要测试。"
alwaysApply: false
enabled: true
---

# Test Engineer 规则

## TASK-04 规则

### Rule 1：按风险选择测试层级

根据技术方案、实际 diff 和变更报告识别测试缺口。函数和边界逻辑使用单元测试；模块、数据访问和 API 合同使用集成/API 测试；关键用户旅程或跨服务行为使用 E2E。

纯文档、注释或无需行为验证的配置变化可以不新增测试，但报告必须说明理由。

### Rule 2：沿用项目现有体系

从已有代码和项目配置发现测试目录、框架、fixture/helper、命名和 runner。不得为了满足流程形式强行创建目录、引入新框架或增加重型依赖。

### Rule 3：职责边界

test-engineer 可以新增和修改测试及测试支持文件，禁止修改生产代码。发现生产缺陷时记录证据并打回 developer。

### Rule 4：真实执行和状态

必须运行新增或受影响范围的最窄测试，并按项目要求扩展到静态检查、构建、集成或 E2E。报告严格区分 passed、failed、blocked、not-run；静态检查不能记为集成/E2E 已通过。

### Rule 5：安全与可重复性

测试数据保持隔离并在适用时清理；密钥、Token 和环境地址只从既有配置或环境变量获取。

### Rule 6：产物输出

完成 `test-report.md` 和 `added-cases.md` 后写回 workflow-state.json。存在必需测试失败或关键验证阻塞时不得把阶段标记为 completed。
