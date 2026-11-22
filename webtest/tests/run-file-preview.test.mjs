import test from 'node:test';
import assert from 'node:assert/strict';
import {loadApp,settle} from '../lib/load-app.mjs';

function files(app){
  app.router.get('/api/runs/run-1/workspace/tree',{entries:[
    {path:'reports',name:'reports',type:'directory'},
    {path:'reports/analysis.md',name:'analysis.md',type:'file'},
    {path:'notes.txt',name:'notes.txt',type:'file'},
  ]});
  app.router.get('/api/runs/run-1/workspace/file',call=>({content:call.url.searchParams.get('path')==='notes.txt'?'plain <tag>':'# Analysis\n\n**Verified** report.\n<script>unsafe()</script>',path:call.url.searchParams.get('path')}));
}

test('run files are available without the IDE and Markdown can switch to source',async t=>{
  const app=await loadApp();t.after(app.close);files(app);
  app.evaluate(`state.activeRun=state.data.runs[0];state.selectedRun=state.activeRun.id;renderRunDetail(state.activeRun)`);
  const entry=app.document.querySelector('[data-run-files]');assert.ok(entry);entry.click();await settle(4);
  app.document.querySelector('[data-run-file-path="reports"]').click();
  app.document.querySelector('[data-run-file-path="reports/analysis.md"]').click();await settle(4);
  const host=app.document.querySelector('#artifactPreview');
  assert.equal(host.querySelector('.markdown-body h1').textContent,'Analysis');
  assert.equal(host.querySelector('.markdown-body strong').textContent,'Verified');
  assert.equal(host.querySelector('script'),null);
  host.querySelector('[data-run-file-mode="source"]').click();
  assert.match(host.querySelector('pre').textContent,/# Analysis/);
  host.querySelector('[data-run-file-up]').click();
  assert.ok(host.querySelector('[data-run-file-path="notes.txt"]'));
  assert.equal(app.router.calls.filter(c=>c.path.includes('/workspace/')&&c.method!=='GET').length,0);
  assert.equal(host.querySelector('iframe'),null);
});

test('large file lists stay bounded and search can find entries beyond the first page',async t=>{
  const app=await loadApp();t.after(app.close);
  app.router.get('/api/runs/run-1/workspace/tree',{entries:Array.from({length:230},(_,i)=>({path:`report-${String(i).padStart(3,'0')}.md`,type:'file'})),truncated:true});
  app.evaluate(`previewRunFiles(state.data.runs[0])`);await settle(4);
  const host=app.document.querySelector('#artifactPreview');
  assert.equal(host.querySelectorAll('[data-run-file-path]').length,200);
  assert.match(host.textContent,/服务端上限/);
  const search=host.querySelector('[data-run-file-search]');search.value='report-229';search.dispatchEvent(new app.window.Event('input',{bubbles:true}));
  assert.equal(host.querySelectorAll('[data-run-file-path]').length,1);
  assert.equal(host.querySelector('[data-run-file-path]').dataset.runFilePath,'report-229.md');
});

test('a closed preview and a newer modal are not changed by a delayed file response',async t=>{
  const app=await loadApp();t.after(app.close);files(app);
  let release;const wait=new Promise(resolve=>release=resolve);
  app.router.get('/api/runs/run-1/workspace/file',async()=>{await wait;return {content:'# late'}});
  app.evaluate(`previewRunFiles(state.data.runs[0])`);await settle(4);
  app.document.querySelector('[data-run-file-path="notes.txt"]').click();await settle(2);
  app.document.querySelector('#artifactModal').classList.remove('open');
  app.document.querySelector('#artifactPreview').innerHTML='<p>new modal content</p>';
  release();await settle(4);
  assert.equal(app.document.querySelector('#artifactPreview').textContent,'new modal content');
  assert.equal(app.document.querySelector('#artifactModal').classList.contains('open'),false);
});

test('file search finds nested reports and only the newest file response is shown',async t=>{
  const app=await loadApp();t.after(app.close);files(app);
  let release;const wait=new Promise(resolve=>release=resolve);
  app.router.get('/api/runs/run-1/workspace/file',async call=>{
    const path=call.url.searchParams.get('path');if(path==='reports/analysis.md')await wait;
    return {path,content:path==='notes.txt'?'latest text':'# Old report'};
  });
  app.evaluate(`previewRunFiles(state.data.runs[0])`);await settle(4);
  const search=app.document.querySelector('[data-run-file-search]');search.value='analysis';search.dispatchEvent(new app.window.Event('input',{bubbles:true}));
  app.document.querySelector('[data-run-file-path="reports/analysis.md"]').click();await settle(2);
  search.value='notes';search.dispatchEvent(new app.window.Event('input',{bubbles:true}));
  app.document.querySelector('[data-run-file-path="notes.txt"]').click();await settle(4);
  release();await settle(4);
  assert.equal(app.document.querySelector('#artifactPreview pre').textContent,'latest text');
});

test('an unavailable workspace explains how to use the existing restore entry',async t=>{
  const app=await loadApp();t.after(app.close);
  app.router.get('/api/runs/run-1/workspace/tree',{status:410,body:{error:'工作区已回收；请先恢复'}});
  app.evaluate(`previewRunFiles(state.data.runs[0])`);await settle(4);
  assert.match(app.document.querySelector('#artifactPreview').textContent,/工作区已回收/);
  assert.match(app.document.querySelector('#artifactPreview').textContent,/工作区.*恢复/);
});

test('a previously opened artifact cannot replace a newer workspace preview',async t=>{
  const app=await loadApp();t.after(app.close);files(app);
  let release;const wait=new Promise(resolve=>release=resolve);
  app.router.get('/api/runs/run-1/artifacts/old-artifact',async()=>{await wait;return '# Old immutable artifact'});
  app.evaluate(`previewArtifact({...state.data.runs[0],artifacts:[{id:'old-artifact',name:'old.md',path:'old.md'}]},'old-artifact')`);
  await settle(2);
  app.document.querySelector('#artifactModal').classList.remove('open');
  app.evaluate(`previewRunFiles(state.data.runs[0])`);await settle(4);
  release();await settle(4);
  assert.ok(app.document.querySelector('.run-file-browser'));
  assert.ok(app.document.querySelector('[data-run-file-search]'));
});
