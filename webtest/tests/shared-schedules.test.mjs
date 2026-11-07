import test from 'node:test';
import assert from 'node:assert/strict';
import {loadApp,settle} from '../lib/load-app.mjs';

const definition=(version=1)=>({id:'shared-inspect',version,sourceProjectId:'other',sourceProjectName:'基础团队',sourceJobId:'source',name:'需求停滞巡检',description:'跨项目复用巡检规则',prompt:version===1?'检查 {{params.days}} 天未更新的需求':'检查 {{params.team}} 超过 {{params.days}} 天未更新的需求',model:'glm-5.3-flash-ioa',intervalSec:3600,changeNote:version===1?'首次发布':'新增业务名称',parameters:[{name:'days',label:'停滞天数',type:'integer',required:true,default:'3'},...(version===1?[]:[{name:'team',label:'业务名称',type:'string',required:true,default:'pay'}])]});

test('subscribes to an explicit version, previews parameters and upgrades without copying prompt or local settings',async t=>{
 const app=await loadApp();t.after(app.close);
 const v1=definition(),v2=definition(2);let jobs=[];
 app.router.get('/api/schedule-jobs',()=>jobs);
 app.router.get('/api/shared-schedule-tasks',[v1]);
 app.router.get('/api/shared-schedule-tasks/shared-inspect/versions',[v2,v1]);
 app.router.post('/api/schedule-jobs',call=>{const job={...call.body,id:'subscription-1'};jobs.push(job);return job});
 app.router.put('/api/schedule-jobs',call=>{jobs=[call.body];return call.body});
 await app.evaluate("selectAIScheduleTab('market')");
 assert.match(app.document.querySelector('#sharedScheduleLibrary').textContent,/基础团队|需求停滞巡检/);
 app.document.querySelector('[data-subscribe-shared]').click();await settle(5);
 const form=app.document.querySelector('#scheduleJobForm');
 assert.equal(form.elements.sharedTaskId.value,v1.id);assert.equal(form.elements.sharedTaskVersion.value,'1');
 assert.equal(form.elements.assistantPrompt.required,false);assert.equal(form.querySelector('[data-schedule-prompt]').hidden,true);
 assert.equal(form.querySelector('[data-shared-param=days]').value,'3');
 form.querySelector('[data-shared-param=days]').value='7';form.querySelector('[data-shared-param=days]').dispatchEvent(new app.window.Event('input'));
 assert.match(app.document.querySelector('#sharedScheduleBinding pre').textContent,/7 天/);
 form.elements.intervalSec.value='900';form.elements.assistantModel.value='local-model';
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle(5);
 const saved=app.router.calls.find(c=>c.path==='/api/schedule-jobs'&&c.method==='POST');
 assert.equal(saved.body.assistantPrompt,'');assert.equal(saved.body.sharedTaskVersion,1);assert.deepEqual(saved.body.parameterValues,{days:'7'});
 assert.equal(saved.body.intervalSec,900);assert.equal(saved.body.assistantModel,'local-model');
 assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'),false);
 assert.match(app.document.querySelector('#sharedScheduleLibrary').textContent,/本项目已订阅 1 个|继续添加/);
 // Catalog refresh announces new releases but does not mutate the saved version.
 app.router.get('/api/shared-schedule-tasks',[v2]);await app.evaluate('refreshAISchedules()');
 assert.match(app.document.querySelector('#aiScheduleJobList').textContent,/有新版 v2/);
 assert.equal(form.elements.sharedTaskVersion.value,'1');
 await app.evaluate("openScheduleJobModal('assistant',state.data.scheduleJobs.find(j=>j.id==='subscription-1'))");
 await app.evaluate("openSharedScheduleVersions('shared-inspect',true)");
 assert.match(app.document.querySelector('#sharedScheduleVersionEditor').textContent,/当前 v1|新增业务名称/);
 assert.equal(app.document.querySelectorAll('.modal.open').length,1);
 assert.equal(app.document.querySelector('#sharedScheduleVersionModal').classList.contains('open'),false);
 assert.equal(form.querySelector('[type=submit]').textContent,'保存并升级到 v2');
 assert.equal(form.elements.sharedTaskVersion.value,'2');assert.equal(form.querySelector('[data-shared-param=days]').value,'7');
 assert.equal(form.querySelector('[data-shared-param=team]').value,'pay');
 assert.equal(form.elements.intervalSec.value,'900');assert.equal(form.elements.assistantModel.value,'local-model');
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle(5);
 const update=app.router.calls.find(c=>c.path==='/api/schedule-jobs'&&c.method==='PUT');
 assert.equal(update.body.sharedTaskVersion,2);assert.deepEqual(update.body.parameterValues,{days:'7',team:'pay'});
 assert.equal(update.body.assistantPrompt,'');assert.equal(update.body.intervalSec,900);
 // Switching back to an independent editor drops all binding fields.
 await app.evaluate("openScheduleJobModal('assistant')");
 assert.equal(form.elements.sharedTaskId.value,'');assert.equal(form.elements.assistantPrompt.required,true);
 assert.equal(app.document.querySelector('#sharedScheduleBinding').hidden,true);
});

