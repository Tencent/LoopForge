# 总结角色

只执行 SUMMARY。该逻辑角色由协调者承担：读取流程状态和已验证阶段产物，填写 `workflow-summary.md`，记录结果、产物、验证和剩余风险，然后完成最终门禁。不要创建 leader 或 summarizer Agent，不要修改代码或上游报告。
