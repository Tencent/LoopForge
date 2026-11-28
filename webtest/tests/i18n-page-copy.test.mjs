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
  '批量操作', '暂无项目说明',
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

// The merge pass must not fold independent, individually styled siblings into a
// single text node: grid/flex children carry the layout, and the class names go
// away with them. Regression for the review on head 6ea4b3ff (PR #1530).
async function render(html, lang = 'en') {
  const dom = new JSDOM(`<!doctype html><html data-lang="${lang}"><body></body></html>`, {
    runScripts: 'outside-only', url: 'http://127.0.0.1/',
  });
  for (const file of ['i18n-en.js', 'i18n.js']) {
    new vm.Script(await readFile(path.join(WEB_ROOT, file), 'utf8'), { filename: file })
      .runInContext(dom.getInternalVMContext());
  }
  const host = dom.window.document.createElement('div');
  host.innerHTML = html;
  dom.window.document.body.appendChild(host);
  await new Promise(r => setTimeout(r, 30));
  return dom;
}

test('English mode keeps the requirement table header as ten aligned cells', async () => {
  const head = '<span>编号</span><span>标题</span><span>来源</span><span>责任人</span><span>仓库</span>'
    + '<span>优先级</span><span>关联 LOOP</span><span>当前环节</span><span>进度</span><span>操作</span>';
  const dom = await render(`<div id="issueBoard"><div class="issue-table"><div class="issue-table-row issue-table-head" role="row">${head}</div></div></div>`);
  const cells = dom.window.document.querySelectorAll('#issueBoard .issue-table-head > span');
  const text = dom.window.document.querySelector('#issueBoard .issue-table-head').textContent;
  assert.equal(cells.length, 10, `header collapsed into: ${JSON.stringify(text)}`);
  assert.deepEqual([...cells].map(c => c.textContent), ['ID', 'Title', 'Source', 'Assignee', 'Repository', 'Priority', 'Linked LOOP', 'Current stage', 'Progress', 'Actions']);
  assert.equal(dom.window.document.querySelector('#issueBoard .issue-table-head').className, 'issue-table-row issue-table-head');
  dom.window.close();
});

test('English mode keeps styled sibling blocks on their own lines', async () => {
  const dom = await render(`
    <div id="metric" class="platform-metric-info">
      <span class="platform-metric-label">平台管理员</span>
      <b class="platform-metric-num">--</b>
      <small>具有跨项目通用模板与角色编辑权</small>
    </div>
    <div id="user" class="upp-name-row">
      <span class="upp-name">李四</span>
      <span class="upp-badge-tag is-guest">未认证</span>
    </div>
    <div id="row" class="upp-info-item">
      <span class="upp-label">显示名称</span>
      <span class="upp-value">李四</span>
    </div>
    <div id="sec" class="flow-settings-sec-head"><span class="sec-dot"></span><b>LOOP 通知</b><small>选一个 Bot，自动接收提醒与完成汇报</small></div>
    <label id="label" class="flow-field-label">流程名称 <span class="required-badge">必填</span></label>
  `);
  const d = dom.window.document;
  const metric = d.getElementById('metric');
  assert.deepEqual([...metric.children].map(c => c.className), ['platform-metric-label', 'platform-metric-num', '']);
  assert.equal(metric.querySelector('.platform-metric-label').textContent, 'Platform admin');
  assert.equal(metric.querySelector('small').textContent, 'Can edit cross-project templates and roles');

  const user = d.getElementById('user');
  assert.equal(user.querySelectorAll('span').length, 2, `name row collapsed into: ${user.innerHTML}`);
  assert.equal(user.querySelector('.upp-name').textContent, '李四');
  assert.ok(user.querySelector('.upp-badge-tag'), `badge lost: ${user.innerHTML}`);

  const row = d.getElementById('row');
  assert.equal(row.querySelectorAll('span').length, 2, `info row collapsed into: ${row.innerHTML}`);
  assert.equal(row.querySelector('.upp-label').textContent, 'Display name');
  assert.equal(row.querySelector('.upp-value').textContent, '李四');

  const sec = d.getElementById('sec');
  assert.equal(sec.children.length, 3, `section head collapsed into: ${sec.innerHTML}`);
  assert.equal(sec.querySelector('b').textContent, 'LOOP 通知');
  assert.ok(sec.querySelector('span.sec-dot'));

  const label = d.getElementById('label');
  assert.ok(label.querySelector('.required-badge'), `required badge lost: ${label.innerHTML}`);
  assert.equal(label.textContent, 'LOOP name Required');
  dom.window.close();
});

test('Chinese mode leaves the requirement table header untouched', async () => {
  const head = '<span>编号</span><span>标题</span><span>来源</span><span>责任人</span>';
  const dom = await render(`<div class="issue-table-row issue-table-head">${head}</div>`, 'zh');
  const el = dom.window.document.querySelector('.issue-table-head');
  assert.equal(el.children.length, 4);
  assert.equal(el.textContent, '编号标题来源责任人');
  dom.window.close();
});

test('English mode never stitches a user-named project into UI copy', async () => {
  const dom = await render(`
    <div id="card" class="project-card">
      <div class="project-name">测试项目</div>
      <span class="project-badge">当前项目</span>
    </div>
    <div id="row" class="credential-row"><span class="cred-label">标题</span><span class="cred-value">生产环境</span></div>
  `);
  const d = dom.window.document;
  assert.equal(d.querySelector('#card .project-name').textContent, '测试项目');
  assert.equal(d.querySelector('#card .project-badge').textContent, 'Current project');
  assert.equal(d.querySelectorAll('#row > span').length, 2);
  assert.equal(d.querySelector('#row .cred-label').textContent, 'Title');
  assert.equal(d.querySelector('#row .cred-value').textContent, '生产环境');
  dom.window.close();
});
