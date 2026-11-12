# LoopForge 前端设计约定

本文记录工作台、导航、设置和会话产物的共同设计规则，供后续前端迭代沿用。视觉结构参考 Grasp，交互图标采用 Harness UI，两者均为 MIT 许可。

## 设计参考

开头提到的两个上游在下表集中列出。本文没有约定的视觉或交互细节，先查这两个上游。

| 参考 | 链接 | 用途 | 许可与来源 |
|---|---|---|---|
| Grasp | 见许可文件 | 视觉结构：工作台布局、侧栏、产物抽屉、模型品牌图标 | MIT，`chatbot/src/assets/provider-logos/LICENSE.grasp`、`chatbot/src/assets/provider-logos/SOURCE.md` |
| Harness UI | 见许可文件 | 图标与动效，Status Badge、Selector、SlidingTabs、Switch 等组件设计语言，以及需求列表、运行记录、Bot 管理、知识中心、流程模板卡片的面板语言 | MIT，上游版本 `da2e476895331ef4afee30878af0fb3fb1c73131`，`chatbot/src/HARNESS-UI-LICENSE.txt`、`web/app/harness-ui-LICENSE.txt` |

引入上游素材或代码时保留对应的 MIT 许可与来源说明。

## 范围与原则

- 保留现有会话、项目、需求、工作流、执行机器、审批和产物功能，通过布局与交互优化入口。
- 本轮改动限于 `chatbot/`、`web/` 和本文档，不修改后端 API、权限规则、数据库或迁移。
- 页面以内容为主：减少重复标题、提示语和装饰性入口；需要的功能收进明确的菜单。
- 悬停只改变颜色、覆盖层或图标内部图形，按钮、图标外框和列表行的位置保持稳定。
- 明暗主题使用同一套布局。所有动效遵循 `prefers-reduced-motion`，减少动态效果时直接呈现可操作的最终状态。

## 布局与配色

| 元素 | 约定 |
| --- | --- |
| 全局图标栏 | 宽 56px，保留业务导航和底部设置入口，不放顶部装饰 Logo |
| 二级侧栏 | 桌面默认宽 304px；工作台与设置保持一致，工作台保留拖动及宽度记忆 |
| 侧栏卡片 | 上下留白 14px、圆角 16px、细边框；标题与列表共用完整卡片背景 |
| 首页内容 | 输入区最大宽 768px，品牌组合与输入区对齐，短屏允许纵向滚动 |
| 产物抽屉 | 默认宽 440px，分隔拖动条 4px；窄工作区使用覆盖面板 |
| 模型菜单 | 宽 240px、圆角 12px、最大高 420px，与触发器间隔 6px |

基础颜色由 CSS 变量管理，组件不依赖截图里的固定背景色。

| 用途 | 浅色 | 深色 |
| --- | --- | --- |
| 主表面 | `#ffffff` | `#141417` |
| 图标栏底色 | `#f6f6f6` | `#0a0a0b` |
| 主文字 | `#18181b` | `#ededf0` |
| 次级文字 | `#71717a` | `#a1a1aa` |
| 分隔线 | `#e5e5e8` | `#26262b` |
| 选中底色 | `#f1f1f1` | `#26262c` |

悬停使用轻量中性灰，选中态保持稳定灰底。成功、失败、运行等业务颜色只表达真实状态，不用常驻彩色底替代普通导航选中态。

## 导航与设置

- 主导航保留全部业务入口，个人配置、配置与管理、执行机器统一归入设置。
- 从工作区点击全局设置按钮，默认打开个人配置；处于设置或管理区域时再次点击该按钮返回工作台首页。
- 侧栏折叠按钮只控制侧栏显示，不改变当前业务页面。折叠和展开都要有完整过渡。
- 图标栏里、设置按钮正上方有导航开关（`#railSidebarToggle`，Harness `panel-left`，`aria-controls="platformRail"`）。默认仍是 56px 图标栏；点开关后栏宽变为 240px，每个入口展开成和设置导航一样的「图标 + 文字」行，再次点击回到只有图标。图标栏不会被收成 0。当前页面、会话列表和设置导航都保持不变。文字已经可见时不再弹出悬停提示。宽度变化沿用外壳的栏宽过渡；系统开启减少动态效果时立即切换。
- 流程设计页 SOP 名称旁有一枚不占视觉重心的复制按钮（`#copyWorkflowName`，Harness `copy`，14px，默认弱化色）。它只把当前 SOP 名称写入剪贴板；点击不打开切换菜单。没有打开流程时按钮隐藏。悬停提示只用 `data-tip` 一处，不再同时写浏览器 `title`。切换列表里每个 SOP 名称右侧也有同样的复制按钮（`.fd-pop-copy`），只复制该行名称，不切换 SOP、不关闭菜单；这一处只用 `title`。
- 设置模式与工作台模式切换通过共享过渡控制器衔接；过渡副本不得接受点击、键盘焦点或提交操作。
- 凭据定位等跳转应在目标页面挂载后聚焦；用户已切换到其他页面时取消旧的焦点请求。
- 对话失败如果是因为当前服务器没有安装所选 CLI，错误写明「没有安装」，并在同一条提示里提供「安装」按钮。点击后按钮变为「自动安装」，刷新页面也保持这个状态，直到安装结束。安装完成后，原来的「没有安装」或「AI 助手生成失败」改为可以重新发送。按钮只安装平台允许的 CLI。安装在服务器上继续，关闭页面也不会中断。
- 个人配置里，Codex、Claude Code，以及带协议的 Agent CLI，可以维护模型目录：获取可用模型，勾选、排序、修改显示名。目录为空时，未填 Base URL 或协议是 Gemini 官方 API 则使用该 CLI 自带模型，填写了 Base URL 则跟随服务公布的模型。保存过的目录在官方服务和自定义地址上都生效。Cursor CLI 只使用官方模型。
- 主题入口在左下角头像卡片（`#userProfilePopover`）的「外观」行：「浅色 / 深色 / 跟随系统」三段开关（`.upp-theme-seg`，`role="radiogroup"`），访客与登录卡片都显示，选项文案与 chatbot 设置一致，偏好写入共享键 `agentflow.theme.v3`。三段前分别是 Harness `sun`、`moon`、`monitor` 图标，悬停、聚焦或点击时播放一次动效；选中滑块由 SlidingTabs 驱动，卡片展开时立即对准当前选项。切换时页面颜色约 280ms 渐变，减少动态效果时直接切换。
- 顶栏 `#themeToggle` 只在宽度 ≤1000px（图标栏隐藏）时显示，作为浅色、深色一键切换的兜底；宽屏下仅隐藏，DOM 节点保留给 AI 助手按钮定位。

### 顶栏标题

页面名在可视区域只出现一次。视图自带页头（`.page-heading`、`.page-header` 或知识中心的 `.kb-head`）时，导航函数给 `body` 加 `view-has-heading`，`product-v4.css` 隐藏顶栏 `.page-context`；搜索、项目切换、窄屏主题切换和窄屏菜单按钮不受影响。没有页头的流程设计页仍由顶栏显示标题，AI 工作台的顶栏本身隐藏。`#pageTitle` 文本始终随视图更新，供 e2e 断言与辅助技术使用。新增视图如果自带 H1，应使用 `.page-heading`，不要在页面内另写同名标题。

会话树的标题图标中心与竖线对齐。当前树轴为 17px，横向分支为 11px，列表文字统一左对齐。整行悬停底色覆盖标题、参与者和状态标记，不在行尾留出突兀缺口。

会话操作通过右键菜单提供，保留键盘菜单键、Shift+F10 和触屏长按入口。置顶、重命名、归档、删除沿用原有权限。普通行不再增加常驻省略号按钮。

## 图标与通用组件

流程设计的发布按钮在发布期间和当前内容已发布时禁用；保存相同内容不新增修订，服务端重复发布保持版本不变。保存后的新修改可继续发布。流程切换遇到未保存修改时，按钮明确为「继续编辑 / 放弃修改并切换」。脚本编辑器的输入层、行号与高亮层共用字体、字距和 Tab 宽度，长行横向滚动，高亮随输入层同步滚动。

技能详情的文件树限高并独立滚动。

### Harness 图标

