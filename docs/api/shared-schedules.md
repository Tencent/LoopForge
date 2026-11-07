# 共享 AI 定时任务与项目订阅

共享定义是不可变发布版本；项目订阅仍为独立的 `ScheduleJob`。不复制提示词，不继承发布项目的凭据、Bot 或执行记录。发布新版本不会改变已有订阅。

- `GET /api/shared-schedule-tasks`：列出已发布任务的最新版本，数组响应；平台登录用户可浏览共享定义。
- `GET /api/shared-schedule-tasks/{id}/versions`：按版本倒序返回不可变定义，未知任务返回 404。
- `POST /api/schedule-jobs/{id}/publish`：从独立 AI 任务发布共享版本。调用者必须具有来源项目写权限。body 为 `name, description, prompt, parameters, changeNote, baseVersion`。首次发布 baseVersion=0，之后为当前最新版本；竞争更新返回 409。触发配置和订阅不能再次发布。
- `PUT/POST /api/schedule-jobs`：订阅填写 `skipDispatch=true, sharedTaskId, sharedTaskVersion, parameterValues, projectId`。版本必须明确指定。保存时验证参数；不保存复制的提示词。名称、时间、模型、启用状态与通知仍属于本项目。首次创建省略时间/模型时使用已选发布版本的默认值。
- 升级沿用保存接口，显式修改 `sharedTaskVersion`。参数按新版定义重新校验；没有隐式升级。运行中的任务仍使用启动时的版本，之后运行使用保存后的版本。
- 新订阅读取、保存、运行和删除按目标项目权限校验；执行使用目标项目 Owner 的凭据。发布者不能通过共享定义取得订阅项目写权限。

参数结构：`{name,label,type,required,default}`，type 为 string/integer/number/boolean；值均为字符串。提示词使用 `{{params.days}}`。最多 20 个参数，名称唯一；未定义、未知、缺失必填或类型错误返回 400。默认值只用于未提交的字段；显式空值不能绕过必填校验。替换后提示词最多 4000 字节，替换单次完成，参数中的模板表达式保持原文。

```json
{"name":"需求停滞巡检","prompt":"检查超过 {{params.days}} 天未更新的需求，汇总实际结果。","parameters":[{"name":"days","label":"停滞天数","type":"integer","required":true,"default":"3"}],"baseVersion":0,"changeNote":"首次发布"}
```

发布后的版本只保存任务定义和推荐调度。来源任务删除不影响已发布版本或订阅，版本不会随通用定义历史的保留窗口被裁剪。第一版不提供版本删除、自动升级或执行身份继承。
