# TASK-03 门禁：代码实现（developer 自检 + leader 他审）

> ⚠️ **已合并到 CODE-REVIEW 阶段前置校验**（code-reviewer Step 0）。
> TASK-03 完成后不再经 leader 独立门禁审核，直接流转到 code-reviewer。
> 本文件保留作为参考，越界检查 + acceptance 完成度两项硬门禁已搬到 `code-reviewer.md` Step 0 和 `gate-code-review.md`。

## 硬门禁（自动检查）

- [ ] 产物文件存在：`{artifacts_dir}/03-code/change-report.md`
- [ ] YAML front matter 完整（task_id, stage, author, date, run_mode）
- [ ] **越界检查**：实际变更文件 ⊆ execution-plan.md 中所有 PT-XX 的 `files_whitelist` 并集（任一越界文件 → 直接打回）
- [ ] **acceptance 完成度**：所有 PT 的 acceptance 项均已验证且结果为 PASS（change-report.md 中 Acceptance 验证结果表格无 FAIL 项）⭐
- [ ] **API 文档产物校验** ⭐：只要本次需求涉及 HTTP API 接口的**新增、修改或删除**（含路由变更、请求/响应结构变更、接口行为变更），则 `{artifacts_dir}/03-code/api-docs.md` 必须存在且非空（缺失 → 直接打回要求 developer 执行 Step 6.5 补生成）
- [ ] workflow-state.json 中 `stages.TASK-03.artifact_path` 指向 change-report.md，`parallel_tasks_summary` 已写入

## 软门禁（AI 判断）

- [ ] 所有代码变更均可回溯到技术方案（无越界修改）
- [ ] 已完成自检（方案对照、功能正确性、编码规范、安全性）
- [ ] 各 sub-developer 局部报告已汇总且未私自写回 workflow-state.json
- [ ] 无 SQL 注入风险（所有 SQL 使用参数化查询）
- [ ] 无硬编码凭证
