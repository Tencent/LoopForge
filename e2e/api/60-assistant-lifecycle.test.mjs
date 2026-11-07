import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {api,get,post,put,del,list,track,cleanup,ensureProject,waitFor} from '../lib/api.mjs';
import {cfg,uid} from '../lib/env.mjs';

before(ensureProject);
after(cleanup);

test('assistant sessions: save, rename, fork, edit, conflict, archive, unarchive and delete',async()=>{
  const id=uid('session');
  track(`session ${id}`,()=>del(`/api/assistant/sessions/${id}`));
  const original=await put(`/api/assistant/sessions/${id}`,{projectId:cfg.projectId,title:'E2E original',messages:[{role:'user',content:'original question'},{role:'assistant',content:'original answer'}]});
  assert.equal(original.messages.length,2);
  const branch=await post(`/api/assistant/sessions/${id}/fork`,{messageIndex:0,content:'branch question',updatedAt:original.updatedAt});
  track(`session ${branch.id}`,()=>del(`/api/assistant/sessions/${branch.id}`));
  assert.equal(branch.parentSessionId,id);
  assert.equal(branch.messages[0].content,'branch question');
  assert.equal((await get(`/api/assistant/sessions/${id}`)).messages[0].content,'original question');
  const edited=await post(`/api/assistant/sessions/${id}/edit`,{messageIndex:0,content:'edited question',updatedAt:original.updatedAt});
  assert.equal(edited.messages.length,1);
  await api('POST',`/api/assistant/sessions/${id}/edit`,{messageIndex:0,content:'stale',updatedAt:original.updatedAt},{expect:409});
  await put(`/api/assistant/sessions/${id}`,{...edited,title:'E2E renamed'});
  await post(`/api/assistant/sessions/${id}/archive`,{archived:true});
  assert.equal(list(await get('/api/assistant/sessions')).find(s=>s.id===id).archived,true);
  await post(`/api/assistant/sessions/${id}/archive`,{archived:false});
  assert.equal((await get(`/api/assistant/sessions/${id}`)).title,'E2E renamed');
  await del(`/api/assistant/sessions/${id}`,{expect:200});
  await api('GET',`/api/assistant/sessions/${id}`,undefined,{expect:404});
});

test('assistant attachments: actual upload/download, empty and oversize rejection, references',async()=>{
  const form=new FormData();
  form.set('projectId',cfg.projectId);
  form.set('file',new Blob(['E2E attachment content\n']),'brief.txt');
  const attachment=await post('/api/assistant/attachments',form);
  assert.equal(attachment.name,'brief.txt');
  const downloaded=await api('GET',`/api/assistant/attachments/${attachment.id}`,undefined,{raw:true});
  assert.equal(downloaded.text,'E2E attachment content\n');
  assert.match(downloaded.headers.get('content-disposition'),/attachment/);
  assert.match(downloaded.headers.get('cache-control'),/no-store/);
  const empty=new FormData(); empty.set('projectId',cfg.projectId);empty.set('file',new Blob([]),'empty.txt');
  await api('POST','/api/assistant/attachments',empty,{expect:400});
  const huge=new FormData();huge.set('projectId',cfg.projectId);huge.set('file',new Blob([new Uint8Array(10*1024*1024+1)]),'huge.txt');
  await api('POST','/api/assistant/attachments',huge,{expect:413});
  assert.ok(await get(`/api/assistant/references?projectId=${cfg.projectId}`));
  // Attachments have no delete endpoint: this case retains one harmless 23-byte fixture.
});

test('real assistant scheduled prompt: manual trigger completes and saves the actual reply', {skip:!cfg.model,timeout:300_000},async()=>{
  const marker=uid('scheduled');
  const task=await put('/api/assistant/tasks',{name:marker,projectId:cfg.projectId,enabled:false,cron:'0 4 * * *',prompt:`自动化验收。禁止调用任何写工具或修改资源。只回复这段文字：${marker}`});
  track(`assistant task ${task.id}`,()=>del(`/api/assistant/tasks/${task.id}`));
  await post(`/api/assistant/tasks/${task.id}/run`,{});
  const done=await waitFor('scheduled prompt reply',async()=>{
    const t=list(await get(`/api/assistant/tasks?projectId=${cfg.projectId}`)).find(t=>t.id===task.id);
    return t?.lastRunAt && ['ok','error'].includes(t.lastStatus)?t:null;
  },{timeoutMs:270_000,intervalMs:2000});
  if(done.sessionId)track(`scheduled session ${done.sessionId}`,()=>del(`/api/assistant/sessions/${done.sessionId}`));
  assert.equal(done.lastStatus,'ok',done.lastError);
  assert.match(done.lastMessage,new RegExp(marker));
  assert.ok(done.sessionId);
  assert.match(JSON.stringify((await get(`/api/assistant/sessions/${done.sessionId}`)).messages),new RegExp(marker));
});

test('real durable generation: cancel persists, unlocks session and permits a fresh turn', {skip:!cfg.model,timeout:300_000},async()=>{
  const started=await post('/api/assistant/generations',{projectId:cfg.projectId,messages:[{role:'user',content:'自动化取消测试。请只用文字解释归并排序，不调用工具。'}]});
  const id=started.sessionId;
  track(`generation session ${id}`,async()=>{
    await post(`/api/assistant/sessions/${id}/generation/cancel`,{}, {expect:[200,404,409]});
    await del(`/api/assistant/sessions/${id}`,{expect:[200,404,409]});
  });
  await post(`/api/assistant/sessions/${id}/generation/cancel?generationId=${started.id}`,{});
  await waitFor('generation cancelled',async()=>{
    const g=await get(`/api/assistant/sessions/${id}/generation`);
    return g.status==='cancelled';
  },{timeoutMs:90_000,intervalMs:500});
  const prior=await get(`/api/assistant/sessions/${id}`);
  const next=await post('/api/assistant/generations',{sessionId:id,projectId:cfg.projectId,messages:[...prior.messages,{role:'user',content:'自动化恢复测试，只回复 RECOVERED，不调用任何工具。'}]});
  assert.notEqual(next.id,started.id);
  const done=await waitFor('new turn completed',async()=>{
    const g=await get(`/api/assistant/sessions/${id}/generation`);
    return g.status!=='running'?g:null;
  },{timeoutMs:180_000,intervalMs:2000});
  assert.equal(done.status,'completed',done.error);
  assert.match(JSON.stringify((await get(`/api/assistant/sessions/${id}`)).messages),/RECOVERED/);
});
