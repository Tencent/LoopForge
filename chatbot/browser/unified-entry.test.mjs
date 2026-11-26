// Browser-level integration check with a deterministic API fixture; no Docker
// or running platform is needed. Run after `npm run build`.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';
import { sampleBootstrap } from '../../webtest/lib/load-app.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../web');
const types = { '.html':'text/html', '.css':'text/css', '.js':'application/javascript', '.svg':'image/svg+xml', '.png':'image/png' };

test('main document mounts chat, preserves drafts, and shares theme and routing', async () => {
  const server = http.createServer(async (req, res) => {
    const pathname = new URL(req.url, 'http://localhost').pathname;
    const relative = pathname === '/' ? 'index.html' : pathname.startsWith('/chatbot/') ? 'native-chat/' + pathname.slice(9) : pathname.slice(1);
    const file = path.resolve(root, relative);
    if (!file.startsWith(root + path.sep)) { res.writeHead(403).end(); return; }
    try { const data = await readFile(file); res.writeHead(200, { 'content-type': types[path.extname(file)] || 'application/octet-stream' }); res.end(data); }
    catch { res.writeHead(404).end(); }
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless:true, channel:'chromium', args:['--no-sandbox'] });
    const page = await browser.newPage({ viewport:{width:1440,height:900} });
    const errors=[];
    page.on('pageerror', error => errors.push(error.message));
    await page.addInitScript(() => {
      window.EventSource = class { close() {} addEventListener() {} removeEventListener() {} };
      window.WebSocket = class { close() {} addEventListener() {} removeEventListener() {} send() {} };
    });
    await page.route('**/api/**', async route => {
      const url = new URL(route.request().url()), pathname=url.pathname;
      const user={id:1,name:'Test User',username:'tester',isGuest:false,isPlatformAdmin:true};
      let body=[];
      if(pathname==='/api/auth/current') body={user,authRequired:false};
      else if(pathname==='/api/bootstrap') body=sampleBootstrap();
      else if(pathname==='/api/projects') body=sampleBootstrap().projects;
      else if(pathname==='/api/users') body=[user];
      else if(pathname==='/api/issues/issue-browser-test') body={id:'issue-browser-test',title:'需求转交测试',projectId:'project-1'};
      else if(pathname==='/api/assistant/references') body={items:[{type:'sop',id:'sop-browser-test',name:'平台验收 · 脚本与审批',projectId:'project-1'}]};
      else if(pathname==='/api/assistant/models') body={models:[],default:''};
      else if(pathname.startsWith('/api/assistant/sessions/')) body={id:pathname.split('/').pop(),projectId:'',title:'浏览器测试会话',messages:[]};
      else if(pathname==='/api/runners/default') body={runnerId:''};
      else if(pathname==='/api/auth/local') body={enabled:false};
      else if(pathname==='/api/capabilities') body={};
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(body)});
    });
    await page.goto(`http://127.0.0.1:${server.address().port}/#/collaboration`);
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
    await page.goto(`http://127.0.0.1:${server.address().port}/?chatSession=s-1#/collaboration/s-1`);
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
    await page.addInitScript(() => localStorage.setItem('loopforge.lang','en'));
    await page.goto(`http://127.0.0.1:${server.address().port}/#/collaboration`);
    await composer.waitFor({state:'visible',timeout:20000});
    await composer.fill('@');
    await page.locator('#chatRoot .mention-options [role="option"]').filter({hasText:'SOP'}).click();
    const option=page.locator('#chatRoot .mention-options [role="option"]').first();
    await option.locator('strong').getByText('平台验收 · 脚本与审批',{exact:true}).waitFor();
    await option.click();
    assert.match(await composer.inputValue(), /@平台验收 · 脚本与审批 /);
    await page.locator('#chatRoot .composer-attachment-file > span').filter({hasText:/^平台验收 · 脚本与审批$/}).waitFor();
    await page.getByRole('button',{name:'移除引用 平台验收 · 脚本与审批'}).click();
    assert.doesNotMatch(await composer.inputValue(), /平台验收/);
    assert.deepEqual(errors,[]);
  } finally {
    await browser?.close();
    await new Promise(resolve=>server.close(resolve));
  }
});
