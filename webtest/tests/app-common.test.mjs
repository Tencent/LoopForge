import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('script highlighting preserves source text and editor scrolling stays synchronized after input', async (t) => {
  const app=await loadApp();t.after(app.close);
  const script='class Example:\n\tprint("str class return 42 <span>")\n\t# comment';
  const host=app.document.createElement('div');
  host.innerHTML=app.evaluate(`highlightCode(${JSON.stringify(script)},'python')`);
  assert.equal(host.textContent,script);
  app.evaluate(`openCodeEditorModal({language:'python',value:${JSON.stringify(script)}})`);
  const editor=app.document.querySelector('#codeEditorTextarea');
  editor.scrollTop=200;editor.scrollLeft=100;
  editor.value+='\nprint("edited")';
  editor.dispatchEvent(new app.window.Event('input'));
  const highlight=app.document.querySelector('#codeEditorHighlight');
  assert.equal(highlight.textContent,editor.value+'\n');
  assert.equal(highlight.scrollTop,editor.scrollTop);
  assert.equal(highlight.scrollLeft,editor.scrollLeft);
});

test('markdown renderer covers tables, alerts, quotes, lists, tasks, rules, links, and code languages', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const markdown = [
    '# Heading **bold**',
    '',
    '> [!WARNING]',
    '> Check `credentials` before continuing.',
    '',
    '> A quoted *paragraph*.',
    '>',
    '> Second line.',
    '',
    '| Name | Value |',
    '| --- | ---: |',
    '| Build | [pass](https://example.com/build) |',
    '',
    '- [x] Unit tests',
    '- [ ] Browser tests',
    '- plain item',
    '',
    '1. First',
    '2. Second',
    '',
    '---',
    '',
    '```bash',
    'export TOKEN="$VALUE" # comment',
    'npm test -- --run',
    '```',
    '```python',
    '@decorator',
    'async def build(value: int = 42):',
    '    print("ok", True) # comment',
    '```',
    '```json',
    '{"enabled": true, "count": 3, "other": null}',
    '```',
    '```text',
    '<unsafe>',
    '```',
    '```shell',
    'echo unfinished',
  ].join('\n');
  const html = app.evaluate(`renderMarkdown(${JSON.stringify(markdown)})`);
  const host = app.document.createElement('div');
  host.innerHTML = html;
  assert.ok(host.querySelector('table'));
  assert.ok(host.querySelector('.md-alert-warning'));
  assert.equal(host.querySelectorAll('.md-code-block').length, 5);
  assert.match(html, /tok-kw|tok-comment|task-checkbox/);
  assert.equal(app.evaluate(`safeURL('javascript:alert(1)')`), '#');
  assert.equal(app.evaluate(`isMarkdownArtifact({path:'README.md'},'')`), true);
  assert.equal(app.evaluate(`isMarkdownArtifact({},'# inferred')`), true);
});

test('global confirm, prompt, alert, toast, and code editor controls resolve through real handlers', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const confirm = app.evaluate(`afConfirm({title:'Delete',message:'Continue?',danger:true,confirmText:'Delete'})`);
  app.document.querySelector('#confirmCancelBtn').click();
  assert.equal(await confirm, false);

  const prompt = app.evaluate(`afPrompt({title:'Notes',message:'Explain',multiline:true,defaultValue:'initial'})`);
  const textarea = app.document.querySelector('#confirmPromptMultiline');
  textarea.value = 'updated';
  textarea.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', ctrlKey: true, bubbles: true }));
  assert.equal(await prompt, 'updated');

  const alert = app.evaluate(`afAlert({title:'Done',message:'Saved',type:'ok'})`);
  app.document.querySelector('#confirmOkBtn').click();
  await alert;
  app.evaluate(`toast('Success','ok',50);toast('Warning','warn',50);toast('Failure','err',50)`);
  app.document.querySelector('.toast-item')?.dispatchEvent(new app.window.MouseEvent('mouseenter', { bubbles: true }));
  app.document.querySelector('.toast-item')?.dispatchEvent(new app.window.MouseEvent('mouseleave', { bubbles: true }));
  app.document.querySelector('.toast-item-close')?.click();

  app.evaluate(`openCodeEditorModal({title:'Script',subtitle:'Edit',language:'python',value:'print(1)',onSave:value=>window.__savedCode=value})`);
  const editor = app.document.querySelector('#codeEditorTextarea');
  editor.value = 'print(2)\n# done';
  editor.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  editor.dispatchEvent(new app.window.Event('scroll', { bubbles: true }));
  const language = app.document.querySelector('#codeEditorLang');
  language.value = 'shell';
  language.dispatchEvent(new app.window.Event('change', { bubbles: true }));
  app.document.querySelector('#codeEditorFullscreen')?.click();
  app.document.querySelector('#codeEditorCopy')?.click();
  app.document.querySelector('#codeEditorSave')?.click();
  await settle(5);
  assert.equal(app.evaluate(`window.__savedCode`), 'print(2)\n# done');
});

test('theme, settings sidebar, merged asset tabs, and route helpers persist UI state', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    localStorage.setItem('theme','dark');
    readThemePreference();applyTheme('system');applyTheme('light');toggleTheme();
    setSettingsSidebarExpanded(false);setSettingsSidebarExpanded(true);
    switchView('settings');startSettingsRouteTransition('access');cancelSettingsRouteTransition();
    switchView('rules');
  `);
  app.window.dispatchEvent(new app.window.StorageEvent('storage', { key: 'agentflow.theme' }));
  app.window.dispatchEvent(new app.window.Event('resize'));
  assert.equal(app.document.documentElement.dataset.theme, 'dark');
  assert.equal(app.document.querySelector('#configurationNav .nav[data-view="mcp"]')?.classList.contains('active'), true);
  assert.equal(app.document.querySelector('#assetsView [data-asset-type-tab="rules"]')?.getAttribute('aria-selected'), 'true');
  assert.equal(app.document.querySelector('#assetsView [data-asset-type-group="environment"]')?.hidden, true);
  assert.match(app.evaluate(`viewTitles().settings`), /个人配置/);
  assert.equal(app.evaluate(`viewTitles().bots`), '通道管理');
});