`HarnessIcon.jsx` 与原生页面的 `harness-motion.js` 使用同一来源的 SVG 定义。上游版本为 `da2e476895331ef4afee30878af0fb3fb1c73131`，MIT 许可随源码保留。

原生页面的模板可以直接写空的 `<svg class="agent-icon harness-icon" data-icon="…">` 占位，由 `AgentFlowHarnessIcons.hydrate()` 填入图形；`#issuesView`、`#assetsView`、`#approvalsView` 内新插入的节点会自动填充，挂到 `<body>` 的浮层需在渲染后调用一次。

图标动效在路径、描边或内部图形上播放；不得通过整体上移、弹跳或缩放来模拟悬停反馈。载入、处理中等持续状态可使用对应循环图标，普通图标使用有限播放。

### 模型与权限选择

- 模型触发器和选项显示品牌图标；未知品牌使用通用图标，不影响模型可选性。
- 品牌图标保留原始颜色及明暗主题变体，悬停时只在图形内部播放短暂光效。
- 模型列表保留服务端顺序、真实模型 ID、名称、系列及倍率信息，品牌推断只用于展示。
- 搜索支持名称、ID 和系列；选中态使用固定勾选位置，搜索为空时显示明确结果。
- 浮层根据可用空间向上或向下展开，限制在可视视口内；手机键盘改变 `visualViewport` 时重新定位。
- Escape 关闭并返回触发器，Tab 接回输入区的正常顺序；点击外部关闭时不抢焦点。
- 权限选择保留原有确认模式和选值，视觉调整不改变执行权限或审批语义。
- SOP「AI 编排助手」在输入区下提供 CLI 与模型选择，复用 `/api/assistant/models` 的个人模型目录，拆成「CLI」「模型」两个按钮，模型列表只显示所选 CLI 的模型，两处都支持搜索。选择独立记忆；同 CLI 切模型保留会话，跨 CLI 开启新对话，画布保留。生成期间禁用选择器，审批仍为每次确认，只生成草稿。面板使用固定标题栏、独立滚动消息区和底部输入框，推荐场景、发送按钮和消息状态完整应用 Harness 样式；窄屏下限制在画布宽度内。

模型品牌 SVG 来自 Grasp 的品牌资源集合。具体来源及许可见 `chatbot/src/assets/provider-logos/README.md`、`SOURCE.md` 和相邻许可文件。

### 标签页与悬停

原生页面和 React 标签页共享 `sliding-tabs.js` / `sliding-tabs.css`，滑动指示层跟随真实选中项。切换时保留业务内容、键盘顺序与选中语义。

普通按钮使用短暂的颜色过渡；导航和会话行的灰色覆盖层从指针位置展开。悬停效果不改变控件的宽高、边距、布局位置或文本对齐。

## 设计规范

`web/` 原生页面只有一套 Harness 规范（`chatbot/` 另有自己的样式，不在此列），唯一来源是 `web/app/harness-ui.css`。它在 `index.html` 中紧接 `product-v4.css` 加载，位于所有页面样式之前，页面文件可以在其后覆盖尺寸。该文件只定义外观（颜色、边框、圆角、阴影、字号、组件内间距、聚焦态），不定义 display、网格、分栏或位置；页面布局仍由原页面规则决定。

### 令牌

全部 `--hui-*` 只在 `harness-ui.css` 的 `:root` 与 `html[data-theme="dark"]` 声明，挂到 `<body>` 的浮层直接继承。其他文件只能引用，不得重复声明（`harness_design_system_test.go` 守护）。页面私有令牌族（`--rl-*`、`--cr-*`、`--bot-*`、`--kb-*`、`--pw-*`、`--hu-*`、`--rv-*`、`--pa-*`）只作为别名引用 `--hui-*`，或保存动效时长、终端底色等页面独有值。

| 令牌 | 浅色 | 深色 |
| --- | --- | --- |
| 强调色 / 悬停 / 淡底 | `#7b61ff` / `#684ce9` / `#f0edff` | `#a08aff` / `#b2a0ff` / `#242033` |
| 主文字 / 次级 / 弱化 / 更弱 | `#252529` / `#62626c` / `#6f6f79` / `#9a9aa4` | `#ededf0` / `#b1b1ba` / `#a1a1ac` / `#6e6e78` |
| 边框 / 细边框 / 强边框 | `#eae9ee` / `#f1f0f4` / `#dcdbe3` | `#26262b` / `#1f1f24` / `#393940` |
| 画布 / 浅底 / 表面 / 舞台 | `#fafafb` / `#f5f6f7` / `#ffffff` / `#f4f3f8` | `#111114` / `#1c1c21` / `#141417` / `#18181c` |
| 状态色 info / success / error / warn / neutral | `#597dcc` `#338768` `#c15161` `#b7791f` `#6b6b76` | `#8aa8ee` `#34d399` `#f0899a` `#f2c46d` `#a1a1ac` |
| 阴影 | `--hui-panel-shadow`（面板、卡片）与 `--hui-popover-shadow`（菜单、下拉、浮层）分开使用 | 同名 |

设置区里尚未改写的旧页面（个人配置、项目配置、执行机器、角色定义、研发效能、业务领域、接入指南、平台管理、领域管理）在各自视图作用域内把旧基础令牌（`--text`、`--muted`、`--line`、`--panel`、`--blue`、`--v3-*` 等）映射到 `--hui-*`，只改颜色，不动尺寸。

### 组件外观

| 组件 | 规格 |
| --- | --- |
| 按钮 | 三级：实心强调色（`.button.primary` / `.button.dark`，文字 `--hui-on-accent`）、淡紫底紫字（`.hui-btn-soft`，行内主操作）、白底强边框（默认）。另有红字红描边 `.danger-outline` 与无边框图标按钮 `.hui-icon-btn`。圆角 7px，聚焦为 `--hui-focus-ring`，禁用 45% 透明；悬停只改颜色。高度沿用原元素，需求列表、资产、待我确认、MCP、Bot 页面级 32px、`.small` 28px |
| Status Badge | `.hui-badge[data-tone]`，色调 neutral / info / success / error / warn / accent / muted；胶囊形，状态色 8% 底 + 23% 描边。默认 26px，`data-size="sm"` 为 24px、12px 字。前置 `.hui-lamp`（呼吸点，`.is-hollow` 为空心）或 `.hui-badge-glyph` 图标槽，文字放 `.hui-badge-label`。`issueStageTone`、`runStatusBadgeHTML`（含 SSE 打补丁）、`botBadge` / `botPatchDeliveryRow`、`kbBadge`、`credentialBadgeHTML` 都输出它，原有 `data-status` 保留 |
| 面板 / 卡片 | 圆角 14px、`--hui-border` 细边框、`--hui-surface` 底、`--hui-panel-shadow`；不用彩色底边、彩色左边或渐变底 |
| 页头 | H1 22px/600，说明 13px 次级色；无眉标 |
| 输入 | 强边框、7–8px 圆角，聚焦为强调色边框 + 3px 淡紫外圈；开关开启为强调色 |
| Selector / 菜单 | `.hui-select-*` 与 `.hui-menu*`：8px 圆角、细边框、`--hui-popover-shadow`，选项 36px，同一时刻只开一个浮层 |
| 图表 | 单序列主色为 `--hui-accent`；多序列图保留区分色 |

### 启用视图与设置区容器

按钮、页头、输入的映射只对登记视图生效：`#issuesView`、`#approvalsView`、`#assetsView`、`#mcpView`、`#botsView`、`#assistantView`、`#knowledgeView` 以及设置区各视图（`#settingsView`、`#projectsView`、`#runnersView`、`#agentsView`、`#metricsView`、`#businessDomainsView`、`#guideView`、`#platformView`、`#businessDomainManagementView`），外加挂到 `<body>` 的 `#agentModal`、`#customMcpModal`、`.pw-root`、`#kbDrawer`。

设置区视图的外层内容统一居中、最大宽 1200px（`.page-view.active` 的直接子元素），页面不再各自限宽。流程设计（`#flowView`）、工作区页面、图标栏、侧栏与顶栏不受影响。

### 表单控件、对话框与提示