test('publishes only task definitions with version concurrency and editable parameter schema',async t=>{
 const app=await loadApp();t.after(app.close);
 const source={id:'source-job',projectId:'project-1',name:'巡检',skipDispatch:true,assistantPrompt:'检查需求',ownerUserId:99,notifications:[{botId:'private-bot'}],lastAssistantMessage:'private execution'};
 app.evaluate(`state.data.scheduleJobs=${JSON.stringify([source])};renderScheduleJobs()`);
 app.router.get('/api/shared-schedule-tasks',[]);
 app.router.post('/api/schedule-jobs/source-job/publish',call=>({...definition(),sourceProjectId:'project-1',sourceJobId:'source-job',...call.body,version:1}));
 app.document.querySelector('[data-publish-schedule]').click();await settle(4);
 const form=app.document.querySelector('#sharedSchedulePublishForm');
 form.elements.prompt.value='检查 {{params.days}} 天未更新的需求';
 app.document.querySelector('#sharedScheduleAddParameter').click();
 const row=form.querySelector('.shared-schedule-parameter-row');
 row.querySelector('[data-param-field=name]').value='days';row.querySelector('[data-param-field=label]').value='停滞天数';
 row.querySelector('[data-param-field=type]').value='integer';row.querySelector('[data-param-field=required]').checked=true;row.querySelector('[data-param-field=default]').value='3';
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle(5);
 const call=app.router.calls.find(c=>c.path.endsWith('/publish')&&c.method==='POST');
 assert.equal(call.body.baseVersion,0);assert.equal(call.body.parameters[0].name,'days');assert.equal(call.body.parameters[0].required,true);
 assert.deepEqual(Object.keys(call.body).sort(),['baseVersion','changeNote','description','name','parameters','prompt']);
 assert.doesNotMatch(JSON.stringify(call.body),/private|ownerUserId/);
 app.router.get('/api/shared-schedule-tasks',[{...definition(),sourceProjectId:'project-1',sourceJobId:'source-job'}]);
 await app.evaluate("openSharedSchedulePublish('source-job')");assert.equal(form.elements.baseVersion.value,'1');
 assert.match(form.querySelector('[type=submit]').textContent,/v2/);
 app.router.post('/api/schedule-jobs/source-job/publish',{status:409,body:{error:'已有新版本，请刷新'}});
 form.dispatchEvent(new app.window.Event('submit',{bubbles:true,cancelable:true}));await settle(4);
 assert.match(app.document.querySelector('#sharedSchedulePublishError').textContent,/已有新版本/);
 assert.equal(form.querySelector('[type=submit]').disabled,false);
});

