import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';
import { WEB_ROOT } from '../lib/load-app.mjs';

const HAN = /[㐀-鿿]/;

// The page descriptions, empty states and hints that used to stay Chinese in
// English mode (Runs, Projects, Sandboxes, Knowledge, AI schedules, Assistant,
// Access keys, Approvals and Requirements).
const COPY = [
  '平台所有项目共用执行名额，默认 10 条顶层 LOOP，平台管理员可在「执行机器」调整。个人和共享机器按各自上限计数。',
  '超出名额后按排队顺序启动。开启自动分配的 LOOP 等待兼容的执行机器，子 LOOP 共用父流程名额。',
  '失败、暂停、待确认的 LOOP 空闲 1 小时后自动归档工作区并释放名额，继续或重试时自动恢复。已开始的 LOOP 取消后，工作区清理完成即释放名额。',
  '暂无进行中的运行',
  '换个关键词，或到需求列表查看对应需求的执行结果。',
  '项目是需求、仓库、团队和默认 LOOP 的统一边界。点击项目进入详情配置。',
  '我参与的', '待完善', '未绑定代码仓库',
  '维护当前项目的运行沙箱：隔离方式、Dockerfile 和环境变量。',
  '本项目沙箱',
  '沙箱决定 LOOP 节点和对话工作区在什么隔离环境里执行：docker 沙箱自带 Dockerfile，native 沙箱使用执行机器上已安装的工具。建好后在流程设置里选用；也可以直接用「共享」里的现成配置。',
  '接入并管理仅你本人可见的外部只读知识库连接器，供 AI 对话与 SOP 运行按需引用。',
  '个人隔离', '只读安全代理', '还没有接入个人知识库连接器',
  '可从上方常用知识库模板快速配置，或选择一个连接器类型接入你的自有知识库；配置仅对你本人可见，可在 AI 对话、SOP 节点与手动运行中引用。',
  '共享任务市场', '执行记录',
  '任务以最后保存人的身份和完全权限执行。关闭页面后，任务会继续运行。',
  '当前项目还没有 AI 定时任务，点击右上角「新建 AI 定时任务」。',
  'AI 工作台里和你对话、帮你动手的助手。设置消息发送方式、角色与 LOOP 的主动派发，以及每次对话都要遵守的规则；只对你自己生效。',
  '消息发送', '生成中发送消息',
  '排队会等当前回复结束；立即发送会打断当前回复，保留已输出内容并继续处理新消息。',
  '还没有访问密钥，创建后即可接入 REST API 和 MCP。',
  '没有需要你拍板的需求', '流程停在人工确认节点时会出现在这里。',
  '批量操作',
];

async function englishWindow() {
  const dom = new JSDOM('<!doctype html><html data-lang="en"><body></body></html>', {
    runScripts: 'outside-only', url: 'http://127.0.0.1/',
  });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file })
      .runInContext(dom.getInternalVMContext());
  }
  return dom;
}

test('English mode translates the page copy and empty states of every tested page', async () => {
  const dom = await englishWindow();
  const window = dom.window;
  const untranslated = COPY.filter(zh => HAN.test(window.lfTranslate(zh)));
  assert.deepEqual(untranslated, []);
  assert.equal(window.lfTranslate('暂无进行中的运行'), 'No LOOPs in progress');
  assert.equal(window.lfTranslate('批量操作'), 'Bulk actions');
  assert.equal(window.lfTranslate('我参与的'), "I'm in");
  window.close();
});

test('the new page copy leaves user-authored names alone', async () => {
  const dom = await englishWindow();
  const window = dom.window;
  // A person's Chinese display name is data, not UI copy; the dictionary must
  // not claim it, so a whole-string lookup leaves it unchanged.
  assert.equal(window.lfTranslate('走查账号'), '走查账号');
  assert.equal(window.LF_I18N_EN['走查账号'], undefined);
  window.close();
});

test('English mode translates copy split by inline markup as one sentence', async () => {
  const dom = await englishWindow();
  const { window } = dom;
  const { document } = window;
  document.body.innerHTML = `
    <div id="tip"><b>平台所有项目共用执行名额</b>，默认 10 条顶层 LOOP，平台管理员可在「执行机器」调整。个人和共享机器按各自上限计数。</div>
    <p id="split">打开后 AI 助手会更主动地把事情交给角色或 LOOP。<strong><em>派角色不需要确认</em></strong>；<span>走 LOOP 在「请求批准」下先征求同意，「帮我批准」下由 AI 审查直接裁决，「完全权限」下直接启动。</span></p>
    <div id="data" class="issue-title-cell"><b>UI 走查：parse_amount 应拒绝 "1.2.3" 这类非法金额。</b></div>`;
  const tip = document.getElementById('tip');
  const split = document.getElementById('split');
  // The initial pass ran on an empty body, so re-inserting the nodes replays the
  // same MutationObserver path the app uses for asynchronously rendered views.
  document.body.appendChild(tip);
  document.body.appendChild(split);
  await new Promise(r => setTimeout(r, 20));
  assert.ok(!/[㐀-鿿]/.test(tip.textContent), `tip still Chinese: ${tip.textContent}`);
  assert.ok(!/[㐀-鿿]/.test(split.textContent), `split still Chinese: ${split.textContent}`);
  assert.match(split.textContent, /hands work to roles or LOOPs more eagerly/);
  assert.match(document.getElementById('data').textContent, /UI 走查/);
  window.close();
});
