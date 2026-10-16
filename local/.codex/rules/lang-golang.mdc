---
description: "Golang 编码规范（语言级）。仅当被改仓库主语言为 Go 时由 developer/test-engineer 按需加载。"
alwaysApply: false
enabled: true
---

# Golang 编码规范

> 仅当被改仓库主语言为 Go（`go.mod` / `.go` 为主）时加载。未明确说明处遵循 Google Golang 代码规范。
> `project_config.coding_standards` 优先级高于本规范。

## 要求等级
- **必须**：违反视为错误 | **推荐**：特殊情况可例外 | **可选**：按情况决定

## 代码风格

### 【必须】格式化
使用 `gofmt` + `goimports`。

### 【推荐】换行
建议 ≤ 120 列。长字符串/签名/import/生成代码/struct tag 例外。

### 【推荐】行数限制
文件 ≤ 800 行，函数 ≤ 80 行；单测文件 ≤ 1600 行，单测函数 ≤ 160 行。

### 【必须】Import
goimports 规则为准，完整路径，分三组（标准库 / 内部 / 第三方），按字母排序。

## 错误处理

### 【必须】Error
- 必须处理或明确忽略，error 为最后返回参数
- 独立错误流，`errors.New` / `fmt.Errorf("%w")`

### 【必须】Panic
- 禁止一般错误处理用 panic；可用于不变量断言
- 导出方法必须 panic 时用 `MustXXX` 命名

### 【必须】Recover
- 必须在 defer 中使用，禁止滥用捕获全部异常

## 注释

### 【必须】总则
- 导出名字必须有文档注释；注释掉的代码 review 前删除

### 【必须】包/结构体/方法/变量/常量/类型注释
- 格式：`// 名称 描述`

## 命名

### 【推荐】包名
小写短单词，与目录一致，不用下划线/混合大小写。

### 【必须】文件/结构体/变量/常量/函数命名
- 文件：小写下划线 | 其他：驼峰，首字母按导出控制
- 特有名词：`apiClient` / `APIClient` / `repoID`
- 枚举常量先创建类型

## 控制结构
- 【必须】range 只需 key 丢弃 value，只需 value 第一项 `_`
- 【必须】switch 要 default
- 【推荐】尽早 return
- 【必须】禁止 goto

## 函数
- 【推荐】参数/返回 ≤ 5 个，尽量值传递
- 【必须】defer 紧跟资源释放，判错后再 defer，禁止循环 defer
- 【必须】嵌套 ≤ 4 层
- 【必须】魔数用常量替代

## 单元测试
- 文件 `example_test.go`，函数 `TestXxx` / `Test_Foo` / `TestBar_Foo`
- 单测文件行数/函数行数为普通 2 倍

## 依赖管理
- 【必须】Go 1.11+ 使用 go modules
- 【推荐】`go.sum` 必须提交

## 应用服务
- 【推荐】有 README.md
- 【必须】有接口测试，重要导出函数有单测
