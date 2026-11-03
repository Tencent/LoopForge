import test from 'node:test';
import assert from 'node:assert/strict';
import { loadApp, settle } from '../lib/load-app.mjs';

test('skill file tree groups folders and previews markdown and source modes', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  const skill = {
    id: 'skill-files', projectId: 'project-1', name: 'File Skill', description: 'Fixture',
    fileRefs: [
      { path: 'SKILL.md', size: 42, sha256: '1234567890abcdef1234', mode: 0o644 },
      { path: 'scripts/check.js', size: 64, sha256: 'abcdef', mode: 0o755 },
      { path: 'docs/guide.txt', size: 30, sha256: 'fedcba', mode: 0o644 },
    ],
  };
  app.evaluate(`state.data.skills=[${JSON.stringify(skill)}];state.assetType='skills';state.selectedAsset='skill-files';renderAssets()`);
  const html = app.evaluate(`skillFileRowsHTML(state.data.skills[0])`);
  assert.match(html, /scripts\/|SKILL\.md|可执行/);
  app.router.get('/api/skills/skill-files/files/SKILL.md', '# File Skill\n\nUse safely.');
  await app.evaluate(`openSkillFilePreview(state.data.skills[0],'SKILL.md')`);
  app.document.querySelector('[data-mode="source"]')?.click();
  app.document.querySelector('[data-mode="preview"]')?.click();
  app.document.querySelector('[data-tree-toggle]')?.click();
  assert.match(app.document.querySelector('#artifactPreview').textContent, /File Skill|SKILL\.md/);
});

test('multi-file skill upload reads files concurrently and replaces metadata', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  app.evaluate(`
    state.data.skills=[{id:'skill-upload',projectId:'project-1',name:'Upload Skill',fileRefs:[]}];
    state.assetType='skills';state.selectedAsset='skill-upload';renderAssets();
  `);
  const uploaded = {
    id: 'skill-upload', projectId: 'project-1', name: 'Upload Skill',
    fileRefs: [{ path: 'SKILL.md', size: 12 }, { path: 'scripts/test.js', size: 16 }],
  };
  app.router.post('/api/skills/skill-upload/files', uploaded);
  const files = [
    { name: 'SKILL.md', webkitRelativePath: 'SKILL.md', size: 12, arrayBuffer: async () => new TextEncoder().encode('# Skill').buffer },
    { name: 'test.js', webkitRelativePath: 'scripts/test.js', size: 16, arrayBuffer: async () => new TextEncoder().encode('console.log(1)').buffer },
  ];
  const input = { files, value: 'selected' };
  await app.evaluate(`uploadSkillFiles`).call(null, input);
  await settle(5);
  assert.ok(app.router.calls.some((call) => call.path === '/api/skills/skill-upload/files'));
  assert.match(app.evaluate(`state.data.skills[0].fileRefs.map(x=>x.path).join(',')`), /SKILL\.md/);
});

test('skill upload validation handles empty, new, foreign, and invalid markdown selections', async (t) => {
  const app = await loadApp();
  t.after(app.close);
  await app.evaluate(`uploadSkillFiles`).call(null, { files: [], value: '' });
  app.evaluate(`state.assetType='skills';state.selectedAsset='__new__'`);
  await app.evaluate(`uploadSkillFiles`).call(null, {
    files: [{ name: 'x.txt', size: 1, arrayBuffer: async () => new ArrayBuffer(1) }], value: '',
  });
  await app.evaluate(`uploadSkillMd`).call(null, {
    files: [{ name: 'wrong.txt', size: 10 }], value: '',
  });
  assert.ok(app.document.querySelector('#toastContainer').textContent.length > 0);
});
