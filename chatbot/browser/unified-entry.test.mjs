// Uses deterministic API fixtures with either the Go service (CI) or a local
// static server. Run after `npm run build`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';
import { sampleBootstrap } from '../../webtest/lib/load-app.mjs';

const root = process.env.LOOPFORGE_BROWSER_WEB_ROOT || path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../web');
const types = { '.html':'text/html', '.css':'text/css', '.js':'application/javascript', '.svg':'image/svg+xml', '.png':'image/png' };
const entryRequest = /\/chatbot\/assets\/(?:workbench|(?:main|index)-[\w-]+)\.js(?:\?|$)/;
const styleRequest = /\/chatbot\/assets\/[^/]+\.css(?:\?|$)/;
const project = {id:'project-2',name:'新建需求',status:'active'};
const bootstrap = () => {
  const data = sampleBootstrap();
  data.projects.push(project);
  return data;
};

async function withPage(run, options = {}) {
  const server = process.env.LOOPFORGE_BROWSER_BASE ? null : http.createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    const relative = pathname === '/' ? 'index.html' : pathname.startsWith('/chatbot/') ? 'native-chat/' + pathname.slice(9) : pathname.slice(1);
    const file = path.resolve(root, relative);
    if (!file.startsWith(root + path.sep)) { res.writeHead(403).end(); return; }
    try {
      let data = await readFile(file);
      if (relative === 'index.html' && data.includes('<!-- loopforge-workbench-assets -->')) {
        const index = await readFile(path.join(root, 'native-chat/index.html'), 'utf8');
        const script = index.match(/<script type="module"[^>]* src="([^"]+)"/)[1];
        const style = index.match(/<link rel="stylesheet"[^>]* href="([^"]+)"/)[1];
        data = data.toString().replace('<!-- loopforge-workbench-assets -->', `<link rel="preload" as="style" href="${style}" data-workbench-style><link rel="modulepreload" href="${script}" data-workbench-entry>`);
      }
      res.writeHead(200, { 'content-type': types[path.extname(file)] || 'application/octet-stream' }); res.end(data);
    }
    catch { res.writeHead(404).end(); }
  });
  if (server) await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless:true, channel:'chromium', args:['--no-sandbox'] });
    const page = await browser.newPage({ viewport:{width:1440,height:900}, ...options });
    page.setDefaultTimeout(8000);
    const errors=[];
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => {
      window.EventSource = class { close() {} addEventListener() {} removeEventListener() {} };
      window.WebSocket = class { close() {} addEventListener() {} removeEventListener() {} send() {} };
    });
    await page.route('**/api/**', async route => {
      const url = new URL(route.request().url()), pathname=url.pathname;
      const user={id:1,name:'Test User',username:'tester',isGuest:false,isPlatformAdmin:true,gitHubCredentialConfigured:true};
      let body=[];
      if(pathname==='/api/auth/current') body={user,authRequired:false};
      else if(pathname==='/api/bootstrap') body=bootstrap();
      else if(pathname==='/api/issues' && route.request().method()==='POST') body={...route.request().postDataJSON(),id:'issue-browser-test'};
      else if(pathname==='/api/projects') body=bootstrap().projects;
      else if(pathname==='/api/users') body=[user];
      else if(pathname==='/api/issues/issue-browser-test') body={id:'issue-browser-test',title:'需求转交测试',projectId:'project-1'};
      else if(pathname==='/api/assistant/references') body={items:[{type:'sop',id:'sop-browser-test',name:'平台验收 · 脚本与审批',projectId:'project-1'}]};
      else if(pathname==='/api/assistant/models') body=url.searchParams.has('schedule') ? {
        models:[{id:'glm-test',name:'GLM Test'}], engines:[{id:'codebuddy',name:'CodeBuddy',configured:true}], default:'glm-test',
      } : {models:[],default:''};
      else if(pathname==='/api/assistant/sessions') body=[{id:'s-2',projectId:'project-2',title:'新建需求'}];
      else if(pathname==='/api/assistant/sessions/s-2') body={id:'s-2',projectId:'project-2',title:'新建需求',messages:[]};
      else if(pathname==='/api/assistant/sessions/s-1') body={id:'s-1',projectId:'',title:'浏览器测试会话',messages:[
        {role:'user',content:'新建需求',references:[{type:'issue',id:'issue-1',name:'新建需求'}]},
        {role:'assistant',content:'**新建需求**'},
      ]};
      else if(pathname==='/api/assistant/sessions/preview') body={id:'preview',projectId:'',title:'预览验收',messages:[{role:'assistant',content:JSON.stringify({message:'已准备\n\n```slides title=验收演示\n# 新建需求\n\n新建需求\n```',drafts:[{
        tool:'workflows_create',body:{name:'新建需求',description:'新建需求',nodes:[{id:'node-test',name:'新建需求',type:'approval',taskPrompt:'新建需求'}]},
      }]})}]};
      else if(pathname==='/api/runners/default') body={runnerId:''};
      else if(pathname==='/api/auth/local') body={enabled:false};
      else if(pathname==='/api/capabilities') body={};
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(body)});
    });
    const base = process.env.LOOPFORGE_BROWSER_BASE || `http://127.0.0.1:${server.address().port}`;
    await run({page, base, errors, composer: page.locator('#chatRoot .composer-column textarea')});
  } finally {
    await browser?.close();
    if (server) await new Promise(resolve=>server.close(resolve));
  }
}

