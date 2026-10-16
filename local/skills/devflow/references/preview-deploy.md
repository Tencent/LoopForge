# 预览与部署

仅在用户明确要求时执行。

1. 发现项目已有的预检、构建、发布脚本和 CI 配置；没有可靠入口时停止并说明缺失信息。
2. 检查当前分支、工作区和远端。不得自动清理改动、切换分支或绕过保护规则。
3. commit、push、创建 MR、触发流水线或部署前，展示将发生的外部写操作并取得明确授权。
4. 从当前项目的 CI 配置或用户输入取得环境、流水线标识和 URL 规则；通用流程不保存业务 ID、内网地址或凭据。
5. 只返回工具或 CI 的真实 build ID、日志和预览 URL，不根据分支名推断成功。

需要通用 Git 预检时，执行本 Skill 自带的 [scripts/preflight_preview.sh](../scripts/preflight_preview.sh)，参数为当前目录；Bash 不可用时按 [manual-runtime.md](manual-runtime.md) 的 Git 清单人工检查。项目不是 Git 仓库时不要执行。