两套页面都不显示浏览器默认的下拉、勾选、日期、上传控件，也不弹浏览器对话框和 `title` 气泡。原生页面采用渐进增强：原生元素留在 DOM 中负责取值、表单提交和 `input` / `change` 事件，Harness 层只负责绘制和交互，业务代码照常读写 `value`、`checked`、`disabled`。控件样式集中在 `web/app/hui-controls.css`：它是 `index.html` 中第一个样式表，只引用 `--hui-*` 令牌（Tooltip 底色、遮罩和浮层层级也在 `harness-ui.css` 中声明），各页面样式表可以在自己的作用域里覆盖尺寸。

元素或任一祖先带 `data-native` 时，以下增强和全局勾选样式全部跳过，保留浏览器控件。目前没有页面使用；只有确实需要浏览器原生行为（如系统级多选列表）时才加，并在这里记录原因。

| 控件 | 规格 |
| --- | --- |
| 下拉（Selector） | 单选 `select`（不含 `multiple`、`size>1`）。面板 8px 圆角、细边框、面板阴影，选项 36px、选中项淡紫底加勾，`optgroup` 显示为不可选的分组标题，`hidden` 选项不显示，超过 5 项出现搜索框。键盘：↓ / ↑ / Enter / 空格 / F4 打开，方向键、Home、End 移动，Enter 选中，Escape 关闭并回到触发器，Tab 关闭后继续正常顺序 |
| 复选 / 单选 | 16px；复选框 4px 圆角，单选为圆形；选中为强调色底加白色勾（CSS mask），`indeterminate` 为横杠；禁用 45% 透明度；键盘聚焦显示强调色外圈 |
| 开关 | `input[type="checkbox"][role="switch"]` 或 `.hui-switch`，统一 34×20、圆钮 14px，开启为强调色；仍是开关形态，不退化成复选框 |
| 确认对话框 | `afConfirm` / `afPrompt` / `afAlert`（`web/app.js`）是唯一入口，返回 `Promise<boolean>` / `Promise<string \| null>` / `Promise<void>`，不再回落到浏览器对话框；页面缺少 `#confirmModal` 时按模板创建。卡片 14px 圆角，图标为 Harness 图标（警告 / 删除 / 信息 / 编辑 / 成功），危险操作用红色实心按钮。同时只开一个，后来的请求排队；Escape 或点遮罩取消，Enter 确认（多行输入需 Ctrl/⌘+Enter），Tab 留在对话框内，关闭后焦点回到打开前的元素；约 180ms 淡入上浮 |
| 日期 | `input[type="date"]` 后插入带日历图标的触发器，浮层为周一起始的 6×7 月历，带上 / 下月、「清除」「今天」；遵守 `min` / `max`，`required` 时不能清除。键盘：方向键按天 / 周移动，PageUp / PageDown 换月（加 Shift 换年），Home / End 到周首尾，Escape 关闭。值始终写成 `YYYY-MM-DD` |
| 上传 | 只增强可见的 `input[type="file"]`：虚线拖拽区、「选择文件」（或「选择文件夹」）按钮和文件列表（最多列 5 个）。拖放按 `accept` 和 `multiple` 校验，错误以 `role="alert"` 显示；文件夹只能点按钮选择。由自定义按钮打开的隐藏文件输入不改动，按钮使用 Harness 按钮规格 |
| 提示（Tooltip） | 接管所有 `title`：悬停约 400ms、键盘聚焦立即显示，显示期间 `title` 暂存到 `data-hui-title` 并设 `aria-describedby`，离开、失焦、按下、滚动、Escape 或元素被移除时隐藏并恢复。单个 `role="tooltip"` 气泡，上方优先、空间不足时翻到下方，最大宽 280px。触屏不显示；`data-hui-tooltip="off"` 和 Monaco 编辑器内部跳过 |

下拉有两种模式。默认的原地模式：`select` 本身就是触发器（样式由 `hui-controls.css` 统一为 Harness 外观），文档级的 mousedown / 触摸 / 键盘监听把浏览器弹出层换成 Harness 面板。面板只在打开期间存在，退出动画结束后从 `<body>` 移除，所以重绘视图不会留下实例，现有布局样式也不需要改。显式调用 `enhanceSelect(select)` 进入触发器模式：隐藏 `select`，在后面插入 `.hui-select-trigger` 按钮，同步 `disabled`、`hidden`、内联宽度和 `label` 名称；需求列表筛选、MCP 安装范围和资产页仍使用此模式。

浮层共用 `window.HuiPopover`（`hui-select.js`）：同一时刻只开一个（下拉、日期、需求页「更多」菜单互斥），单例的外部按下、窗口缩放和外部滚动监听负责关闭，定位时限制在视口内，层级高于 `.modal`。`window.HuiAuto` 用一个文档级 MutationObserver（按微任务合并）自动增强新插入的日期和上传输入，并在原生元素离开文档后销毁实例、移除浮层和监听。因此 `hui-select.js` 必须先于 `hui-date.js`、`hui-upload.js`、`hui-tooltip.js` 加载，四者都在 `app/issues.js` 之前，`app/start.js` 仍最后。

`chatbot/` 是 React 受控组件，不做 DOM 增强：下拉使用 `HarnessSelect.jsx`（与 Selector 同规格，`onChange` 收到 `{ target: { value } }`，可直接复用原 handler）；勾选样式和 Tooltip 直接引入同一份 `web/app/hui-controls.css` 与 `web/app/hui-tooltip.js`，令牌由 `chatbot/src/hui-tokens.css` 声明（工作台不加载 `harness-ui.css`，取值需与其保持一致），只依赖 CSS 和事件委托，不改 React 管理的状态。

减少动态效果时，以上控件的过渡和弹窗动画全部关闭，直接呈现最终状态。

## 需求列表

需求列表页（`#issuesView`：标题区、项目条、筛选栏、列表、看板、分页）按 Harness UI 的设计语言绘制，数据、列和筛选逻辑不变。令牌、按钮、Status Badge、Selector 与菜单见「设计规范」；页面独有样式在 `web/app/issues-list.css`，在 `index.html` 中最后加载，规则限定在 `#issuesView` 及其挂到 `<body>` 的两个浮层内。

- 按钮分三级：整页只有「新建需求」使用实心强调色；行内主操作用淡紫底紫字；其余为白底强边框。「处理失败」用红字红描边，「⋯」是无边框图标按钮。页面级按钮 32px，行内 28px，悬停只改颜色。
- 标题区右侧提供刷新、「更多」、「导入 TAPD」和「新建需求」。「更多」保留外部同步和触发配置，同步状态与触发配置数显示在菜单项右侧；同步失败时「更多」带红点。AI 定时任务独立放在全局图标栏，页面按当前项目展示，只管理提示词任务；触发配置只管理 SOP 自动派发，每个项目一份，弹窗直接编辑已有配置，首次填写后保存；不提供新建按钮和任务名称。两者沿用原有 API 和数据。原按钮 id 全部保留。
- 当前环节使用 Status Badge，颜色由环节类型和文字共同决定：运行中蓝色呼吸点、排队中蓝色时钟、待澄清与待确认琥珀色、失败红色、已合入与已完成绿色、已关闭与未开始灰色。映射在 `issueStageTone()`。
- 四个筛选由 `hui-select.js` 以触发器模式增强为 Harness Selector：原生 `select` 仍在 DOM 中负责取值和 `change`，增强层通过 MutationObserver 和 `value` 写入同步选项与文字。选项超过 5 个时显示搜索框，选了非默认值时触发器显示淡紫底。
- 「更多」菜单、筛选面板和行内「⋯」共用同一套面板规格：8px 圆角、细边框、面板阴影，选项 36px，同一时刻只开一个浮层。
- 需求的菜单和详情显示「删除需求」。有已结束的任务或运行记录、或需求已完成时，入口显示「强制删除」，危险操作确认框说明会一并清理执行数据。批量删除包含这类需求时，确认框显示需要强制删除的条数。澄清或执行尚未结束时，入口置灰并显示后端返回的原因和处理入口。未知删除条件提示刷新，服务端按完整历史校验。
- 优先级使用信号格图标，紧急使用闪电图标；责任人前显示首字头像，Agent 用机器人图标，未指定用人像占位。超长编号单行截断，列宽上限 120px，悬停显示完整编号。
- 窗口 1200px 以下搜索框、筛选按钮和项目数据块同步收窄，1024px 下筛选栏保持一行；960px 以下标题区、项目条和筛选栏改为上下堆叠。

