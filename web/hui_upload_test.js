const assert = require('assert');
const path = require('path');
const { createDOM } = require('./hui_fake_dom');

(async () => {
  const dom = createDOM();
  const { document, window } = dom;
  dom.load(path.join(__dirname, 'app/hui-select.js'));
  dom.load(path.join(__dirname, 'app/hui-upload.js'));
  const { HuiUpload } = window;

  const file = (name, type = '', size = 2048) => ({ name, type, size });
  assert.ok(HuiUpload.accepts('', file('a.bin')));
  assert.ok(HuiUpload.accepts('.zip,application/zip', file('skill.ZIP')), 'extensions match case-insensitively');
  assert.ok(HuiUpload.accepts('.zip, application/zip', file('blob', 'application/zip')));
  assert.ok(HuiUpload.accepts('image/*', file('a.png', 'image/png')));
  assert.ok(!HuiUpload.accepts('image/*,.md', file('a.txt', 'text/plain')));

  const form = dom.html(`<form><label>导入 MCP 配置<input type="file" id="mcpImport" accept=".json"></label><button type="reset">重置</button></form>`);
  const hiddenPick = dom.html(`<div><input type="file" id="skillZipPick" style="display:none"><input type="file" hidden id="attach"></div>`);
  hiddenPick.querySelector('#skillZipPick').style.display = 'none';
  const native = dom.html(`<div data-native><input type="file" id="raw"></div>`);
  await dom.settle();

  const input = form.querySelector('#mcpImport');
  const zone = form.querySelector('.hui-upload');
  assert.ok(zone, 'visible file inputs get an upload zone');
  assert.strictEqual(HuiUpload.enhance(input).zone, zone, 'enhance is idempotent');
  assert.ok(input.classList.contains('hui-upload-native'));
  assert.strictEqual(input.tabIndex, -1);
  assert.ok(!hiddenPick.querySelector('.hui-upload'), 'hidden inputs opened by custom buttons are left alone');
  assert.ok(!native.querySelector('.hui-upload'), '[data-native] is skipped');

  const pick = zone.querySelector('.hui-upload-pick');
  assert.strictEqual(pick.type, 'button');
  assert.strictEqual(pick.textContent, '选择文件');
  assert.strictEqual(pick.getAttribute('aria-label'), '导入 MCP 配置：选择文件');
  assert.strictEqual(zone.querySelector('.hui-upload-hint').textContent, '支持 .json，单个文件');
  assert.ok(zone.querySelector('.hui-upload-files').hidden);

  let clicks = 0;
  input.addEventListener('click', () => clicks++);
  pick.click();
  assert.strictEqual(clicks, 1, 'the button opens the native file dialog');

  let changes = 0;
  input.addEventListener('change', () => changes++);
  const drop = files => {
    const event = dom.event('drop', { bubbles: true, dataTransfer: { files } });
    zone.dispatchEvent(event);
    return event;
  };
  const over = dom.event('dragover', { bubbles: true });
  zone.dispatchEvent(over);
  assert.ok(over.defaultPrevented, 'dragover is accepted so the drop can happen');
  assert.ok(zone.classList.contains('is-dragover'));

  const error = zone.querySelector('.hui-upload-error');
  assert.ok(drop([file('a.json'), file('b.json')]).defaultPrevented);
  assert.ok(!zone.classList.contains('is-dragover'));
  assert.strictEqual(changes, 0, 'a single-file input rejects several dropped files');
  assert.strictEqual(error.textContent, '只能选择一个文件');
  assert.ok(!error.hidden);
  assert.strictEqual(error.getAttribute('role'), 'alert');

  drop([file('notes.txt', 'text/plain')]);
  assert.strictEqual(changes, 0);
  assert.strictEqual(error.textContent, '不支持的文件类型：notes.txt');

  drop([file('servers.json', 'application/json', 3 * 1024 * 1024)]);
  assert.strictEqual(changes, 1, 'an accepted drop fires change');
  assert.deepStrictEqual(input.files.map(f => f.name), ['servers.json']);
  assert.ok(error.hidden, 'a good drop clears the error');
  const list = zone.querySelector('.hui-upload-files');
  assert.ok(!list.hidden);
  assert.deepStrictEqual(list.querySelectorAll('li').map(li => li.textContent), ['servers.json3.0 MB']);

  form.reset();
  dom.advance(1);
  assert.ok(list.hidden, 'form reset clears the file list');

  const multi = dom.html(`<div><input type="file" multiple></div>`);
  await dom.settle();
  const multiApi = HuiUpload.enhance(multi.querySelector('input'));
  assert.strictEqual(multi.querySelector('.hui-upload-hint').hidden, true, 'no hint without accept or single-file limit');
  multiApi.take(Array.from({ length: 7 }, (_, i) => file(`f${i}.txt`)));
  const items = multi.querySelectorAll('li');
  assert.strictEqual(items.length, 6, 'five files and a summary row');
  assert.strictEqual(items[5].textContent, '等 7 个文件');
  multi.querySelector('input').value = '';
  multi.querySelector('input').files = [];
  multi.querySelector('input').value = '';
  assert.ok(multi.querySelector('.hui-upload-files').hidden, 'clearing value re-renders');

  const folder = dom.html(`<div><input type="file" webkitdirectory multiple></div>`);
  await dom.settle();
  assert.strictEqual(folder.querySelector('.hui-upload-pick').textContent, '选择文件夹');
  const folderApi = HuiUpload.enhance(folder.querySelector('input'));
  assert.strictEqual(folderApi.take([file('a.md')]), false, 'folders cannot be dropped');
  assert.strictEqual(folder.querySelector('.hui-upload-error').textContent, '文件夹请点击「选择文件夹」上传');

  input.setAttribute('disabled', '');
  await dom.settle();
  assert.ok(pick.disabled, 'disabled syncs to the button');
  assert.ok(zone.classList.contains('is-disabled'));
  const blocked = dom.event('dragover', { bubbles: true });
  zone.dispatchEvent(blocked);
  assert.ok(!blocked.defaultPrevented, 'disabled zones refuse drops');

  form.remove();
  await dom.settle();
  assert.ok(!input.classList.contains('hui-upload-native'), 'removed inputs are destroyed');

  console.log('hui upload ok');
})().catch(err => { console.error(err); process.exit(1); });
