import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('HuiUpload enhances file inputs, validates drag/drop, lists files, and cleans up', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  const form = app.document.createElement('form');
  const label = app.document.createElement('label');
  label.textContent = '附件上传';
  const input = app.document.createElement('input');
  input.type = 'file';
  input.accept = '.json, image/*, text/plain';
  label.append(input);
  form.append(label);
  app.document.body.append(form);

  const api = app.window.HuiUpload.enhance(input);
  assert.ok(api?.zone);
  assert.equal(app.window.HuiUpload.enhance(input), api);

  // Test accepts helper directly
  assert.equal(app.window.HuiUpload.accepts('.json, image/*, text/plain', { name: 'a.json', type: '' }), true);
  assert.equal(app.window.HuiUpload.accepts('.json, image/*, text/plain', { name: 'b.png', type: 'image/png' }), true);
  assert.equal(app.window.HuiUpload.accepts('.json, image/*, text/plain', { name: 'c.txt', type: 'text/plain' }), true);
  assert.equal(app.window.HuiUpload.accepts('.json', { name: 'c.exe', type: 'application/octet-stream' }), false);

  // Dragover and dragleave
  api.zone.dispatchEvent(new app.window.Event('dragenter', { bubbles: true, cancelable: true }));
  assert.ok(api.zone.classList.contains('is-dragover'));
  api.zone.dispatchEvent(new app.window.Event('dragleave', { bubbles: true }));
  assert.equal(api.zone.classList.contains('is-dragover'), false);

  // Reject multiple files on single file input
  assert.equal(api.take([{ name: '1.json', size: 10 }, { name: '2.json', size: 20 }]), false);
  assert.match(api.zone.querySelector('.hui-upload-error').textContent, /只能选择一个文件/);

  // Reject bad file type
  assert.equal(api.take([{ name: 'bad.exe', size: 100, type: 'application/x-msdownload' }]), false);
  assert.match(api.zone.querySelector('.hui-upload-error').textContent, /不支持的文件类型/);

  // Provide DataTransfer stub so take() succeeds and renders file sizes (B, KB, MB) and >5 items
  input.multiple = true;
  app.window.DataTransfer = class {
    constructor() {
      const list = [];
      this.items = { add(f) { list.push(f); } };
      this.files = list;
    }
  };
  const sixFiles = [
    { name: 'a.json', size: 500, type: 'application/json' },
    { name: 'b.json', size: 2048, type: 'application/json' },
    { name: 'c.json', size: 3 * 1024 * 1024, type: 'application/json' },
    { name: 'd.json', size: 10, type: 'application/json' },
    { name: 'e.json', size: 10, type: 'application/json' },
    { name: 'f.json', size: 10, type: 'application/json' },
  ];
  Object.defineProperty(input, 'files', { configurable: true, writable: true, value: [] });
  assert.equal(api.take(sixFiles), true);
  assert.match(api.zone.querySelector('.hui-upload-files').textContent, /MB/);
  assert.match(api.zone.querySelector('.hui-upload-files').textContent, /等 6 个文件/);

  // Folder mode rejects drop
  const folderInput = app.document.createElement('input');
  folderInput.type = 'file';
  folderInput.setAttribute('webkitdirectory', '');
  app.document.body.append(folderInput);
  const folderApi = app.window.HuiUpload.enhance(folderInput);
  assert.equal(folderApi.take([{ name: 'a.json', size: 10 }]), false);
  assert.match(folderApi.zone.textContent, /选择文件夹/);

  // Reset and destroy
  form.dispatchEvent(new app.window.Event('reset'));
  await settle(2);
  app.window.HuiUpload.destroy(input);
  app.window.HuiUpload.destroy(folderInput);
});

