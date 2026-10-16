---
description: "SQL 开发规范：索引规范、慢SQL防护、查询优化、分页规范、事务控制。由 developer / code-reviewer 按需加载。"
globs: "**/*.sql,**/*.go,**/*.py,**/*.java,**/*.js,**/*.ts"
alwaysApply: false
---

# SQL 开发规范

## S1：参数化查询（强制）

- 所有 SQL 必须使用参数化查询（PreparedStatement / ? 占位符 / $N 占位符）
- ⛔ 禁止字符串拼接构造 SQL（包括 ORM 的 raw query）
- 动态表名/列名/ORDER BY 必须使用白名单校验

## S2：索引规范

- WHERE、JOIN、ORDER BY 涉及的字段必须有索引支持
- 联合索引遵循最左前缀原则
- 单表索引数量不超过 5 个（特殊场景需说明理由）
- 禁止在索引字段上使用函数（如 `WHERE DATE(create_time) = ...`）
- 新增索引须评估对写入性能的影响

## S3：查询优化

- ⛔ 禁止 `SELECT *`，必须明确字段列表
- ⛔ 禁止在循环中执行 SQL（N+1 问题），应批量查询
- 大表查询必须有 LIMIT 限制或流式读取
- JOIN 表数量不超过 3 张（超过需拆分或使用子查询）
- LIKE 查询禁止前缀模糊（`LIKE '%keyword'`）

## S4：分页规范

- 标准分页使用 `LIMIT + OFFSET`，但深翻页（offset > 10000）必须改为游标分页
- 游标分页推荐格式：`WHERE id > :last_id ORDER BY id LIMIT :page_size`
- 所有分页接口必须限制 page_size 上限（建议 ≤ 100）

## S5：事务控制

- 事务范围最小化，禁止在事务内做 RPC/HTTP 调用
- 事务超时建议 ≤ 5 秒
- 必须有回滚处理（defer rollback 或 try-catch-rollback）
- 避免事务嵌套（如需嵌套使用 SAVEPOINT）

## S6：DDL 规范

- 新建表必须有主键（推荐自增 ID 或 UUID）
- 新建表必须有 `created_at` 和 `updated_at` 字段
- 字段必须有 NOT NULL 约束 + DEFAULT 值（除非业务明确需要 NULL）
- 字符串字段使用 VARCHAR 并指定合理长度（禁止滥用 TEXT）
- 大字段（TEXT/BLOB）单独建表，避免影响主表查询性能

## S7：数据安全

- 敏感字段（密码、手机号、身份证）必须加密存储
- 日志中禁止打印完整敏感信息（脱敏处理）
- DELETE 操作推荐使用软删除（`is_deleted` 标记）
- 批量 UPDATE/DELETE 必须有 WHERE 条件限制，禁止全表更新
