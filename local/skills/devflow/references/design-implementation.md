# 设计稿实现

1. 收集可访问的设计来源、实现范围、目标页面和验收视口。
2. 先分析页面层级、组件、布局、响应式规则、字体、颜色和资源，再编码。
3. 优先复用项目已有的设计系统和组件；保留真实文案、数量和资源，不以占位内容代替。
4. 分批完成“提取、规格、实现、数值回验、视觉回验”，每批最多 3 个可独立验证的 section。
5. 使用 `--require-design-artifacts` 初始化后，DESIGN 的 `prepare` 会从 `templates/` 自动生成 `overview.md` 和 `components.md`。每个 `sections/*.md` 从 `templates/design-context-section.md` 蓝图复制后填写；图片/PDF 视觉证据写入 `02-design/design-context/evidence/`。完成后执行 `scripts/validate_design_artifacts.py`。
6. 设计数据、资源或视觉工具不可用时说明缺失项，不根据截图臆造隐藏结构。
