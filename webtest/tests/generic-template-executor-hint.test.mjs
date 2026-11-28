import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle, WEB_ROOT } from '../lib/load-app.mjs';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import vm from 'node:vm';
import { JSDOM } from 'jsdom';

// issue #142: generic-template roles are not hard-bound to CodeBuddy. The
// template uses the responsible user's default executor on a direct run and a
// copied SOP can switch CLI per role, so the copy modal must say so instead of
// letting users read the CodeBuddy badge as a fixed binding.
test('generic template modal explains that roles follow the default executor and can switch CLI', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const workflow = app.evaluate('structuredClone(state.data.workflows[0])');
  const generic = { ...workflow, id: 'generic-backend-delivery', name: '通用后端交付 SOP',
    generic: true, projectId: '', shared: false, owner: 'LoopForge', tags: ['public'],
    nodes: [
      { ...workflow.nodes[0], id: 'clarify', name: '需求定型', type: 'agent', agentId: 'generic-clarify' },
      { id: 'accept', name: '人工验收', type: 'approval' },
    ] };
  app.evaluate(`state.data.workflows=[${JSON.stringify(generic)}]`);
  app.evaluate(`openGenericTemplateModal('generic-backend-delivery')`);
  await settle(2);
  const structure = app.document.querySelector('#genericTemplateStructure');
  assert.ok(structure, 'structure block rendered');
  const text = structure.textContent;
  assert.match(text, /角色未固定 CLI/);
  assert.match(text, /跟随任务责任人的默认执行器/);
  assert.match(text, /复制后可在角色编辑器里更换/);
});

test('the executor hint sentence has an English dictionary entry', async () => {
  globalThis.window = {};
  new vm.Script(await readFile(path.join(WEB_ROOT, 'i18n-en.js'), 'utf8'), { filename: 'i18n-en.js' })
    .runInThisContext();
  const dict = globalThis.window.LF_I18N_EN;
  const key = '角色未固定 CLI：直接运行跟随任务责任人的默认执行器，复制后可在角色编辑器里更换';
  assert.equal(dict[key], "Roles do not pin a CLI: direct runs follow the responsible user's default executor, and copies can switch CLI in the role editor");
  assert.equal(dict['角色节点使用任务责任人的执行器配置。'], "Role steps run with the responsible user's configured executor.");
  const dom = new JSDOM('<!doctype html>');
  dom.window.close();
});