for (const failure of ['slow', '404']) {
  test(`shell survives workbench stylesheet ${failure}`, () => withPage(async ({page, base, composer}) => {
    let release;
    const held = new Promise(resolve => { release=resolve; });
    let requested = false;
    await page.route(styleRequest, async route => {
      requested = true;
      if (failure === 'slow') await held;
      await route.fulfill({status:404});
    });
    try {
      await page.goto(`${base}/#/settings`, {waitUntil:'commit'});
      await page.locator('#settingsView.active').waitFor({timeout:5000});
      await page.locator('#bootLoader').waitFor({state:'detached'});
      assert.ok(requested);
      await page.locator('#railSettingsEntry').click();
      const retry = page.getByRole('button', {name:'重新加载工作台'});
      await retry.waitFor({timeout:12000});
      assert.equal(await composer.count(), 0);
      assert.equal(await page.locator('#collaborationView [aria-busy=true]:visible').count(), 0);
      release();
      await page.unroute(styleRequest);
      await retry.click();
      await composer.waitFor();
    } finally { release(); }
  }));
}

test('RSI deep link stays in settings, follows project changes and returns to workbench', () => withPage(async ({page, base}) => {
  await page.goto(`${base}/?projectId=project-1#/evolution`);
  await page.locator('#evolutionView.active').waitFor();
  assert.equal(await page.locator('body.management-mode').count(), 1);
  const item = page.locator('#settingsSidebar [data-view=evolution]');
  assert.ok(await item.isVisible());
  assert.ok(await item.evaluate(el => el.classList.contains('active')));
  await page.getByRole('button', {name:'切换项目',exact:true}).click();
  await Promise.all([
    page.waitForRequest('**/api/evolution/overview?projectId=project-2'),
    page.getByRole('option', {name:/新建需求/}).click(),
  ]);
  assert.ok(await item.evaluate(el => el.classList.contains('active')));
  await page.locator('#railSettingsEntry').click();
  await page.locator('#collaborationView.active').waitFor();
  await page.waitForURL('**#/collaboration');
}));

for (const failure of ['entry', 'dependency']) {
  test(`shell survives workbench ${failure} failure`, () => withPage(async ({page, base}) => {
    let intercepted = 0;
    let bootstraps = 0;
    page.on('request', request => { if (new URL(request.url()).pathname === '/api/bootstrap') bootstraps++; });
    const pattern = failure === 'entry' ? entryRequest : '**/chatbot/assets/ReferenceInput-*.js';
    await page.route(pattern, route => {
      intercepted++;
      return failure === 'entry' ? route.abort() : route.fulfill({status:404});
    });
    await page.goto(`${base}/#/settings`);
    await page.locator('#settingsView.active').waitFor({timeout:5000});
    await page.locator('#bootLoader').waitFor({state:'detached'});
    assert.equal(bootstraps, 1);
    assert.ok(intercepted > 0);
    assert.equal(await page.getByRole('button', {name:'重新加载工作台'}).isVisible(), false);
    await page.locator('#railSettingsEntry').click();
    await page.getByRole('button', {name:'重新加载工作台'}).waitFor();
    assert.equal(await page.locator('#collaborationView [aria-busy=true]:visible').count(), 0);
    await page.unroute(pattern);
    await page.getByRole('button', {name:'重新加载工作台'}).click();
    await page.locator('#chatRoot .composer-column textarea').waitFor();
  }));
}