需求列表页的选中态（筛选已生效、项目数据块、勾选行、当前页码、Selector 选中项）使用强调色淡底，是「悬停用中性灰、选中用稳定灰底」规范的例外：这里的选中表示筛选或批量选择已生效，需要和普通悬停明显区分。其他页面仍按中性灰规范。

- AI 定时任务页通过「本项目任务 / 共享任务市场」SlidingTabs 切换，市场沿用 MCP 市场卡片与搜索，展示来源、版本、推荐调度及本项目订阅数量；允许继续添加不同参数或调度的订阅。独立任务可发布不可变共享版本。项目订阅显示来源与固定版本，在原任务弹窗填写业务参数，保留独立调度、模型与 Bot 通知。有新版的订阅入口使用淡紫强调色；版本选择、提示词与参数对比直接展开在同一个任务编辑器内，参数随目标版本更新，点击「保存并升级」生效。市场详情保持独立只读窗口。编辑保存成功自动关闭弹窗并刷新列表，失败保留输入；迟到的保存响应不得关闭新打开的编辑器。弹窗沿用 Harness 令牌与关闭交互，窄屏改为单列。

## 待我确认

待我确认页（`#approvalsView`：页头、统计条、卡片、空状态）按需求列表的 Harness 先例绘制，操作语义、接口和按钮文案不变。令牌与共享组件见「设计规范」；页面样式集中在 `web/app/approvals-harness.css`，在 `index.html` 中最后加载，规则全部限定在 `#approvalsView` 内，只引用 `--hui-*` 令牌。

| 区域 | 约定 |
| --- | --- |
| 页头 | 无眉标；H1 28px/600 + 14px 说明。右侧「更新于 HH:MM」为无底色呼吸灯 + 12px 弱化文字，有待确认项时灯为 `--hui-success`；刷新为 32px 白底强边框按钮，前置 `refresh` 图标 |
| 统计条 | 无边框弱化文字「N 条待确认」，数字为主文字色 500 |
| 卡片 | 纵向：标题行 → 正文 → 按钮组。1px `--hui-border`、13px 圆角、`--hui-surface` 纯色底，无渐变与阴影，内边距 16px 18px |
| 标题行 | 左侧标题 14px/500 + Status Badge，下方元信息 12.5px 弱化色，分隔符「·」，需求号为等宽字；右侧依次为「打开需求」（或「排查日志」/「查看对齐详情」）、有新版本且未暂停时「更新为最新 SOP（vX #Y）」、「⋯」。次要按钮 28px、7px 圆角、强边框 |
| ⋯ 菜单 | 无边框 28px 图标按钮（`more-horizontal`，`aria-haspopup="menu"`、`aria-expanded`），展开卡片内右对齐的操作浮层，规格同需求列表行内浮层；唯一菜单项「取消流程」为红色 danger 项，点击后先收起浮层再弹原有确认框。同一时刻只开一个，点击外部、Escape、滚动或列表重绘时关闭 |
| 正文 | 判断依据为 `--hui-canvas` 底、`--hui-border-soft` 细边框、9px 圆角的块，小节标题 12px 弱化色；暂停面板去掉自带标题条与边框，标题与说明合成同款块。补充上下文输入框强边框、8px 圆角，聚焦为强调色边框 + 淡紫外圈 |
| 按钮组 | 右对齐、可换行、宽度按文字；主操作（确认通过 / 重试本节点 / 直接继续）通过 CSS `order` 放在最右；「不重试」为红字红描边 |
| 空状态 | 细边框 13px 圆角容器内居中：淡紫 `check` 图标块 + 14px/500 标题 + 13px 弱化说明 |
| 窄屏 | ≤960px 页头上下堆叠，次要操作换到标题与元信息下方，按钮组换行仍右对齐 |

状态标签由 `approvalStatusBadgeHTML()` 生成，规格同需求列表 Status Badge：待确认（含对话轮次用尽）为琥珀 `pending-review`，与需求列表「待确认」一致；已暂停为中性灰 `pause`。

**实心主按钮破例**：每张卡片的主操作使用实心强调色（文字为 `--hui-surface`，即浅色白字、深色 `#141417`），不遵守「整页只有一个实心按钮」。每张卡片都是一次独立决策，主操作需要一眼识别；该破例经用户确认，只限本页卡片。

决策面板（`humanDecisionPanelHTML`、`pausedRunPanelHTML`、`waitingActionButtons`、`refreshRunSOPButtonHTML`）同时用于需求抽屉和运行详情，输出不变；本页只重组卡片外壳（`approvalCardHTML()`），按钮位置靠 CSS 调整，其他页面的面板外观不受影响。

## 业务领域

业务领域报表页（`#businessDomainsView`）以统计页为设计基准，接口、统计口径、排序与下钻不变。`#businessDomainsView` 加入 `web/app/issues-list.css` 的共享亮/暗 `--hui-*` 令牌选择器；样式在 `web/app/business-harness.css`，于 `observability-harness.css` 之后加载，规则全部限定在 `#businessDomainsView` 内。`web/business-domains.css` 保留给「领域管理」编辑页和项目配置的业务归属字段。

| 区域 | 约定 |
| --- | --- |
| 页头 | 无眉标；「导出报告」「刷新」均为 32px 白底强边框按钮，前置 `download` / `refresh` 图标，不用实心主按钮。id 保持 `#businessExport`、`#businessRefresh`，报告未就绪时导出禁用 |
| 筛选栏 | 12px 圆角白底面板；时间段为 SlidingTabs（轨道 34px）；业务范围为 Harness Selector（选了非默认值时淡紫底）；起止日期为 34px 输入框；「应用筛选」34px 细边框按钮 |
| 指标区 | 一张分组指标卡：标题行为业务范围、日期区间与项目数；6 个交付指标（新增完成后合入耗时，标签 12px、数值 24px 等宽、竖分隔线、脚注 12px 弱化色）；资源概要作为卡底次级指标行。无样本时数值显示「—」，脚注写「待采集 · N 个样本」；表格中仍显示「待采集」 |
| 数据提示 | 琥珀色提示条，前置 `warning` 图标，文案不变 |
| 趋势图 | `obs-card2` 卡片 + `obsChartSlot`。交付为两色分组柱图（`--c1` / `--c2`，与图例一致），完成周期为折线 + 点，缺样本日期为 `null` 断开。无数据时图位收为约 110px 的虚线框空状态。ECharts 不可用时显示原自绘 SVG 并标「简版图表」 |
| 表格 | Harness 面板头 + 计数徽标；表头 36px 弱化色、行高 52px、悬停中性灰；业务域行前有首字头像，名称按钮带 `chevron-right` 并保留 `data-business-scope` 下钻；排序为 Selector |
| 折叠区 | 「运行与资源投入」「统计口径与数据覆盖」为 12px 圆角面板，`chevron-right` 展开时旋转 90°；展开后挂载并 resize 区内图表 |
| 状态 | 加载为旋转指示 + 原文案，出错为红色提示面板（`role="alert"`），筛选栏始终在上方，位置不跳动 |
| 窄屏 | ≤1100px 指标 3 列，≤850px 图表与折叠区单列，≤700px 指标 2 列、筛选控件换行铺满 |

图表 option 由纯函数 `businessDeliveryOption`、`businessCycleOption`、`businessRunsOption` 生成，复用 `obsChartBase`、`obsCategoryAxis`、`obsValueAxis`、`obsTooltipRows`，提示框内容经 `obsEsc` 转义。图表实例与统计页共用 `obs-charts.js` 的全局注册表，因此 key 一律带 `biz-` 前缀；本页重渲染前与离开视图时由 `businessDisposeView()` 只释放本页图表与 Selector 浮层。

导出报告：重新生成报告模板，`echarts.init(null, null, {renderer: 'svg', ssr: true, width, height})` 关闭动画后用 `renderToSVGString()` 把图表槽替换为静态 SVG，图表库不可用时使用自绘 SVG；内联 `business-domains.css`、`business-harness.css` 与当前主题下计算出的 `--hui-*`、`--c1`…`--c4` 值，外层包 `#businessDomainsView`，折叠区默认展开，不含 `<script>` 与外部资源，文件名仍为 `业务报告-YYYY-MM-DD.html`。

## 首页品牌入场

首页采用左 Logo、右标题和副标题的组合，背景透明。Logo 使用 SVG 路径绘制，不播放视频、不插入全屏遮罩，输入区始终可用。

