package web

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBotSessionSearchAndTypes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `const assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');const ctx={};vm.createContext(ctx);vm.runInContext(fs.readFileSync('app/bots.js','utf8'),ctx);const sessions=[{name:'研发群',type:'group'},{name:'Alice',type:'single'}];assert.equal(ctx.filteredBotSessions(sessions,'研发')[0].type,'group');assert.equal(ctx.filteredBotSessions(sessions,'ALICE')[0].type,'single');assert.equal(ctx.filteredBotSessions(sessions,'不存在').length,0);assert.equal(ctx.botTypeLabel('group'),'群聊');assert.equal(ctx.botTypeLabel('single'),'单聊');assert.equal(ctx.filteredBotSessions(sessions,'').length,2);ctx.escapeHTML=x=>String(x);assert.match(ctx.botModelPickerHTML({model:'gpt-6.1-sol'}),/name="model" value="gpt-6.1-sol"/);assert.match(fs.readFileSync('app/bots.js','utf8'),/name="model"/);`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("search: %v %s", err, out)
	}
}

func TestBotTabsListAndGuidedSteps(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');
const ctx={escapeHTML:x=>String(x??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'})[c]),isPlatformAdmin:()=>false,currentProject:()=>ctx.project,project:{name:'AgentFlow',ownerId:9,members:[]},state:{authRequired:false}};
vm.createContext(ctx);vm.runInContext(fs.readFileSync('app/bots.js','utf8'),ctx);

assert.equal(ctx.botStepAllowed({name:''},1),true);
assert.equal(ctx.botStepAllowed({name:''},2),false);
assert.equal(ctx.botStepAllowed({name:''},3),false);
assert.equal(ctx.botStepAllowed({id:'b1'},2),true);
assert.equal(ctx.botStepAllowed({id:'b1'},3),false);
assert.equal(ctx.botStepAllowed({id:'b1',target:{id:'g'}},3),true);
assert.equal(ctx.botStepAllowed({id:'b1',target:{id:'g'},_targetChanged:true},3),false,'unsaved target must not unlock the test step');

const bots=[{id:'b1',name:'研发通知',enabled:true,health:'ready',ownerId:1,target:{type:'group',name:'研发群'}},{id:'b2',name:'<夜间>',enabled:false,health:'unchecked',ownerId:2}];
const list=ctx.botListHTML(bots);
assert.deepEqual([...list.matchAll(/<th(?: [^>]*)?>(?:<span class="bot-sr">)?([^<]*)/g)].map(m=>m[1]),['名称','接收会话','启用状态','连接健康','操作']);
assert.equal((list.match(/<tr data-bot-row=/g)||[]).length,2);
assert.match(list,/<span class="bot-chip">群聊<\/span><span class="bot-target-name">研发群<\/span>/);assert.match(list,/尚未选择接收会话/);
assert.match(list,/已启用/);assert.match(list,/已停用/);assert.match(list,/消息权限可用/);
assert.match(list,/&lt;夜间&gt;/);
assert.equal((list.match(/class="bot-glyph"><span class="bot-icon" data-bot-icon="robot"/g)||[]).length,2,'each row leads with a robot glyph');
assert.match(list,/class="bot-row-sub">glm-5.3-flash-ioa</,'model falls back to the default');
assert.equal((list.match(/data-bot-delete=/g)||[]).length,2);
assert.match(list,/class="hui-icon-btn" data-bot-edit="b1" aria-label="编辑 研发通知" title="编辑"/);
assert.match(list,/class="hui-icon-btn is-danger" data-bot-delete="b1" aria-label="删除 研发通知" title="删除"/);
assert.doesNotMatch(list,/bot-btn-label|bot-bar|data-bot-new/,'list has no text buttons, explainer bar or inline add');
assert.ok(!list.includes('bot-guide'));
assert.match(ctx.botRowHTML({id:'b3',name:'对话',model:'gpt-6.1-sol',interactionEnabled:true,enabled:true}),/gpt-6.1-sol · 已启用对话/);

ctx.state={authRequired:true,currentUser:{id:1}};ctx.project={name:'AgentFlow',ownerId:9,members:[{userId:1,role:'developer'}]};
const limited=ctx.botListHTML(bots);
assert.match(limited,/data-bot-delete="b1"/,'creator keeps delete');
assert.doesNotMatch(limited,/data-bot-delete="b2"/,'non-manager loses delete');
ctx.project={name:'AgentFlow',ownerId:9,members:[{userId:1,role:'viewer'}]};
assert.match(ctx.botListHTML([]),/data-bot-new disabled/,'viewer cannot add');
ctx.state={authRequired:false};

const guide=ctx.botListHTML([]);
assert.match(guide,/class="bot-guide"/);assert.ok(!guide.includes('<table'));
assert.match(guide,/① 填写凭据/);assert.match(guide,/② 选择接收会话/);assert.match(guide,/③ 发送测试/);
assert.match(guide,/data-bot-new >/);assert.match(guide,/开始添加/);assert.match(guide,/当前项目：AgentFlow/);
assert.equal((guide.match(/button primary/g)||[]).length,1,'the guide holds the only solid button');

const tabs=ctx.botTabsHTML('records',3);
assert.match(tabs,/class="bot-tabs"/);assert.match(tabs,/class="active" data-bot-tab="records"/);assert.doesNotMatch(tabs,/class="active" data-bot-tab="list"/);
assert.deepEqual([...tabs.matchAll(/data-bot-tab="(\w+)"/g)].map(m=>m[1]),['list','records'],'editing is not a tab');
assert.match(tabs,/<span>机器人<\/span><span class="bot-tab-count sliding-tab-count" data-bot-count>3</);
assert.doesNotMatch(ctx.botTabsHTML('list',0),/data-bot-count/,'zero count is hidden');
assert.match(ctx.botTabsHTML('edit',2),/aria-selected="true" class="active" data-bot-tab="list"/,'edit falls back to the list tab');
assert.doesNotMatch(tabs,/添加 \/ 编辑|data-bot-icon/);

const head=vm.runInContext('botUI.tab="list";botUI.loading=false;botUI.items=[];botPanelHeadHTML()',ctx);
assert.match(head,/class="bot-panel-head"><div class="bot-tabs"/);
assert.match(head,/class="hui-icon-btn" data-bot-refresh title="刷新" aria-label="刷新"/);
vm.runInContext('botUI.items=[{id:"b1",name:"研发通知"}];botUI.selected={id:"b1",name:"草稿名"};botUI.tab="edit";botUI.busy=false',ctx);
const crumb=ctx.botPanelHeadHTML();
assert.match(crumb,/class="bot-panel-head is-crumb"><nav class="bot-crumbs" aria-label="位置">/);
assert.match(crumb,/data-bot-back >[\s\S]*data-bot-icon="robot"[\s\S]*<span>机器人<\/span>[\s\S]*data-bot-icon="chevron-right"/);
assert.match(crumb,/aria-current="page">研发通知</,'crumb shows the saved name');
assert.match(crumb,/id="botDelete"[^>]*>[\s\S]*删除 Bot/);
assert.doesNotMatch(crumb,/data-bot-tab/);
vm.runInContext('botUI.busy=true',ctx);
assert.match(ctx.botPanelHeadHTML(),/data-bot-back disabled/,'busy editor cannot leave');
vm.runInContext('botUI.busy=false;botUI.selected={name:""}',ctx);
const fresh=ctx.botPanelHeadHTML();
assert.match(fresh,/aria-current="page">新建 Bot</);assert.doesNotMatch(fresh,/botDelete/,'unsaved bot has no delete');
ctx.state={authRequired:true,currentUser:{id:5}};ctx.project={ownerId:9,members:[]};
vm.runInContext('botUI.selected={id:"b1",name:"研发通知",ownerId:2}',ctx);
assert.doesNotMatch(ctx.botPanelHeadHTML(),/botDelete/,'non-manager has no delete');
ctx.state={authRequired:false};
vm.runInContext('botUI.selected={id:"b1",name:"研发通知"};botUI.step=1',ctx);
const editor=ctx.botEditTabHTML(vm.runInContext('botUI.selected',ctx));
assert.doesNotMatch(editor,/<h2|返回列表|bot-edit-head/,'editor has no duplicate title or back link');
assert.equal((editor.match(/button primary/g)||[]).length,1,'one solid button in the editor');
assert.equal((editor.match(/type="checkbox" role="switch"/g)||[]).length,2,'switches stay native checkboxes');
assert.match(ctx.botTestPaneHTML({id:'b1',name:'x',enabled:true,target:{type:'group',name:'g'}},true),/data-bot-back >完成，返回列表/);
assert.match(ctx.botEditTabHTML(null),/bot-guide|bot-table/,'no selection falls back to the list');
vm.runInContext('botUI.tab="list";botUI.selected=null;botUI.items=[]',ctx);

const records=ctx.botRecordTabsHTML('interactions');
assert.match(records,/class="bot-record-tabs"/);
assert.match(records,/class="active" data-bot-records="interactions"/);
assert.match(records,/class="" data-bot-records="deliveries"/);
assert.match(ctx.botRecordTabsHTML('deliveries'),/class="active" data-bot-records="deliveries"/);

const steps=vm.runInContext('botUI.step=2;botStepsHTML({id:"b1"},2)',ctx);
assert.match(steps,/class="bot-step done" data-bot-step="1"/);
assert.match(steps,/data-bot-icon="check-draw"/);
assert.match(steps,/class="bot-step active" data-bot-step="2"/);
assert.match(steps,/data-bot-step="3" disabled/);

for(const status of ['pending','sending'])assert.match(ctx.botStatusBadge(status),/hui-lamp/,status+' breathes');
for(const status of ['sent','failed','target_unavailable','cancelled'])assert.doesNotMatch(ctx.botStatusBadge(status),/hui-lamp/,status+' stays still');
assert.match(ctx.botStatusBadge('sent'),/hui-badge" data-size="sm" data-tone="success"/);assert.match(ctx.botStatusBadge('failed'),/hui-badge" data-size="sm" data-tone="error"/);assert.match(ctx.botStatusBadge('pending'),/data-tone="info"[\s\S]*等待发送/);
assert.match(ctx.botConnectionBadge('reconnecting'),/hui-lamp[\s\S]*重连中/);assert.doesNotMatch(ctx.botConnectionBadge('connected'),/hui-lamp/);
assert.match(ctx.botMessageBadge('running'),/hui-lamp/);assert.doesNotMatch(ctx.botMessageBadge('replied'),/hui-lamp/);
const glyph=html=>(html.match(/data-bot-icon="([\w-]+)"/)||[])[1]||(html.includes('d="M6 12h12"')?'minus':'');
assert.equal(glyph(ctx.botEnabledBadge(true)),'check');assert.equal(glyph(ctx.botEnabledBadge(false)),'minus');
assert.equal(glyph(ctx.botHealthBadge('ready')),'check');assert.equal(glyph(ctx.botHealthBadge('unchecked')),'clock');
assert.equal(glyph(ctx.botHealthBadge('awaiting_authorization')),'user');assert.match(ctx.botHealthBadge('awaiting_authorization'),/data-tone="warn"/);
assert.equal(glyph(ctx.botHealthBadge('error')),'alert');assert.match(ctx.botHealthBadge('error'),/data-tone="error"/);
assert.equal(glyph(ctx.botStatusBadge('sent')),'check');assert.equal(glyph(ctx.botStatusBadge('failed')),'alert');assert.equal(glyph(ctx.botStatusBadge('target_unavailable')),'alert');
assert.equal(glyph(ctx.botStatusBadge('awaiting_authorization')),'user');assert.equal(glyph(ctx.botStatusBadge('cancelled')),'close');assert.equal(glyph(ctx.botStatusBadge('expired')),'clock');
for(const status of ['pending','sending'])assert.equal(glyph(ctx.botStatusBadge(status)),'',status+' shows the lamp, not a glyph');
assert.equal(glyph(ctx.botConnectionBadge('offline')),'minus');assert.equal(glyph(ctx.botMessageBadge('reply_failed')),'alert');
vm.runInContext('botUI.items=[{id:"b1",name:"x"}]',ctx);
const retry=ctx.botDeliveryActionHTML({id:'d1',botId:'b1',status:'failed'});
assert.match(retry,/class="button small hui-btn-soft" data-delivery-retry="d1"/);
assert.equal(ctx.botDeliveryActionHTML({id:'d2',botId:'b1',status:'sent'}),'','retry only for retryable states');
vm.runInContext('botUI.deliveries=[];botUI.recordsTab="deliveries"',ctx);
const deliveries=ctx.botRecordsHTML();
assert.match(deliveries,/<div class="bot-subbar"><div class="bot-record-tabs"[\s\S]*<span class="bot-subbar-hint">保留最近 100 条<\/span>/);
vm.runInContext('botUI.items=[];botUI.deliveries=[]',ctx);
const source=fs.readFileSync('app/bots.js','utf8');
assert.doesNotMatch(source,/class="(?:err|ok)"/);
assert.doesNotMatch(source,/botInteractionHTML|bot-layout|bot-history/,'form no longer embeds the conversation block');

vm.runInContext('botUI.motion=null;botMotion("tab",1);botMotion("rows")',ctx);
assert.deepEqual(JSON.parse(vm.runInContext('JSON.stringify(botUI.motion)',ctx)),{tab:1,rows:true});

const css=fs.readFileSync('bots.css','utf8');
assert.match(css,/@media\(prefers-reduced-motion:reduce\)\{[^}]*#botsContent \*[^}]*animation:none!important;transition:none!important/);
for(const token of ['--bot-flow:cubic-bezier(.16,1,.3,1)','--bot-spring:cubic-bezier(.22,1.15,.36,1)','--bot-fold:cubic-bezier(.22,1,.36,1)','--bot-exit:cubic-bezier(.4,0,1,1)','--bot-t-hover:160ms','--bot-t-panel:280ms','--bot-t-fold:340ms','--bot-t-indicator:440ms'])assert.ok(css.includes(token),token);
assert.equal((css.match(/cubic-bezier/g)||[]).length,4,'curves are only declared as tokens');
for(const name of ['bot-in-r','bot-row-in','bot-shimmer','bot-bump'])assert.match(css,new RegExp('animation:'+name+' var\\(--bot-t-'));
assert.doesNotMatch(css,/\.bot-layout|\.bot-history/);
assert.match(css,/@media\(max-width:680px\)\{[\s\S]*\.bot-col-sub\{display:none\}/);
assert.doesNotMatch(css,/\.page-header|\.bot-edit-head/,'Bot page uses the shared page heading');
assert.doesNotMatch(css,/--hui-[a-z-]+:/,'Bot page reads the shared tokens instead of declaring them');
const sharedCSS=fs.readFileSync('app/harness-ui.css','utf8');
assert.match(sharedCSS,/\.hui-lamp::after \{[^}]*animation: huiBreath /,'live badges breathe through the shared lamp');
for(const token of ['--hui-accent: #7b61ff','--hui-text: #252529','--hui-border: #eae9ee','--hui-success: #338768','--hui-error: #c15161'])assert.ok(sharedCSS.includes(token),token);
assert.match(sharedCSS,/html\[data-theme="dark"\] \{[^}]*--hui-accent: #a08aff[^}]*--hui-surface: #141417/,'dark tokens');
assert.match(css,/#botsContent \.bot-record-tabs\.sliding-tabs\[data-sliding-tabs\]>\[data-sliding-indicator\]\{[^}]*height:2px!important[^}]*background:var\(--hui-accent\)/,'records use an underline');
assert.match(css,/\.bot-list-table tbody tr:hover \.bot-row-actions,\.bot-list-table tbody tr:focus-within \.bot-row-actions\{opacity:1\}/);
assert.doesNotMatch(css,/\.bot-check input\[type="checkbox"\]\{appearance:none/,'bot switches use the global Harness switch');
assert.match(fs.readFileSync('app/bots.js','utf8'),/<label class="bot-check"><input name="enabled" type="checkbox" role="switch"/);
assert.match(fs.readFileSync('app/hui-controls.css','utf8'),/input\[type="checkbox"\]:is\(\[role="switch"\], \.hui-switch\)[^{]*\{[^}]*width: 34px;[^}]*height: 20px;/,'switches are 34x20');
assert.doesNotMatch(css,/:hover\{[^}]*transform:translate/,'hover never moves elements');
const html=fs.readFileSync('index.html','utf8'),view=html.slice(html.indexOf('<section id="botsView"'),html.indexOf('</section>',html.indexOf('<section id="botsView"')));
assert.match(view,/<div class="page-heading bot-heading">/);assert.match(view,/<h1>通道管理<\/h1>/);
assert.match(view,/<button id="botNew" class="button primary" type="button">[\s\S]*添加通道<\/button>/);
assert.doesNotMatch(view,/eyebrow|page-header|loadBots\(\)/,'no eyebrow and no heading refresh button');
assert.doesNotMatch(css,/\.bot-tabs[^{]*\{[^}]*max-width:520px/,'bot tabs shrink to content');
assert.ok(css.includes('#botsContent .bot-tabs.sliding-tabs[data-sliding-tabs]{display:inline-flex!important;width:auto;max-width:100%!important;align-self:flex-start'),'bot tabs are an inline segmented control');
assert.ok(css.includes('#botsContent .bot-tabs.sliding-tabs[data-sliding-tabs]>[data-sliding-tab]{flex:0 0 auto;'),'bot tab items do not stretch');
assert.match(css,/@media\(max-width:680px\)\{[\s\S]*#botsContent \.bot-tabs \.bot-icon\{display:none\}/,'narrow bot tabs fit without clipping');
const tabsSource=fs.readFileSync('app/sliding-tabs.js','utf8');
assert.ok(tabsSource.includes("['.bot-tabs', '.active']"));assert.ok(tabsSource.includes("['.bot-record-tabs', '.active']"));
assert.doesNotMatch(css,/animation-delay:calc\([^)]*\d+ms/,'stagger delays come from tokens');
assert.match(tabs,/role="tab" aria-selected="true" class="active" data-bot-tab="records"/);
assert.match(tabs,/role="tab" aria-selected="false" class="" data-bot-tab="list"/);
assert.match(records,/role="tab" aria-selected="true" class="active" data-bot-records="interactions"/);
`
	cmd := exec.Command(node, "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bot tabs: %v %s", err, out)
	}
}

func TestBotHeadingAddButtonState(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');
const add={hidden:false,disabled:false,onclick:null};
const ctx={escapeHTML:String,isPlatformAdmin:()=>false,currentProject:()=>ctx.project,project:{ownerId:9,members:[]},state:{authRequired:false,projectFilter:'p1'},$:sel=>sel==='#botNew'?add:null};
vm.createContext(ctx);vm.runInContext(fs.readFileSync('app/bots.js','utf8'),ctx);
const paint=()=>vm.runInContext('renderBotHeading()',ctx);
paint();assert.equal(add.hidden,true,'empty list hides the heading button; the guide offers 开始添加');
vm.runInContext('botUI.items=[{id:"b1",name:"x"}]',ctx);paint();
assert.equal(add.hidden,false);assert.equal(add.disabled,false);assert.equal(typeof add.onclick,'function');
vm.runInContext('botUI.tab="edit";botUI.selected={name:"x"}',ctx);paint();assert.equal(add.hidden,true,'editing hides it so unsaved edits are not discarded');
vm.runInContext('botUI.selected={name:""}',ctx);paint();assert.equal(add.hidden,true,'creating hides it too');
vm.runInContext('botUI.tab="records"',ctx);paint();assert.equal(add.hidden,false,'records keeps it');
vm.runInContext('botUI.tab="list";botUI.selected=null;botUI.items=[];botUI.loading=true',ctx);paint();
assert.equal(add.hidden,false,'loading keeps it in place');assert.equal(add.disabled,true,'loading disables');
vm.runInContext('botUI.loading=false;botUI.items=[{id:"b1",name:"x"}]',ctx);
vm.runInContext('botUI.busy=true',ctx);paint();assert.equal(add.disabled,true,'busy disables');
vm.runInContext('botUI.busy=false',ctx);ctx.state={authRequired:true,currentUser:{id:1},projectFilter:'p1'};ctx.project={ownerId:9,members:[{userId:1,role:'viewer'}]};
paint();assert.equal(add.disabled,true,'viewer cannot add');
ctx.state={authRequired:false,projectFilter:''};paint();assert.equal(add.hidden,true,'no project, no add');
console.log('BOT_HEADING_OK');
`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "BOT_HEADING_OK") {
		t.Fatalf("bot heading: %v %s", err, out)
	}
}

func TestBotDeleteClearsErrorAndIgnoresRepeat(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');
const calls=[];let fail=true,release;
const ctx={escapeHTML:String,state:{authRequired:false,projectFilter:'p1'},$:()=>null,toast(){},afConfirm:async()=>true,nodeBotCache:new Map(),
 api:async(url,opts)=>{calls.push(opts?.method);if(fail)throw new Error('删除失败（模拟）');await new Promise(r=>release=r)}};
vm.createContext(ctx);vm.runInContext(fs.readFileSync('app/bots.js','utf8'),ctx);
vm.runInContext('loadBots=()=>{};botUI.projectId="p1";botUI.items=[{id:"b2",name:"夜间"}]',ctx);
(async()=>{
 const bot=vm.runInContext('botUI.items[0]',ctx);
 await ctx.botDelete(bot,null);
 assert.equal(vm.runInContext('botUI.error',ctx),'删除失败（模拟）');
 assert.equal(vm.runInContext('botUI.items.length',ctx),1,'failed delete keeps the row');
 fail=false;
 const first=ctx.botDelete(bot,null);await new Promise(r=>setTimeout(r));
 await ctx.botDelete(bot,null);
 assert.equal(calls.length,2,'a repeated delete while one is in flight is ignored');
 release();await first;
 assert.equal(vm.runInContext('botUI.error',ctx),'','successful delete clears the earlier error');
 assert.equal(vm.runInContext('botUI.items.length',ctx),0);
 assert.equal(vm.runInContext('botUI.deletingId',ctx),'');
 console.log('BOT_DELETE_OK');
})().catch(e=>{console.error(e);process.exit(1)});
`
	cmd := exec.Command(node, "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "BOT_DELETE_OK") {
		t.Fatalf("bot delete: %v %s", err, out)
	}
}