test('bare collaboration route restores the same session independent of origin', () => withPage(async ({page, base}) => {
  const url = `${base}/?chatSession=s-1#/collaboration`;
  await page.goto(url);
  await page.getByText('浏览器测试会话').first().waitFor({timeout:7000});
  assert.equal(page.url(), url);
  for (const source of ['#/collaboration/s-1', '#/settings']) {
    await page.evaluate(hash => { location.hash = hash; }, source);
    await page.waitForURL(`**${source}`);
    await page.evaluate(() => { location.hash = '#/collaboration'; });
    await page.locator('#collaborationView.active').waitFor();
    await page.getByText('浏览器测试会话').first().waitFor({timeout:5000});
    assert.equal(page.url(), url);
    await page.goBack();
    await page.waitForURL(`**${source}`);
    await page.goForward();
    await page.waitForURL(url);
    await page.getByText('浏览器测试会话').first().waitFor();
  }
}));

test('hashless workbench URLs and different sessions survive history and explicit returns', () => withPage(async ({page, base, composer}) => {
  const assertSession = async (view, id) => {
    await page.locator(`#${view}View.active`).waitFor();
    await page.waitForFunction(session => window.loopforgePlatform.getSnapshot().session === session, id);
  };
  await page.goto(`${base}/`);
  await composer.waitFor();
  assert.equal(page.url(), `${base}/`);
  await assertSession('collaboration', '');
  await page.goto(`${base}/?chatSession=s-1`);
  await assertSession('collaboration', 's-1');
  assert.equal(page.url(), `${base}/?chatSession=s-1`);
  await page.evaluate(() => { location.hash = '#/settings'; });
  await assertSession('settings', 's-1');
  await page.evaluate(() => { location.hash = '#/collaboration/s-2'; });
  await assertSession('collaboration', 's-2');
  await page.goBack();
  await assertSession('settings', 's-2');
  await page.goBack();
  await assertSession('collaboration', 's-1');
  assert.equal(page.url(), `${base}/?chatSession=s-1`);
  await page.goForward();
  await page.locator('#settingsView.active').waitFor();
  await page.goForward();
  await assertSession('collaboration', 's-2');
  assert.equal(page.url(), `${base}/?chatSession=s-1#/collaboration/s-2`);

  // Hash-only sessions leave no query parameter for a return action to guess from.
  await page.goto(`${base}/#/collaboration/s-2`);
  await assertSession('collaboration', 's-2');
  for (const via of ['settings button', 'command search']) {
    await page.locator('#railSettingsEntry').click();
    await page.locator('#settingsView.active').waitFor();
    if (via === 'settings button') await page.locator('#railSettingsEntry').click();
    else {
      await page.keyboard.press('Control+k');
      await page.locator('#commandSearch').fill('AI 工作台');
      await page.keyboard.press('Enter');
    }
    await assertSession('collaboration', 's-2');
    assert.equal(page.url(), `${base}/#/collaboration/s-2`, via);
  }
}));

test('production prompt editor retains Shadow DOM styles and mention keyboard behavior', () => withPage(async ({page, base}) => {
  await page.goto(`${base}/#/aiSchedules`);
  await page.locator('#newAISchedule').click();
  const host = page.locator('#schedulePromptEditor');
  const input = host.locator('textarea');
  await input.waitFor();
  assert.deepEqual(await input.evaluate(el => [getComputedStyle(el).minHeight, getComputedStyle(el.closest('.composer')).position]), ['112px','relative']);
  await input.fill('@');
  const group = host.getByRole('option', {name:/LOOP/});
  await group.waitFor();
  assert.equal(await host.locator('.mention-picker').evaluate(el => getComputedStyle(el).position), 'absolute');
  await group.click();
  await host.getByRole('option', {name:/平台验收/}).click();
  assert.match(await input.inputValue(), /@平台验收/);
  const trigger = host.getByRole('button', {name:/引用项目资产/});
  await trigger.click();
  await page.keyboard.press('Escape');
  await host.locator('.mention-picker').waitFor({state:'hidden'});
  assert.equal(await trigger.evaluate(el => el.getRootNode().activeElement === el), true);
  assert.equal(await page.locator('#scheduleModal').evaluate(el=>el.classList.contains('open')), true);
}));