顺序为：渐变圆弧画成圆环，圆环收拢成 LoopForge 标记，蓝紫光圈扫出标题，副标题“把想法，变成下一步。”连续显现，最后停止在正常主题下的静态字标。

所有阶段共用 `--home-intro-tempo: .45`，包括尾环的延迟，避免只加快局部造成不同步。

| 阶段 | 当前时间 |
| --- | --- |
| 圆环绘制 | 0–450ms |
| 收拢成 Logo | 450–675ms |
| 光圈扫过标题 | 约 576–1206ms，尾环随后淡出 |
| 标题完整呈现 | 约 1170ms |
| 副标题连续显现 | 约 1238–1580ms |
| 光标与尾圈全部结束 | 约 1778ms |

Logo 默认 64px，小屏或短屏为 52px；标题为 36px，小屏为 30px。文字完整宽度从首帧预留，使用遮罩显现，不逐字改写 DOM，不产生宽度跳动。主题切换、输入和悬停不重播入场动画。

## 浮层、折叠与焦点

| 场景 | 行为 |
| --- | --- |
| 工作台侧栏、产物抽屉 | 约 220ms 的展开和收起，固定网格列，窄屏保持会话主体稳定 |
| 原生设置侧栏 | 约 240ms 的双向宽度与裁切过渡 |
| 模型菜单 | 约 160ms 的显隐过渡，关闭后延迟卸载 |
| `@` 引用面板 | 约 260ms，从输入框上方由下向上展开，关闭时反向收起 |
| 会话分组 | 约 220ms 的内容折叠，悬停不改变位置 |
| 快速接入向导 | 弹窗约 300ms 淡入上浮、关闭反向后卸载；步骤约 320ms 按前进/后退方向滑入；仓库区约 300ms 高度折叠。中性按钮与链接用落点 ink，强调按钮只换底色与投影；提示图标与文字同行并对齐首行 |

有退出动画的组件不能立即通过条件渲染移除。`usePanelPresence` 保留退出节点至动画结束，关闭时立即设置 `inert`、`aria-hidden` 并停用选择逻辑。快速反向操作取消旧计时器并从当前状态衔接。

`@` 面板展开时保留编辑器焦点；关闭、选择、搜索分类和分页继续使用原有引用逻辑。搜索和切换分类不重播入场，关闭后迟到的请求不得改写退出中的内容。明确关闭或 Escape 返回编辑器，外部点击及打开其他弹窗时不延迟抢回焦点。

## 会话、输入与产物

- 首页只保留品牌、输入区和可执行的起始操作，不显示重复面包屑、键盘说明或无意义的上下文提示。
- 上传与引用入口合并到 `+` 菜单，输入 `@` 可搜索角色、需求、SOP、运行和仓库；会话主需求仍可关联、更换和解除。
- 回复状态以真实 generation 为依据。“回复完成”不等同于项目或任务完成，未知状态不推断成等待审批。
- 会话侧栏行尾的回复状态参照 Codex：回复进行中显示转圈（`arc-spinner`），回复失败或中断显示感叹号（`alert` / `warning`），在用户离开期间完成、尚未打开查看的回复显示 8px 蓝色圆点；打开该会话后圆点消失，已读的完成回复和手动停止的回复不加标记。已读记录由服务端按用户保存每个会话最后看过的回复（`assistant_session_reads`），列表接口据此返回 `unread`：打开会话、或会话打开期间回复完成时，前端调用 `POST /api/assistant/sessions/{id}/read` 记录（`session-unread.js`），换设备也一致。升级前就已结束的回复不算未读，同事发起、自己未参与的项目会话不显示圆点。
- 回复进行中发送的消息进入排队托盘（参照 Codex）：托盘贴在输入框上方、左右内缩并压在输入框顶边下，不加标题行；每行是排队图标、单行截断的消息和「引导 / 编辑 / 删除」，「引导」打断当前回复立即发送这一条，没有回复在跑时显示为「发送」。停止后托盘顶部提示「已暂停自动发送」并提供继续发送。输入框占位文案不随生成状态变化。
- 参与者按实际发过消息的用户统计，浮层在外部点击、焦点离开或 Escape 时关闭。
- 未开始的首页不显示产物入口。有会话内容后显示图标入口；有新产物生成时自动展开产物抽屉，让用户不必先点开才能看到产物。没有产物、只恢复历史会话，或窄屏下抽屉会覆盖会话时保持原有收起状态，只显示未读提示，不抢走正在阅读的回复。
- 产物保留搜索、预览、源码、复制、下载，以及 Markdown、Mermaid、HTML、SVG、图片、演示文稿和节点交接内容的查看能力。
- 模型切换、项目绑定、需求引用、审批及真实发送沿用已有 API 和权限边界，不用视觉状态替代后端结果。

服务端没有会话时，可使用保存在浏览器中的可编辑本地会话展示完整列表和交互。本地 ID 由 `useLocalConversations` 拦截，不能进入真实会话 API 或 URL；本地回复不冒充真实模型执行，新建对话继续走真实发送流程。本地内容不写入数据库，初始记录不伪造正在运行的回复。

## 知识中心

设置区的知识中心（`#knowledgeView`）采用与 Bot 管理、运行记录相同的 Harness 面板语言：页头、概览指标、下划线标签，标签内容放在面板里。令牌、按钮、徽标与输入框见「设计规范」；覆盖 sliding-tabs 的规则全部限定在 `#knowledgeView` 内。

| 区域 | 约定 |
| --- | --- |
| 版心 | 使用设置区外层容器（最大宽 1200px 居中）；页面底色 `--hui-canvas`，面板为 `--hui-surface` |
| 页头 | `.kb-head`：H1「知识中心」22px/600 + 一句 13px 说明；右侧「打开控制台 ↗」为 34px 白底强边框按钮，未配置地址时隐藏。不放眉标和刷新按钮，进入页面与写操作成功后自动刷新 |
| 概览指标 | 页头与标签之间 4 张指标卡（`#kbMetrics`）：圆角 14px、细边框、极浅阴影，标签 12px、数值 26px/600 等宽数字、脚注 12px 弱化色。依次为连接状态（圆点 + 已接入/未启用/未接入/连接异常/读取失败，节点 `#kbStatus`，完整说明在 `title`）、可用来源（脚注跟随平台默认/本项目自定义）、文档总数（本项目可用且 `status=ready` 来源的 `doc_count` 之和，千分位，脚注为占比最大的来源）、已开启角色 / 待审核（两个数字各是按钮，分别切到角色、审核标签；脚注为 `usage.activity` 记录的最近检索次数）。数据全部来自 `kbRefresh` 已有请求；知识库未启用、来源不可达或配置读取失败时依赖来源的数值显示「—」，计算有除零保护 |
| 标签 | 连接、角色、审核、检索四个下划线标签，选中下划线 2px 强调色。角色显示已开启数，审核显示待处理数（运行候选 + 待审核），0 不显示；切换标签不重新请求数据 |
| 面板 | `.kb-panel`：圆角 14px、细边框、`--hui-panel-shadow`；面板头最小高 52px、内边距 10px 18px，标题 14px/600，右侧为弱化说明或控件，底部分隔线 |
| 列表行 | 最小行高 54px、内边距 12px 18px，行间 `--hui-border-soft`；可点击行悬停为中性浅灰，Enter / 空格触发。角色行首为 28px 灰色图标块 |
| 连接 | `.kb-grid` 双栏（1fr : 1.1fr，间距 16px）：左「连接配置」面板（非管理员时面板头显示「仅平台管理员可修改」，底部操作区为检查结果 + 「检查连接」+「保存」）；右「本项目可用来源」面板，面板头为「跟随平台 / 自定义」分段，行内依次为等宽来源 id、占比细条（相对最大来源的篇数）、篇数、开关；非 ready 来源用状态徽标替代占比条 |
| 审核 | 面板头左侧「待处理 / 已写回 / 已驳回」分段，右侧小号「新建」 |
| 检索 | 面板头是 44px 检索框（聚焦强调色边框 + 淡紫外圈）与来源下拉；面板内为结果或「Agent 最近的检索」，命中行保留标题、小节、两行摘要和引用 |
| 按钮 | 强调色实心 / 白底强边框 / quiet（红字，用于删除）三级。页面主体只有「保存」一个实心按钮，抽屉内主操作另计 |
| 输入与开关 | 输入框 36px、强边框，聚焦强调色边框 + 淡紫外圈；开关 34×20，开启为强调色 |