test('HuiDate opens calendar, navigates months and keyboard grid, selects today/clear, and destroys', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  const label = app.document.createElement('label');
  label.textContent = '截止日期';
  const input = app.document.createElement('input');
  input.type = 'date';
  input.min = '2026-01-01';
  input.max = '2026-12-31';
  input.value = '2026-10-15';
  label.append(input);
  app.document.body.append(label);

  const api = app.window.HuiDate.enhance(input);
  assert.ok(api?.trigger);
  assert.equal(app.window.HuiDate.format(new Date(2026, 9, 5)), '2026-10-05');
  assert.equal(app.window.HuiDate.parse('invalid'), null);
  assert.equal(app.window.HuiDate.parse('2026-02-31'), null);

  // Open via trigger click
  api.trigger.click();
  assert.equal(api.isOpen(), true);
  assert.match(api.panel.textContent, /2026 年 10 月/);

  // Navigate prev / next month
  const [prevBtn, nextBtn] = api.panel.querySelectorAll('.hui-date-nav');
  prevBtn.click();
  assert.match(api.panel.textContent, /2026 年 9 月/);
  nextBtn.click();
  assert.match(api.panel.textContent, /2026 年 10 月/);

  // Keyboard navigation on day cell
  const focusedDay = api.panel.querySelector('.hui-date-day[tabindex="0"]');
  focusedDay?.focus();
  for (const key of ['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown', 'Home', 'End', 'PageUp', 'PageDown', 'Tab']) {
    api.panel.dispatchEvent(new app.window.KeyboardEvent('keydown', { key, bubbles: true }));
  }

  // Click a day cell to select
  const dayCell = api.panel.querySelector('.hui-date-day:not([disabled])');
  dayCell.click();
  assert.equal(api.isOpen(), false);

  // Reopen via ArrowDown on trigger, click Today and Clear
  api.trigger.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  assert.equal(api.isOpen(), true);
  const [clearBtn, todayBtn] = api.panel.querySelectorAll('.hui-date-action');
  todayBtn.click();
  api.open();
  clearBtn.click();
  assert.equal(input.value, '');

  // Escape closes
  api.open();
  api.panel.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(api.isOpen(), false);

  // Label click focuses trigger
  label.click();
  app.window.HuiDate.destroy(input);
});

test('HuiSelect supports trigger mode, inline mode, search filter, keyboard navigation, and SOP catalog interactions', async (t) => {
  const app = await loadApp();
  t.after(app.close);

  // Trigger mode select with >5 options and optgroup
  const select = app.document.createElement('select');
  select.setAttribute('data-icon', 'folder');
  select.setAttribute('aria-label', '模型选择');
  select.innerHTML = `
    <optgroup label="主力模型">
      <option value="m1" data-meta="推荐" data-search="deepseek">DeepSeek V4</option>
      <option value="m2">GLM 5</option>
      <option value="m3">Claude Sonnet</option>
    </optgroup>
    <optgroup label="其他模型">
      <option value="m4">GPT 5</option>
      <option value="m5">Kimi K2</option>
      <option value="m6">Qwen Max</option>
    </optgroup>
  `;
  app.document.body.append(select);

  const api = app.window.HuiSelect.enhance(select);
  assert.ok(api?.trigger);
  api.trigger.click();
  assert.equal(api.isOpen(), true);

  // Filter options via search input
  const search = api.panel.querySelector('.hui-select-search input');
  search.value = 'deepseek';
  search.dispatchEvent(new app.window.Event('input', { bubbles: true }));
  api.panel.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  assert.equal(select.value, 'm1');
  assert.equal(api.isOpen(), false);

  // Reopen and navigate with arrows / Home / End / Escape
  api.trigger.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  assert.equal(api.isOpen(), true);
  for (const key of ['ArrowDown', 'ArrowUp', 'Home', 'End', 'Tab']) {
    api.panel.dispatchEvent(new app.window.KeyboardEvent('keydown', { key, bubbles: true }));
  }
  api.open();
  api.panel.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
  assert.equal(api.isOpen(), false);

  // Inline mode select
  const inlineSelect = app.document.createElement('select');
  inlineSelect.innerHTML = '<option value="a">Alpha</option><option value="b">Beta</option>';
  app.document.body.append(inlineSelect);
  inlineSelect.dispatchEvent(new app.window.MouseEvent('mousedown', { button: 0, bubbles: true, cancelable: true }));
  inlineSelect.dispatchEvent(new app.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
  app.window.HuiSelect.closeAll();
  app.window.HuiSelect.destroy(select);

  // SOP catalog card menu, actions, and motion helpers
  app.evaluate(`
    state.currentUser = { id: 1, loginName: 'owner', isPlatformAdmin: true };
    state.sopScope = 'project';
    renderSOPCatalog();
  `);
  const moreBtn = app.document.querySelector('[data-sop-menu]');
  if (moreBtn) {
    moreBtn.click();
    const menu = app.document.querySelector('#sopActionPopover');
    assert.equal(menu.hidden, false);
    for (const key of ['ArrowDown', 'ArrowUp', 'Home', 'End', 'Escape']) {
      menu.dispatchEvent(new app.window.KeyboardEvent('keydown', { key, bubbles: true }));
    }
  }
  app.evaluate(`
    state.sopScope = 'shared';
    state.data.workflows.push({ id: 'shared-wf', name: 'Shared SOP', projectId: 'other-proj', shared: true, status: 'published', version: 1, nodes: [] });
    renderSOPCatalog();
    state.sopScope = 'generic';
    state.data.workflows.push({ id: 'gen-wf', name: 'Generic SOP', generic: true, status: 'published', version: 1, nodes: [{ id: 'n1', type: 'agent' }] });
    renderSOPCatalog();
  `);
  const genMore = app.document.querySelector('[data-sop-menu="gen-wf"]');
  genMore?.click();
  await settle(4);
});