test('Escape closes only the top schedule model menu before the editor', () => withPage(async ({page, base}) => {
  await page.goto(`${base}/#/aiSchedules`);
  const opener = page.locator('#newAISchedule');
  await opener.click();
  const modal = page.locator('#scheduleModal');
  const toggle = modal.locator('#scheduleModelPicker [data-model-toggle]');
  await toggle.click();
  const option = modal.getByRole('option', {name:/GLM Test/});
  await option.focus();
  await page.keyboard.press('Escape');
  assert.ok(await modal.evaluate(el => el.classList.contains('open')));
  assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(await toggle.evaluate(el => document.activeElement === el), true);
  await page.keyboard.press('Escape');
  assert.equal(await modal.evaluate(el => el.classList.contains('open')), false);
  assert.equal(await opener.evaluate(el => document.activeElement === el), true);
}));

for (const viewport of [{width:1280,height:400},{width:390,height:600}]) {
  test(`one keyboard search and correct focus at ${viewport.width}×${viewport.height}`, () => withPage(async ({page, base, composer}) => {
    await page.goto(`${base}/#/collaboration`);
    await composer.focus();
    await page.keyboard.press('Control+k');
    const search = page.getByRole('textbox', {name:'搜索会话名称'});
    await search.waitFor();
    assert.equal(await page.locator('#commandSearch').isVisible(), false);
    assert.equal(await search.evaluate(el=>document.activeElement===el), true);
    assert.equal(await search.evaluate(el=>{const rect=el.getBoundingClientRect();return document.elementFromPoint(rect.x+rect.width/2,rect.y+rect.height/2)===el}), true);
    await page.keyboard.press('Escape');
    await search.waitFor({state:'hidden'});
    assert.equal(await composer.evaluate(el=>document.activeElement===el), true);
    await composer.fill('@');
    await page.locator('#chatRoot .mention-options').waitFor();
    await page.keyboard.press('Control+k');
    assert.equal(await search.isVisible(), false);
    await page.keyboard.press('Escape');
    await page.locator('#chatRoot .mention-options').waitFor({state:'hidden'});
    await page.evaluate(() => window.loopforgePlatform.navigate('settings'));
    await page.locator('#settingsView.active').waitFor();
    const trigger=page.locator('#railSettingsEntry');
    await trigger.focus();
    await page.keyboard.press('Control+k');
    const native=page.locator('#commandSearch');
    await native.waitFor();
    await page.waitForFunction(() => document.activeElement?.id === 'commandSearch');
    assert.equal(await search.isVisible(), false);
    await page.keyboard.press('Escape');
    assert.equal(await native.isVisible(), false);
    assert.equal(await trigger.evaluate(el=>document.activeElement===el), true);
  }, {viewport}));
}

test('English localizes UI labels but not messages or reference names', () => withPage(async ({page, base}) => {
  await page.addInitScript(() => localStorage.setItem('loopforge.lang','en'));
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto(`${base}/#/collaboration/s-1`);
  await page.locator('.markdown strong').waitFor();
  assert.equal(await page.locator('.user-bubble').innerText(), '新建需求');
  assert.equal(await page.locator('.markdown strong').innerText(), '新建需求');
  assert.equal(await page.locator('.message-references span span').innerText(), '新建需求');
  await page.getByRole('button', {name:'Copy message'}).click();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), '新建需求');
  for (const selector of ['.markdown strong', '.message-references span span']) {
    await page.evaluate(() => navigator.clipboard.writeText(''));
    await page.locator(selector).evaluate(element => {
      const range = document.createRange();
      range.selectNodeContents(element);
      getSelection().removeAllRanges();
      getSelection().addRange(range);
    });
    await page.keyboard.press('Control+c');
    await page.waitForFunction(async () => await navigator.clipboard.readText() === '新建需求');
  }
}));

test('English search preserves session and project names', () => withPage(async ({page, base, composer}) => {
  await page.addInitScript(() => localStorage.setItem('loopforge.lang','en'));
  await page.goto(`${base}/`);
  await composer.focus();
  await page.keyboard.press('Control+k');
  const dialog = page.locator('#chatRoot [role=dialog]');
  await dialog.waitFor();
  const result = dialog.locator('.search-results button').first();
  assert.equal(await result.innerText(), '新建需求\n新建需求');
  assert.equal(await dialog.locator('.date-label').innerText(), '新建需求');
  await page.getByRole('dialog', {name:'Search conversations'}).waitFor();
  await dialog.getByRole('textbox', {name:'Search conversation names'}).waitFor();
  await result.evaluate(element => {
    const range = document.createRange();
    range.selectNodeContents(element.querySelector('span[data-i18n-skip]'));
    getSelection().removeAllRanges();
    getSelection().addRange(range);
  });
  assert.equal(await page.evaluate(() => getSelection().toString()), '新建需求');
}));