状态徽标由 `kbBadge(label, tone)` 生成，输出 `.hui-badge`（`data-size="sm"`），颜色只表达真实状态：

| 状态 | 色调 |
| --- | --- |
| 候选、已驳回 | 中性灰 |
| 待审核、索引中、等待索引 | 琥珀 |
| 已写回 | 绿 |
| 来源异常、检索失败 | 红 |

运行时注入详情、审核编辑、运行候选和引用原文共用宽 `min(520px, 94vw)` 的右侧抽屉：约 220ms 滑入滑出，关闭按钮、Escape、点击遮罩均可关闭，关闭后焦点回到触发行；收起时设置 `inert` 与 `aria-hidden`。抽屉标题 16px/600，字段标签为 12.5px 中灰，原文高亮行为淡琥珀底加左侧琥珀细线。

权限与校验保持原语义：非管理员只读；自定义来源至少保留一个；写回前校验来源与 `.md` 路径并二次确认；未开启「审核后写回」时写回按钮禁用。角色开关请求期间禁用，失败回滚并提示。

响应式与动效：宽度 <1080px 时连接双栏上下堆叠、指标卡 2×2；<640px 时指标卡单列、输入行上下堆叠、隐藏来源占比条。减少动态效果时抽屉、开关和标签指示层直接呈现；悬停只改颜色，不改尺寸和位置。

## 运行记录列表

列表页（`#runsView` 非详情态）采用一体化面板：标题行之下只有一个面板，依次是筛选头、列表和分页底栏。进入详情时标题行和面板一起隐藏，详情页布局不受列表样式影响。

| 区域 | 约定 |
| --- | --- |
| 标题行 | 「运行记录」22px/600、并发规则提示图标、实时状态（呼吸灯 + 12px 文字）；靠右是成功率与累计 Token 两个无边框细字指标（标签和说明 12px，数值 15px/600）和 32px 刷新图标按钮 |
| 面板 | 圆角 14px、细边框、极浅阴影；筛选头内边距 10px 12px，底部一条分隔线 |
| 筛选头 | 左侧滑动标签（轨道 34px、按钮 28px、13px 字，带数量）；右侧搜索框 220×34 和 SOP 下拉 170×34（原地模式的 Harness Selector）。宽度 ≤1180px 时搜索和下拉换到第二行，搜索框拉伸 |
| 列表行 | 不设列头；网格依次为图标 28px、标题区（自适应）、状态 104px、进度 148px、耗时 64px、操作 64px，列间距 12px，最小行高 54px，内边距 8px 14px |
| 底栏 | 44px，左侧「共 N 条」，右侧沿用通用分页器 |
| 窄屏 | ≤860px 时指标换到标题下一行，列表只保留图标、标题区和状态 |

标签数量与筛选结果共用 `runMatchesFilter(r, key)`；数量的统计范围是项目范围 + SOP 下拉 + 搜索词，不套用状态筛选本身。数量变化只改 `.sliding-tab-count` 文字和 `is-warn` / `is-danger` / `is-zero` 类，不重建按钮，滑动指示块因此保持对齐。成功率与累计 Token 只受项目范围影响。

状态徽标由 `runStatusBadgeHTML(r)` 生成，列表渲染和 SSE 打补丁都用它整体替换徽标，进度条颜色跟随同一色调。徽标为 `.hui-badge`（`data-size="sm"`，色调由 `runStatusTone` 映射），图形放在 `.hui-badge-glyph`，进行中为 `.hui-lamp`：

| 状态 | 色调 | 图形 |
| --- | --- | --- |
| 进行中 | 蓝 | 呼吸灯 |
| 排队中 | 中性灰 | `clock` |
| 待确认 | 橙 | `user-hold` |
| 已暂停 | 橙 | `pause` |
| 已完成 | 绿 | `check` |
| 失败 | 红 | `alert` |
| 已取消 | 中性灰 | `close` |
| 自由协作 · 待指令 | 中性灰 | `chat` |

行首图标块为中性灰：SOP 运行用 `workflow`，自由协作（`collaborationMode==='group'`）用 `chat`；自由协作行的进度显示「—」，不显示耗时。查看（`eye`）和删除（`trash`）图标按钮在悬停或键盘聚焦到行时显示，删除悬停变红；不可删除的行不输出删除按钮也不留占位。整行点击或按 Enter 进入详情。列表周期刷新时保留行或按钮上的键盘焦点。

### 节点当前活动

运行详情中，选中节点实际执行时，状态文字旁显示转圈，当前活动文字以约 2.4 秒一轮从左向右扫过高光。标签、模型信息与布局保持静止；实时日志只更新文本与状态类，避免重建元素打断动效。已完成、失败、取消、暂停、等待人工确认及子 SOP 待确认时均为静态。明暗主题沿用 Harness 文字令牌，长活动文字省略；减少动态效果或强制颜色模式下关闭两种动画，保留可读文字与静态状态图形。

## Bot 管理

Bot 管理页（`#botsView`）按需求列表与运行记录列表的 Harness 先例绘制，接口、权限、三步引导和表单字段不变。令牌与共享组件见「设计规范」；动效只引用 `--bot-*` 时长与曲线令牌，曲线不在令牌以外重复声明。

| 区域 | 约定 |
| --- | --- |
| 页头 | `.page-heading`：H1 22px/600 + 一句说明，无眉标；右侧「添加 Bot」（`#botNew`）是整页唯一的实心强调色按钮，无创建权限、忙碌或加载中时禁用，列表为空或处于编辑/新建态时隐藏（编辑器自带主按钮，也避免丢弃未保存的修改） |
| 面板 | 页头下只有一个面板：圆角 14px、细边框、极浅阴影；面板头内边距 10px 12px，底部分隔线 |
| 面板头 · 列表/记录 | 左侧滑动标签「机器人 N / 记录」（轨道 34px、按钮 28px、13px，数量 0 不显示）；右侧 32px 刷新图标按钮 |
| 面板头 · 编辑 | 面包屑 `nav[aria-label="位置"]`：「[robot] 机器人 › 名称 / 新建 Bot」，「机器人」返回列表且忙碌时禁用；右侧「删除 Bot」红字红描边，仅已保存且可管理时输出。内部仍为 `botUI.tab='edit'`，但不作为标签渲染 |
| 列表行 | 行首 28px 灰色 `robot` 图标块；名称 500 + 模型副文字；接收会话前置「群聊 / 单聊」小标签。编辑、删除为 28px 图标按钮（带 `title` 与 `aria-label`），行悬停或键盘聚焦时显示，删除悬停变红；无权限不输出删除。≤680px 隐藏连接健康列，接收会话移到名称下 |
| 编辑器 | 步骤条 22px 圆形序号：当前步强调色实心 + 淡紫光圈，已完成绿色描边 + 勾，未解锁 45% 透明不可点。输入框 36px、强边框、聚焦强调色边框 + 淡紫外圈；「启用此 Bot」「启用对话」为开关样式的原生 checkbox（`role="switch"`）。表单最大宽 820px，底部操作区上方细分隔线，第 3 步保留「完成，返回列表」 |
| 记录 | 面板内子工具行：下划线分段「发送记录 / 对话记录」（`.bot-record-tabs` 仍注册 sliding-tabs，指示层为 2px 强调色下划线），右侧弱化「保留最近 100 条」。「重试」为淡紫底紫字行内按钮；对话消息为细边框卡片 |
| 空列表 | 淡紫图标块、16px 标题、三步细边框卡片和「开始添加」，此时它是唯一实心按钮 |

状态徽标由 `botBadge()` 生成，输出 `.hui-badge`（`data-size="sm"`）。进行中状态（等待发送、发送中、连接中、处理中等）显示呼吸灯，其余状态用图形区分，映射在 `botBadgeIcon()`：

| 状态 | 色调 | 图形 |
| --- | --- | --- |
| 已启用、消息权限可用、已发送、已连接、已回复 | 绿 | `check` |
| 已停用、未连接 | 中性灰 | `minus`（内联绘制，Harness 图标集无此图形） |
| 待验证、已过期 | 中性灰 | `clock` |
| 需要重新授权、等待授权 | 琥珀 | `user` |
| 连接异常、发送失败、会话不可用、回复失败 | 红 | `alert` |
| 已取消 | 中性灰 | `close` |