test('missing version prevents destructive saves and stale version dialogs cannot change another editor',async t=>{
 const app=await loadApp();t.after(app.close);
 const job={id:'sub',projectId:'project-1',name:'Subscription',skipDispatch:true,sharedTaskId:'missing',sharedTaskVersion:1};
 app.router.get('/api/shared-schedule-tasks/missing/versions',{status:404,body:{error:'missing version'}});
 await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify(job)})`);
 const form=app.document.querySelector('#scheduleJobForm');assert.equal(form.querySelector('[type=submit]').disabled,true);
 assert.match(app.document.querySelector('#sharedScheduleBinding').textContent,/读取订阅失败/);
 const v1=definition(),v2=definition(2);
 app.evaluate(`rememberSharedScheduleVersions(${JSON.stringify([v1])})`);
 await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify({...job,sharedTaskId:v1.id})})`);
 let resolveVersions;app.router.get('/api/shared-schedule-tasks/shared-inspect/versions',()=>new Promise(resolve=>{resolveVersions=resolve}));
 const loading=app.evaluate("openSharedScheduleVersions('shared-inspect',true)");await settle();
 await app.evaluate("openScheduleJobModal('assistant')");resolveVersions([v2,v1]);await loading;
 assert.equal(form.elements.sharedTaskId.value,'');assert.equal(app.document.querySelector('#sharedScheduleVersionEditor').hidden,true);
 assert.equal(app.document.querySelector('#sharedScheduleVersionModal').classList.contains('open'),false);
});

test('shared market is inline, searchable, and supports multiple project-local subscriptions',async t=>{
 const app=await loadApp();t.after(app.close);
 const v1=definition(),other={...definition(),id:'shared-report',name:'每日交付报告',description:'汇总完成情况',sourceProjectName:'交付团队'};
 app.router.get('/api/shared-schedule-tasks',[v1,other]);
 app.evaluate(`state.data.scheduleJobs=[{id:'other-project',projectId:'elsewhere',skipDispatch:true,sharedTaskId:'shared-inspect'},{id:'local-a',projectId:'project-1',skipDispatch:true,sharedTaskId:'shared-inspect'},{id:'local-b',projectId:'project-1',skipDispatch:true,sharedTaskId:'shared-inspect'}]`);
 app.document.querySelector('#aiScheduleMarketTab').click();await settle(5);
 assert.equal(app.document.querySelector('#sharedScheduleLibraryModal'),null);
 assert.equal(app.document.querySelector('#aiScheduleMarketPanel').hidden,false);
 assert.equal(app.document.querySelector('#aiScheduleProjectPanel').hidden,true);
 assert.equal(app.document.querySelector('#aiScheduleMarketTab').getAttribute('aria-selected'),'true');
 assert.equal(app.document.querySelector('#sharedScheduleMarketCount').textContent,'2');
 assert.equal(app.document.querySelector('#sharedScheduleLibrary').querySelectorAll('.mcp-market-card').length,2);
 assert.match(app.document.querySelector('#sharedScheduleLibrary').textContent,/本项目已订阅 2 个/);
 assert.equal(app.document.querySelector('[data-subscribe-shared="shared-inspect"]').disabled,false);
 const search=app.document.querySelector('#sharedScheduleSearch');search.value='交付团队';search.dispatchEvent(new app.window.Event('input'));
 assert.equal(app.document.querySelectorAll('#sharedScheduleLibrary .mcp-market-card').length,1);
 assert.match(app.document.querySelector('#sharedScheduleLibrary').textContent,/每日交付报告/);
 search.value='<nothing>';search.dispatchEvent(new app.window.Event('input'));assert.match(app.document.querySelector('#sharedScheduleLibrary').textContent,/没有匹配/);
 search.value='';search.dispatchEvent(new app.window.Event('input'));
 app.evaluate("state.projectFilter='elsewhere';renderScheduleJobs()");assert.match(app.document.querySelector('#sharedScheduleLibrary').textContent,/本项目已订阅 1 个/);
 app.evaluate("state.projectFilter='';renderScheduleJobs()");assert.equal(app.document.querySelector('[data-subscribe-shared]').disabled,true);
 app.document.querySelector('#aiScheduleProjectTab').click();await settle();assert.equal(app.document.querySelector('#aiScheduleMarketPanel').hidden,true);
 assert.equal(app.document.querySelector('#sharedScheduleSearchField').hidden,true);
});