test('English artifact preview preserves definition content but translates its controls', () => withPage(async ({page, base}) => {
  await page.addInitScript(() => localStorage.setItem('loopforge.lang','en'));
  await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.goto(`${base}/#/collaboration/preview`);
  const toggle = page.locator('button[aria-controls="conversation-artifacts"]');
  await toggle.click();
  const title = page.locator('.artifact-file-copy strong').filter({hasText:'新建需求'});
  assert.equal(await title.locator('xpath=ancestor::button').getAttribute('title'), '新建需求');
  await title.click();
  const preview = page.locator('.definition-preview');
  await preview.waitFor();
  assert.equal(await preview.locator('h2').innerText(), '新建需求');
  const description = preview.locator('.definition-summary p');
  assert.equal(await description.innerText(), '新建需求');
  assert.equal(await preview.locator('.definition-node-list strong').innerText(), '新建需求');
  await page.getByText('Back to artifacts', {exact:true}).waitFor();
  await description.evaluate(element => {
    const range = document.createRange();
    range.selectNodeContents(element);
    getSelection().removeAllRanges();
    getSelection().addRange(range);
  });
  await page.keyboard.press('Control+c');
  await page.waitForFunction(async () => await navigator.clipboard.readText() === '新建需求');
  await page.getByText('Back to artifacts', {exact:true}).click();
  await page.getByText('验收演示', {exact:true}).click();
  const slides = page.locator('.artifact-panel-preview .slides');
  assert.equal(await slides.locator('.slide-eyebrow').innerText(), '新建需求');
  await slides.getByRole('button', {name:'Download PowerPoint'}).waitFor();
}));

test('quick access returns from executor settings to the same step', () => withPage(async ({page, base}) => {
  let credentialReads = 0;
  page.on('request', request => { if (new URL(request.url()).pathname === '/api/users') credentialReads++; });
  await page.goto(`${base}/?quickAccess=1#/collaboration`);
  const configure = page.getByRole('button', {name:'执行器配置'});
  await configure.waitFor({timeout:7000});
  const heading = page.locator('.qa-title');
  await page.getByRole('button', {name:'下一步'}).click();
  await page.getByRole('button', {name:'上一步'}).waitFor();
  await page.getByText('新建项目', {exact:true}).first().click();
  const name = page.getByRole('textbox', {name:'项目名称'});
  await name.fill('保留向导草稿');
  await page.getByRole('button', {name:'上一步'}).click();
  await configure.waitFor();
  const step = await heading.innerText();
  await page.getByText('使用访问令牌（高级）', {exact:true}).click();
  await page.getByLabel('GitHub 个人访问令牌').fill('disposable-test-token');
  await configure.click();
  await page.locator('#settingsView.active').waitFor();
  await page.locator('.qa-backdrop').waitFor({state:'detached'});
  const beforeReturn = credentialReads;
  await page.locator('#returnToQuickAccess').click();
  await heading.waitFor();
  assert.equal(await heading.innerText(), step);
  await page.waitForFunction(() => document.activeElement?.classList.contains('qa-title'));
  assert.ok(credentialReads > beforeReturn);
  await page.getByText('使用访问令牌（高级）', {exact:true}).click();
  assert.equal(await page.getByLabel('GitHub 个人访问令牌').inputValue(), '');
  await page.getByRole('button', {name:'下一步'}).click();
  assert.equal(await name.inputValue(), '保留向导草稿');
}));

for (const delayed of [false, true]) {
  test(`requirement handoff reaches ${delayed?'delayed':'mounted'} composer`, () => withPage(async ({page, base, composer}) => {
    let release;
    if (delayed) {
      const gate = new Promise(resolve => { release=resolve; });
      await page.route(entryRequest, async route => { await gate; await route.continue(); });
    }
    await page.goto(`${base}/#/issues`, {waitUntil:'commit'});
    await page.locator('#issuesView.active').waitFor({timeout:7000});
    if (!delayed) await composer.waitFor({state:'attached'});
    else assert.equal(await composer.count(), 0);
    await page.locator('#newIssue').click();
    await page.locator('#chatInput').fill('需求转交测试');
    const created = page.waitForResponse(response => new URL(response.url()).pathname === '/api/issues' && response.request().method() === 'POST');
    await page.locator('#reqCreateChat').click();
    assert.equal((await (await created).json()).projectId, 'project-1');
    release?.();
    await page.waitForFunction(() => document.querySelector('#chatRoot .composer-column textarea')?.value === '@需求转交测试 帮我澄清这个需求');
    assert.equal(await page.locator('#chatRoot .composer-attachment-file > span').innerText(), '需求转交测试');
    assert.ok(await page.locator('#chatRoot .composer-column').getByText('Atlas', {exact:true}).isVisible());
  }));
}