发送状态轮询（`botPatchDeliveryRow`）保留原徽标节点，只替换 `data-tone` 和内部图形，颜色因此可以过渡。

## AI 助手设置

界面上称「AI 助手」，避免和「Bot 管理」里的企业微信机器人混淆。设置页、SOP 建议卡片和子 Agent 面板都用这个名字。

AI 助手页（`#assistantView`，路由 `#/assistant`）在设置导航「管理与记录」下紧跟 Bot 管理，图标为 Harness `sparkles`。页头只有 H1 和一句说明（先讲它是谁，再讲能设置什么）；内容和其他设置页一样铺满 1200px 居中容器，两块 14px 圆角面板：「主动派发」里是「主动派角色」「主动走 SOP」两行（默认关闭，面板说明写明打开后更主动，以及角色直接派、SOP 先确认），左侧标题 + 说明，右侧 `role="switch"` 开关；「自定义规则」是一个可纵向拉伸的输入框，右下角显示「N / 4000」。底部「保存」是页面唯一的实心按钮，没有改动或保存中时禁用；未登录只显示提示。样式在 `web/app/assistant-settings.css`，规则限定在 `#assistantView` 内，只引用 `--hui-*` 令牌。

没有 @ SOP、也没有点名要走 SOP 时，AI 助手自己决定交给 SOP 的事是建议。每次批准由用户在卡片上确认；帮我批准由独立审查决定，未通过时同样变成建议卡片；完全权限下直接启动。角色始终直接派发，不弹卡片。`timing=start` 的建议 3 分钟无人处理就超时，服务端把事情交回 AI 助手自己做，不依赖浏览器还开着。决定通过 `POST /api/assistant/sessions/{id}/proposals` 提交。开关和自定义规则按用户存在 `assistant_preferences`，由 AutoMigrate 建表，接口是 `GET` / `PUT /api/assistant/preferences`。

## 角色定义弹窗

角色定义的新建与编辑共用 `#agentModal`（`web/index.html`，面板在 `web/app/flows.js`、`web/app/agents.js`），样式在 `web/product-v4.css` 的 `#agentModal` 段。

| 区域 | 约定 |
| --- | --- |
| 尺寸 | 编辑态随屏幕缩放：宽取视口 72%（不小于 880px、不大于 1320px），高取视口 84%（不小于 640px、不大于 1000px），且始终不超过视口的 94% / 92%；切换标签页时弹窗不变高也不移位；正文超出时在 `.as-main` 内滚动，任务页的 prompt 输入框撑满剩余高度；≤720px 全屏 |
| 顶部 | 标题 + 一行弱化元信息「CLI · 模型（未选时标「默认」）· 版本 · 版本历史」，由 `renderAgentSummary()` 写入 `#agentRuntimeMeta`；不放状态胶囊 |
| 标签 | 身份 / 任务 / 产物与门禁 / 技能与工具，计数为 0 时隐藏 |
| 身份 | 名称与只读标识同一行（新建时标识在名称下方，可「修改」）；职责为单行输入、最多 160 字并显示计数；CLI、模型、思考强度归在「运行方式」下，三个触发器同为 36px、细边框、chevron。模型下拉首项是「默认 · CLI 默认模型」，不显示「选择模型」占位。模型与思考强度菜单在滚动的正文里展开，打开时按触发器上下剩余空间选方向，最大高度不超过该空间；菜单打开时不显示触发器的提示气泡，标签未被截断时也不挂提示 |
| 任务 | 只有一个角色 prompt 输入框；运行时变量按钮做成贴在输入框顶部的工具条，点击插入到光标处；框下一行弱化小字列出运行时自动附上的内容（角色名、产物数、门禁数、重试轮数），随其他标签页的修改实时更新 |
| 开关 | 共享、自动重试、人工介入、平台 MCP、知识库都用 `.as-switch-row`：左侧标题 + 说明，右侧 `role="switch"` 开关；不用带边框的勾选卡片 |
| 说明文字 | 各标签页不放开头说明句，说明写在字段或分区标题的弱化副文字里 |

## 流程模板卡片

流程模板页（`#sopsView`）的 `#sopCatalog` 里项目、共享、通用三类 SOP 卡片按 Harness UI 绘制；顶部指标卡、工具栏、通用模板复制弹窗不在此范围。样式在 `web/app/sop-catalog.css`，紧跟 `assets-harness.css` 加载，规则限定在 `#sopsView` 与挂到 `<body>` 的 `.sop-action-popover` 内；`--hui-*` 令牌继承自 `harness-ui.css`。

| 区域 | 约定 |
| --- | --- |
| 卡片 | `--hui-surface` 底、细边框、圆角 14px、`--hui-ind-shadow`，最小高 286px；网格 `minmax(330px, 1fr)`，≤1050px 为 280px，≤720px 单列。不再用健康色描边或通用模板蓝色渐变 |
| 头部 | 左侧健康 Status Badge + 12px 弱化说明（成功率或提示），右侧发布状态徽标；通用模板左侧为「通用」，右侧为版本 |
| 主体 | 28px 灰色 `workflow` 图标块（通用为淡紫）+ 16px/600 单行标题；描述 13px 三行截断；KPI 区 `--hui-soft` 底，4 列等宽数字，失败数非零时为红色。通用模板的运行依赖用 success / warn 徽标 |
| 底部 | 细分隔线下为元信息（负责人 · 版本 · 最近运行；共享卡前置「来自 项目」标签，通用卡前置「平台」）、唯一主操作（28px 淡紫底紫字）和「⋯」 |

每张卡只有一个主操作，其余进「⋯」菜单：

| 卡片 | 主操作 | 菜单 |
| --- | --- | --- |
| 项目 SOP | 「打开」（`data-open-sop`，进入设计器） | 共享给其他项目 / 取消共享（`toggleShare`）、红字「删除」（`deleteWorkflow`） |
| 共享 SOP | 「复制到本项目」（`cloneWorkflow`） | 无，不渲染「⋯」 |
| 通用模板 | 「复制到本项目」（`openGenericTemplateModal`）；已有副本时为「查看项目副本」 | 「直接运行」（`openRunModal`）；平台管理员另有「编辑模板」（`openGenericTemplateEditor`） |

整卡（`tabindex=0`、`role=button`）点击或 Enter / 空格：项目与共享 SOP 进入设计器，通用模板打开复制预检弹窗。「⋯」菜单是挂在 `<body>` 的单例浮层（`issue-action-popover sop-action-popover`），复用需求列表的浮层规格；打开时关闭需求页菜单，聚焦首项，方向键 / Home / End 移动，Escape 或 Tab 关闭并把焦点还给「⋯」，外部点击、滚动和窗口缩放关闭；触发器同步 `aria-expanded`。

健康徽标按 `metricHealth().label` 映射（`sopHealthTone()`）：

| 状态 | 色调 | 图形 |
| --- | --- | --- |
| 运行中 | info | 呼吸点 |
| 待审批 | warn | `user-hold` |
| 已闭环 | success | `check` |
| 有阻塞、未闭环 | error | `alert` |
| 从未运行 | neutral | `circle` |

发布状态：已发布 success `check`，草稿 neutral `edit`，已归档 neutral `circle`，通用 accent `layers`。

动效由 `sopCatalogMotion` 控制：

- 入场：新出现的卡片上浮 22px、缩放 .97、模糊 3px 后复原，.55s 弹性曲线，按顺序每张错峰 30ms，延迟上限 300ms。
- 重排：切换范围、健康筛选、排序时，仍在列表中的卡片用 FLIP 从旧位置平滑移到新位置（.58s `--hui-ease-flow`）；被移除的卡片以绝对定位的退出节点淡出约 .18s 后移除，退出节点设 `inert` 与 `aria-hidden`。
- 刷新不重播：显示上下文 = 视图激活次数 + 项目 + 范围 + 健康筛选 + 排序。上下文不变时 `refreshWorkflowMetrics` 等内容刷新只替换内容；入场途中的刷新用负延迟接续，不从头播放。从其他页面切回 `#sopsView` 时重置并重播一次。刷新时保留卡片或按钮上的键盘焦点，已打开的菜单重新挂到新的「⋯」上。
- 悬停只加深阴影（紫调 `0 12px 28px -16px`）并换强边框，显式覆盖 product-v3 / v4 / sops.css 的 `translateY`；键盘焦点为强调色边框 + 3px 淡紫外圈。主操作与「⋯」带 `data-hover-ink-control`，由 `hover-ink.js` 驱动落点 ink。
- 减少动态效果时控制器跳过入场、FLIP 与退出节点，CSS 同时关闭卡片内动画、过渡和呼吸点。

