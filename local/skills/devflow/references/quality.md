# 审查、测试与知识沉淀规则入口

以下文件是各阶段的规则唯一事实源，由 `build_stage_prompt.py` 根据当前阶段自动注入，不需要协调者复制或转述：

- REVIEW：[rules/stages/review.md](../rules/stages/review.md)
- TEST：[rules/stages/test.md](../rules/stages/test.md)
- KNOWLEDGE：[rules/stages/knowledge.md](../rules/stages/knowledge.md)

前端、后端、Go、SQL 等专项检查由 [rules/manifest.json](../rules/manifest.json) 根据项目和需求证据叠加。新增或调整质量规则时修改上述规则源，不在本文件维护重复副本。
