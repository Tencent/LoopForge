import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[㐀-鿿「」，。：（）、；！？]/;

// A page in English mode: i18n.js rewrites the Chinese copy with i18n-en.js.
async function englishPage() {
  const dom = new JSDOM('<html data-lang="en"><body></body></html>', { runScripts: 'outside-only', url: 'http://127.0.0.1/' });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file }).runInContext(dom.getInternalVMContext());
  }
  return dom.window;
}

// The copy these pages render in Chinese; every one must come out in English.
const COPY = {
  knowledge: [
    '接入并管理仅你本人可见的外部只读知识库连接器，供 AI 对话与 SOP 运行按需引用。',
    '个人隔离', '：连接器与凭据仅归属当前账号，其他成员无法查看或调用。',
    '只读安全代理', '：密钥 AES-GCM 加密托管，沙箱通过无状态代理检索并标注出处，支持按工具白名单精准放行只读工具。',
    '常用知识库 · 快速接入', '快速配置', '还没有接入个人知识库连接器', '接入此类型',
    '可从上方常用知识库模板快速配置，或选择一个连接器类型接入你的自有知识库；配置仅对你本人可见，可在 AI 对话、SOP 节点与手动运行中引用。',
  ],
  roles: ['未填写职责'],
  bots: [
    '机器人', '记录', '开始添加', '① 填写凭据', '名称、Bot ID 与 Secret', '② 选择接收会话',
    '绑定单聊或群聊', '③ 发送测试', '确认消息能送达',
    '还没有机器人。三步完成配置，随后在 LOOP 设置或 AI 定时任务中选择这个 Bot。',
  ],
  aiSchedules: [
    '共享任务市场', '执行记录',
    '任务以最后保存人的身份和完全权限执行。关闭页面后，任务会继续运行。',
    '当前项目还没有 AI 定时任务，点击右上角「新建 AI 定时任务」。',
  ],
  observability: ['总览'],
  evolution: ['当前项目暂无 LOOP', '创建并运行 LOOP 后，这里会按 LOOP 持续统计核心消耗与评分，并按偏好自动沉淀优化建议。', '监测中', '优化中', '灰度中', 'LOOP 与最近分析', '优化偏好 · 周期', '近批指标（Token / 工具 / 评分）', '自动进化', '省 Token', '省工具',
    '最近 1 次 · 一次成功，未发现异常', '最近 1 次 · 节点打回返工、评测得分偏低'],
  projects: [
    '项目是需求、仓库、团队和默认 LOOP 的统一边界。点击项目进入详情配置。',
    '我参与的', '待完善', '暂无项目说明', '未绑定代码仓库',
  ],
  settings: [
    'GitHub 授权 / 个人访问令牌', '权限', '校验后显示', '最近校验', '连接 GitHub', '删除凭证', '手动更换令牌',
    '在 GitHub 确认授权后，即可选择你的仓库。无需复制令牌。', '授权仅用于当前账号，可随时撤销。',
    '新的个人访问令牌', '保存前会校验有效性与权限', '保存后替换当前的令牌，对下一次任务生效',
    '已配置 · 留空则不改', '输入新值以替换', '官方地址使用', '可选 · 留空使用网络环境的官方地址', '留空使用官方地址',
    '模型目录', '未自定义', '获取可用模型', '添加模型', '手动输入', '接口协议', 'Base URL 提供的 API', '可选强度',
    '跟随服务发布的模型', '清空目录，改回跟随服务发布的模型', '清空自定义列表',
    'CodeBuddy 会使用可用的共享配置。其他 CLI 继续使用各自的凭据。',
    '依赖私有仓库的 LOOP 将无法克隆或推送仓库。',
    '使用 CodeBuddy 自带的模型',
    '目录为空时，AI 对话列出 Cursor CLI 支持的模型。获取可用模型可以改成自己的目录，也可以手动添加。',
    'AI 对话将不能再切换到 Pi，以 Pi 运行的 LOOP 节点将无法执行，Base URL、协议与模型目录一并清空。',
  ],
};

test('English mode translates the copy on knowledge, bots, schedules, evolution, projects and settings', async (t) => {
  const window = await englishPage();
  t.after(() => window.close());
  for (const [page, strings] of Object.entries(COPY)) {
    for (const text of strings) {
      assert.doesNotMatch(window.lfTranslate(text), HAN, `${page}: ${text}`);
    }
  }
  assert.equal(window.lfTranslate('总览'), 'Overview');
  assert.equal(window.lfTranslate('还没有接入个人知识库连接器'), 'No personal knowledge connectors yet');
  assert.equal(window.lfTranslate('已配置 · 留空则不改'), 'Configured · leave blank to keep');
  assert.equal(window.lfTranslate('最近 1 次 · 一次成功，未发现异常'), 'Last run · all succeeded, no anomalies');
  assert.equal(window.lfTranslate('最近 1 次 · 节点打回返工、评测得分偏低'), 'Last run · Node rework, Low evaluation score');
  assert.equal(window.lfTranslate('可选强度'), 'Effort (optional)');
});

test('English mode pluralises the counts these pages render', async (t) => {
  const window = await englishPage();
  t.after(() => window.close());
  assert.equal(window.lfTranslate('0 个任务'), '0 tasks');
  assert.equal(window.lfTranslate('1 个任务'), '1 task');
  assert.equal(window.lfTranslate('1 个模型'), '1 model');
  assert.equal(window.lfTranslate('3 个自定义模型'), '3 custom models');
  assert.equal(window.lfTranslate('每 10 次'), 'Every 10 runs');
  assert.equal(window.lfTranslate('每 1 次'), 'Every 1 run');
  // The "共 N 个" rule must not eat the counter: the noun keeps its own plural.
  assert.equal(window.lfTranslate('共 3 个任务'), '3 tasks');
  assert.equal(window.lfTranslate('共 1 个任务'), '1 task');
  assert.equal(window.lfTranslate('共 2 个模型'), '2 models');
});