## 悬浮页面反馈

登录用户（非访客）在所有页面右下角看到 44px 圆形反馈按钮，`renderUserBadge()` 调用 `AgentFlowPageFeedback.sync()` 按当前用户挂载或卸载。样式全部使用 `--hui-*` 令牌，类名统一为 `.fb-*`；层级为 110–117，低于对话框（120）。工作台返回按钮出现时按钮整体上移避让。

| 区域 | 约定 |
| --- | --- |
| 卡片 | 宽 384px（≤1000px 时贴边），标题「帮助我们做得更好」、副标题「AI Native团队将在24小时以内快速响应」；只有一个输入框（占位「您的不知所措也是我们的问题」，上限 5000 字），首行非空文字即标题（≤120 字）。底部折叠显示自动附带的页面位置 |
| 图片 | 选择、粘贴、拖入均可，最多 5 张、单张 ≤5MB，仅 PNG / JPEG / GIF / WebP；服务端按内容识别类型，不信任扩展名 |
| 点选元素 | 「点选元素」进入拾取态：顶部提示条 + 高亮框；同源 iframe（工作台）内同样可选，捕获阶段拦截点击，不触发业务逻辑；Escape 退出 |
| 提交 | `multipart/form-data` 发到 `POST /api/page-feedback`，提交人只取服务端会话；401 时保留草稿并提示重新登录。成功后显示完成态约 1.6s 再收起清空 |
| 拖动与隐藏 | 长按 320ms 或移动超过 6px 开始拖动，底部中央出现回收区，进入 72px 半径变为激活态，松开即隐藏并给出可撤销提示。拖动中 Escape 或 `pointercancel` 回弹原位，拖动后的点击一律吞掉 |

隐藏状态只保存在内存，刷新后恢复；组件不读写 localStorage、sessionStorage 或 cookie。URL 与 iframe 地址在上传前去掉 userinfo 与 token、code、signature 等敏感参数，服务端再清洗一次。减少动态效果时取消全部动画与过渡。

## 实现位置

| 位置 | 职责 |
| --- | --- |
| `web/app.js`、`web/app/navigation.css` | 原生外壳、统一设置入口、导航宽度和折叠 |
| `web/app/sidebar-mode-motion.js` | 工作台与设置模式切换的过渡 |
| `chatbot/src/navigation*.css`、`SidebarPreview.jsx` | 会话侧栏、分组、树线及抽屉过渡 |
| `HomeLogo.jsx`、`HomeWelcome.jsx`、`home-*.css` | 透明品牌标记、标题扫光与副标题 |
| `HarnessIcon.jsx`、`harness-icons.css` | React 图标定义和内部动效 |
| `ModelPicker.jsx`、`ModelProviderIcon.jsx` | 模型菜单、品牌标识和内部光效 |
| `MentionPicker.jsx`、`mention-context.css` | `@` 引用搜索、选择和展开收起 |
| `ArtifactPanel.jsx`、`useArtifactPanelLayout.js`、`artifact-*.css` | 产物列表、预览和响应式抽屉 |
| `usePanelPresence.js` | 双向动效的挂载、退出和快速反向控制 |
| `useLocalConversations.js`、`session-status.js`、`session-unread.js` | 本地会话隔离、真实回复状态与未读标记展示 |
| `web/app/hover-ink.*`、`web/app/sliding-tabs.*` | 两套页面共用的悬停和标签页行为 |
| `web/app/harness-ui.css` | 唯一设计规范：`--hui-*` 令牌、按钮、Status Badge、面板、输入、Selector 与菜单、设置区旧令牌映射和 1200px 外层容器 |
| `web/app/issues-list.css` | 需求列表页独有的操作列、看板与响应式样式 |
| `web/app/approvals.js`、`web/app/approvals-harness.css` | 待我确认页的卡片外壳（`approvalCardHTML`）、状态标签、⋯ 菜单交互与 Harness 样式 |
| `web/app/hui-select.js` | Harness Selector（原地与触发器两种模式），以及共用的 `HuiPopover`（浮层互斥与定位）和 `HuiAuto`（自动增强与移除清理） |
| `web/app/business-domains.js`、`web/app/business-harness.css` | 业务领域报表页的模板（`businessReportHTML`）、图表 option（`businessDeliveryOption` 等）、`biz-` 图表挂载与释放、静态 SVG 导出与 Harness 样式 |
| `web/knowledge.js`、`web/knowledge.css` | 知识中心的概览指标（`kbMetricsHTML`）、四个标签的面板与行模板（`kbPanel`、`kbRow`、`kbBadge`）、通用右侧抽屉 |
| `web/app/runs.js`、`web/product-v4.css`（运行记录列表区段） | 运行记录列表的面板、标签计数、状态徽标和行模板 |
| `web/app.js`（视图切换）、`web/product-v4.css`（`.page-context`） | 按视图是否自带页头切换 `body.view-has-heading`，隐藏重复的顶栏标题 |
| `web/app/assistant-settings.js`、`web/app/assistant-settings.css` | AI 助手设置页的开关、自定义规则与保存 |
| `web/app/bots.js`、`web/bots.css` | Bot 管理页的面板头（标签 / 面包屑）、列表行、状态徽标、引导编辑器与记录 |
| `web/app/hui-controls.css` | Selector 面板，复选 / 单选 / 开关，确认对话框，日期浮层，上传区和 Tooltip 样式 |
| `web/app/hui-date.js`、`hui-upload.js`、`hui-tooltip.js` | 日期浮层、上传拖拽区、`title` 接管的 Tooltip |
| `web/app.js`（`afOpenDialog`、`afConfirm`、`afPrompt`、`afAlert`） | 确认对话框的排队、焦点管理和模板 |
| `chatbot/src/HarnessSelect.jsx`、`harness-select-model.js`、`harness-controls.css` | 工作台的 React 下拉组件、可测试的选项 / 键盘 / 定位逻辑及样式 |
| `web/app/sops.js`、`web/app/sop-catalog.css` | 流程模板卡片模板（`projectSOPCard`、`sharedSOPCard`、`genericSOPCard`）、徽标映射、「⋯」菜单浮层、`sopCatalogMotion` 入场 / FLIP / 退出动效 |
| `web/app/page-feedback.js`、`web/app/page-feedback.css`、`internal/httpapi/page_feedback.go`、`internal/repo/page_feedback.go` | 悬浮反馈按钮、卡片、元素拾取与拖动隐藏；反馈提交 / 查询接口及 `page_feedbacks`、`page_feedback_images` 表 |

## 验证与交付

修改后执行与改动对应的检查：

```bash
npm --prefix chatbot test
npm --prefix chatbot run build
GOCACHE=/tmp/agentflow-gocache go test ./web -run 'TestAppBundleFilesAreEmbeddedAndListed|TestAppJSParses' -count=1
# 表单控件、对话框、Tooltip（node 假 DOM：web/hui_*_test.js + web/hui_fake_dom.js）
GOCACHE=/tmp/agentflow-gocache go test ./web -run 'Hui|NoNativeDialogs' -count=1
```

服务端嵌入前端资源，发布时先构建 `chatbot/`，再编译服务端；完整构建入口为 `make build`。只更新源码而未重新构建嵌入资源，不会更新已运行页面。

浏览器验收覆盖桌面与窄屏、明暗主题、减少动态效果、快速展开收起、键盘和外部点击、软键盘下的浮层定位，以及动画期间的布局稳定性。涉及发送和平台写操作的验收使用拦截或隔离环境，检查真实请求参数与权限，避免通过伪造状态掩盖功能问题。

提交源码、资源来源和许可证；`web/native-chat/` 构建输出、`node_modules/`、`state/`、运行日志、凭据以及参考视频不纳入提交。
