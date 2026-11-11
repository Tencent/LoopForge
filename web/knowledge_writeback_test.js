'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ctx = {
  escapeHTML: value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])),
  kbBadge: (label, tone) => `<span class="hui-badge" data-tone="${tone}">${label}</span>`,
  document: { addEventListener() {}, getElementById() { return null; } },
  MutationObserver: class { observe() {} },
  URL,
  window: {},
};
vm.createContext(ctx);
const source = fs.readFileSync(path.join(__dirname, 'knowledge.js'), 'utf8');
vm.runInContext(source, ctx);
assert.match(source, /确认写回 Git 来源/);
assert.match(source, /不支持的平台只推送分支/);

const html = ctx.kbWriteInfoHTML({
  branch: 'kb/writeback-<kbs-1>',
  mrUrl: 'https://github.com/example/repo/pull/7',
  mrStatus: 'mr_open',
  writeNote: '已创建合并请求，合并后会自动同步索引。',
});
assert.match(html, /kb-write-info/);
assert.match(html, /合并请求待合并/);
assert.match(html, /href="https:\/\/github.com\/example\/repo\/pull\/7"/);
assert.match(html, /kb\/writeback-&lt;kbs-1&gt;/);
assert.match(html, /已创建合并请求/);

const unsafe = ctx.kbWriteInfoHTML({ mrUrl: 'javascript:alert(1)', mrStatus: 'merged' });
assert.doesNotMatch(unsafe, /href=/);
assert.match(unsafe, /已合并/);

const empty = ctx.kbWriteInfoHTML({});
assert.equal(empty, '');

const failed = ctx.kbWriteInfoHTML({ mrStatus: 'failed', writeError: '推送被仓库拒绝' });
assert.match(failed, /写回失败/);
assert.match(failed, /推送被仓库拒绝/);

assert.equal(ctx.kbWriteStatus({ mrStatus: 'branch_pushed' })[0], '已推分支');
assert.equal(ctx.kbSafeHref('ftp://example.com/x'), '');

assert.equal(ctx.kbVectorStatusText(true, true), '向量检索：已启用（当前不可用）');
assert.equal(ctx.kbVectorStatusText(true, false), '向量检索：已启用');
assert.equal(ctx.kbVectorStatusText(false, false), '向量检索：未启用（仅关键词）');
assert.match(ctx.kbSourceProgressHTML({ vectorCurrent: 32, vectorTotal: 40 }), /重建向量中 32\/40/);
assert.equal(ctx.kbSourceProgressHTML({}), '');
assert.equal(ctx.kbSyncStageLabel('pulling'), '拉取中');
assert.equal(ctx.kbSyncStageLabel('comparing'), '比对中');
assert.equal(ctx.kbSyncStageLabel('indexing'), '建索引中');
assert.equal(ctx.kbSyncStageLabel(''), '');

ctx.state={projectFilter:'p',data:{projects:[{id:'p',name:'体验'}]}};
const kbState=ctx.kbViewModel();
kbState.sharedAdmin=false;
kbState.overview={bases:[{id:'b',layer:'project',name:'项目知识库',sources:[
  {id:'git1',baseId:'b',kind:'git',name:'LoopForge 文档',status:'syncing',syncStage:'pulling',branch:'main',metadataChecks:true,docCount:25},
]}]};
const sourcesHTML=ctx.kbSourcesPanelHTML(true);
assert.match(sourcesHTML, /拉取中/);
assert.match(sourcesHTML, /data-kb-metadata="git1"/);
assert.match(sourcesHTML, /aria-checked="true"/);

ctx.relativeTime=()=>'刚刚';
ctx.$=()=>null;
kbState.governance={
  metadataRule:'没有 frontmatter 的文档只检查死链和标题重复',
  counts:{stale:0,duplicate:1,dead_link:0,missing_owner:0,total:1},
  issues:[],feedback:[],
  gaps:[{query:'香蕉皮',count:2,lastSeen:'2026-11-11T00:00:00Z'}],
  gapsTotal:1,gapsHidden:3,
};
const body={innerHTML:'',querySelectorAll(){return []}};
ctx.kbRenderGovernance(body);
assert.match(body.innerHTML, /没有 frontmatter/);
assert.match(body.innerHTML, /忽略/);
assert.match(body.innerHTML, /已处理/);
assert.match(body.innerHTML, /另有 3 条已忽略或已处理/);
assert.match(source, /\/api\/knowledge\/governance\/gaps/);
assert.match(source, /\/api\/knowledge\/bases\/sources\/metadata/);

assert.match(ctx.kbGitCredentialError('git@github.com:Tencent/LoopForge.git', ''), /SSH 地址需要部署密钥/);
assert.equal(ctx.kbGitCredentialError('git@github.com:Tencent/LoopForge.git', 'ssh'), '');
assert.match(ctx.kbGitCredentialError('https://github.com/Tencent/LoopForge.git', 'ssh'), /HTTPS 地址/);

const parsedCite = ctx.kbParseCitation('kb-hosted-x@4:guides/a.md#L12-L13');
assert.equal(parsedCite.source, 'kb-hosted-x');
assert.equal(parsedCite.version, '4');
assert.equal(parsedCite.path, 'guides/a.md');
assert.equal(parsedCite.start, 12);
assert.equal(parsedCite.end, 13);
const fullCite = ctx.kbCitationHTML('kb-hosted-x@4:guides/a.md#L12-L13', '', { citation: 'kb-hosted-x@4:guides/a.md#L12-L13', full: true });
assert.match(fullCite, /kb-cite-full/);
assert.match(fullCite, /data-kb-cite-open/);
console.log('knowledge writeback ok');