test('main document mounts chat, preserves drafts, and shares theme and routing', () => withPage(async ({page, base, errors}) => {
    await page.goto(`${base}/#/collaboration`);
    const composer=page.locator('#chatRoot .composer-column textarea');
    await composer.waitFor({state:'visible',timeout:20000});
    await composer.fill('保留这份草稿');
    assert.equal(await page.locator('#chatNextFrame').count(),0);
    assert.equal(await page.locator('#chatRoot .chat-shell').count(),1);
    await page.evaluate(() => window.loopforgePlatform.navigate('settings'));
    await page.locator('#settingsView.active').waitFor();
    const resize=page.getByRole('button',{name:'调整侧栏宽度'});
    await resize.focus();
    await page.keyboard.press('ArrowRight');
    await page.waitForFunction(() => document.querySelector('#chatRoot .chat-shell')?.style.getPropertyValue('--sidebar-width') === `${localStorage.getItem('agentflow.sidebarWidth')}px`);
    assert.equal(await page.evaluate(() => document.querySelector('#chatRoot .chat-shell').style.getPropertyValue('--sidebar-width')), `${await page.evaluate(() => localStorage.getItem('agentflow.sidebarWidth'))}px`);
    await page.evaluate(() => window.loopforgePlatform.setTheme('dark'));
    assert.equal(await page.locator('html').getAttribute('data-theme'),'dark');
    await page.evaluate(() => { location.hash='#/collaboration'; });
    await composer.waitFor({state:'visible'});
    assert.equal(await composer.inputValue(),'保留这份草稿');
    await page.goto(`${base}/?chatSession=s-1#/collaboration/s-1`);
    await page.getByText('浏览器测试会话').first().waitFor({timeout:10000});
    assert.equal(new URL(page.url()).searchParams.get('chatSession'),'s-1');
    await page.evaluate(() => window.loopforgePlatform.navigate('settings'));
    await page.locator('#settingsView.active').waitFor();
    await page.evaluate(() => { location.hash='#/collaboration'; });
    await page.getByText('浏览器测试会话').first().waitFor({timeout:10000});
    assert.equal(new URL(page.url()).searchParams.get('chatSession'),'s-1');
    await page.evaluate(() => window.loopforgePlatform.navigate('settings'));
    await page.locator('#settingsView.active').waitFor();
    await page.goBack();
    await page.locator('#collaborationView.active').waitFor();
    assert.equal(new URL(page.url()).searchParams.get('chatSession'),'s-1');
    await page.goForward();
    await page.locator('#settingsView.active').waitFor();
    await page.goBack();
    await page.locator('#collaborationView.active').waitFor();
    await page.setViewportSize({width:390,height:844});
    await composer.waitFor({state:'visible'});
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.setViewportSize({width:1440,height:900});
    await page.evaluate(() => window.loopforgePlatform.navigate('settings'));
    await page.locator('#settingsView.active').waitFor();
    await page.evaluate(() => openIssueInChat({id:'issue-browser-test',projectId:'project-1'}));
    await page.waitForFunction(() => document.querySelector('#chatRoot .composer-column textarea')?.value.startsWith('@需求转交测试 '));
    assert.deepEqual(errors,[]);
    await page.evaluate(() => localStorage.setItem('loopforge.lang','en'));
    await page.reload();
    await composer.waitFor({state:'visible',timeout:20000});
    await composer.fill('@');
    await page.locator('#chatRoot .mention-options [role="option"]').filter({hasText:'LOOP'}).click();
    const option=page.locator('#chatRoot .mention-options [role="option"]').first();
    await option.locator('strong').getByText('平台验收 · 脚本与审批',{exact:true}).waitFor();
    await option.click();
    assert.match(await composer.inputValue(), /@平台验收 · 脚本与审批 /);
    await page.locator('#chatRoot .composer-attachment-file > span').filter({hasText:/^平台验收 · 脚本与审批$/}).waitFor();
    await page.getByRole('button',{name:'Remove reference 平台验收 · 脚本与审批'}).click();
    assert.doesNotMatch(await composer.inputValue(), /平台验收/);
    assert.deepEqual(errors,[]);
}));