test('market reports loading failures and retry fetches current definitions',async t=>{
 const app=await loadApp();t.after(app.close);
 app.router.get('/api/shared-schedule-tasks',{status:500,body:{error:'market unavailable'}});
 await app.evaluate("selectAIScheduleTab('market')");
 assert.match(app.document.querySelector('#sharedScheduleMarketStatus').textContent,/market unavailable/);
 assert.equal(app.document.querySelector('#sharedScheduleLibrary').getAttribute('aria-busy'),'false');
 app.router.get('/api/shared-schedule-tasks',[definition()]);app.document.querySelector('[data-retry-shared]').click();await settle(4);
 assert.equal(app.document.querySelector('#sharedScheduleMarketStatus').textContent,'');
 assert.match(app.document.querySelector('#sharedScheduleLibrary').textContent,/需求停滞巡检/);
});


test('highlighted update opens one editor and version changes preserve parameter drafts and local config',async t=>{
 const app=await loadApp();t.after(app.close);
 const v1=definition(),v2=definition(2),job={id:'upgrade-me',name:'本项目巡检',projectId:'project-1',skipDispatch:true,sharedTaskId:v1.id,sharedTaskVersion:1,parameterValues:{days:'7'},intervalSec:900,assistantModel:'local-model',enabled:true};
 app.router.get('/api/schedule-jobs',[job]);app.router.get('/api/shared-schedule-tasks',[v2]);app.router.get('/api/shared-schedule-tasks/shared-inspect/versions',[v2,v1]);
 await app.evaluate('refreshAISchedules()');
 const update=app.document.querySelector('[data-shared-versions]');assert.equal(update.classList.contains('hui-btn-soft'),true);
 update.click();await settle(6);
 const f=app.document.querySelector('#scheduleJobForm'),selector=()=>app.document.querySelector('#sharedScheduleVersionEditor select');
 assert.equal(app.document.querySelectorAll('.modal.open').length,1);assert.equal(f.elements.sharedTaskVersion.value,'2');
 assert.equal(f.elements.intervalSec.value,'900');assert.equal(f.elements.assistantModel.value,'local-model');
 f.querySelector('[data-shared-param=team]').value='custom-team';
 selector().value='1';selector().dispatchEvent(new app.window.Event('change'));assert.equal(f.elements.sharedTaskVersion.value,'1');
 selector().value='2';selector().dispatchEvent(new app.window.Event('change'));assert.equal(f.querySelector('[data-shared-param=team]').value,'custom-team');
 app.evaluate('closeScheduleJobModal()');assert.equal(app.evaluate('state.data.scheduleJobs[0].sharedTaskVersion'),1);
 assert.equal(app.router.calls.filter(c=>c.method==='PUT'&&c.path==='/api/schedule-jobs').length,0);
 // Catalog details still use a single read-only dialog without opening an editor.
 await app.evaluate("openSharedScheduleVersions('shared-inspect',false)");assert.equal(app.document.querySelectorAll('.modal.open').length,1);
 assert.equal(app.document.querySelector('#scheduleModal').classList.contains('open'),false);
 assert.match(app.document.querySelector('#sharedScheduleVersions').textContent,/新增业务名称/);
});

test('inline version errors can retry without losing edits or opening another dialog',async t=>{
 const app=await loadApp();t.after(app.close);
 const v1=definition(),v2=definition(2),job={id:'sub',name:'Subscription',skipDispatch:true,sharedTaskId:v1.id,sharedTaskVersion:1,parameterValues:{days:'9'}};
 app.evaluate(`rememberSharedScheduleVersions(${JSON.stringify([v1])})`);
 await app.evaluate(`openScheduleJobModal('assistant',${JSON.stringify(job)})`);
 app.router.get('/api/shared-schedule-tasks/shared-inspect/versions',{status:500,body:{error:'version service unavailable'}});
 await app.evaluate("openSharedScheduleVersions('shared-inspect',true)");assert.match(app.document.querySelector('#sharedScheduleVersionEditor').textContent,/version service unavailable/);
 assert.equal(app.document.querySelector('#scheduleJobForm [type=submit]').disabled,false);
 app.router.get('/api/shared-schedule-tasks/shared-inspect/versions',[v2,v1]);app.document.querySelector('[data-retry-shared-versions]').click();await settle(5);
 const f=app.document.querySelector('#scheduleJobForm');assert.equal(f.elements.sharedTaskVersion.value,'2');assert.equal(f.querySelector('[data-shared-param=days]').value,'9');
 assert.equal(app.document.querySelectorAll('.modal.open').length,1);
});
