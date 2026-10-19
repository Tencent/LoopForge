async function importMcpJsonFile(e){
  const file=e.target.files&&e.target.files[0];if(e.target)e.target.value='';
  if(!file)return;
  try{
    const data=JSON.parse(await file.text());
    const res=await api('/api/mcp-servers/import',{method:'POST',body:JSON.stringify(data)});
    (res.servers||[]).forEach(v=>{const i=(state.data.mcpServers||[]).findIndex(x=>x.id===v.id);if(i>=0)state.data.mcpServers[i]=v;else state.data.mcpServers.push(v)});
    state.assetType='mcpServers';state.selectedAsset=res.servers?.[0]?.id||state.selectedAsset;renderAssets();toast(`已导入 ${res.imported} 个 MCP`);
  }catch(err){toast(err.message||'JSON 无效')}
}
function closeRevisionHistory(){$('#revisionModal')?.classList.remove('open');state.revisionSelected=null}
async function openRevisionHistory(kind,id,name){
  if(!id)return toast('先保存再看历史');
  state.revisionKind=kind;state.revisionEntity=id;state.revisionSelected=null;
  $('#revisionTitle').textContent=`${name||id} 的版本历史`;
  const items=await api(kind==='workflow'?`/api/workflows/${encodeURIComponent(id)}/revisions`:`/api/agents/${encodeURIComponent(id)}/revisions`);
  state.revisionItems=items||[];
  $('#revisionList').innerHTML=items.length?items.map(r=>`<button type="button" class="revision-item" data-rev="${escapeHTML(r.id)}"><b>${escapeHTML(r.summary||r.name)}</b><small>v${r.version}${r.revision?` · #${r.revision}`:''} · ${new Date(r.createdAt).toLocaleString()}</small></button>`).join(''):'<p class="prop-hint">还没有历史。保存一次修改后才会留下上一版。</p>';
  $('#revisionDiff').textContent='选择一个历史版本，查看和当前的差异。';
  $('#revisionRestore').disabled=true;
  $('#revisionList').onclick=ev=>{const btn=ev.target.closest('[data-rev]');if(btn)showRevisionDiff(btn.dataset.rev)};
  $('#revisionModal').classList.add('open');
}
function showRevisionDiff(id){
  const rev=(state.revisionItems||[]).find(x=>x.id===id);if(!rev)return;
  state.revisionSelected=id;
  $$('#revisionList .revision-item').forEach(el=>el.classList.toggle('active',el.dataset.rev===id));
  let current={};
  if(state.revisionKind==='workflow')current=state.workflow||{};
  else current=(state.data.agents||[]).find(a=>a.id===state.revisionEntity)||{};
  let prev=rev.payload;if(typeof prev==='string'){try{prev=JSON.parse(prev)}catch{prev={}}}
  $('#revisionDiff').textContent=lineDiff(JSON.stringify(prev,null,2),JSON.stringify(current,null,2));
  $('#revisionRestore').disabled=false;
}
function lineDiff(oldText,newText){
  const oldLines=oldText.split('\n'),newLines=newText.split('\n'),oldSet=new Set(oldLines),newSet=new Set(newLines),out=[];
  oldLines.forEach(l=>{if(!newSet.has(l))out.push('- '+l)});
  newLines.forEach(l=>{if(!oldSet.has(l))out.push('+ '+l)});
  return out.join('\n')||'与当前版本没有可见差异';
}
async function restoreSelectedRevision(){
  const id=state.revisionSelected;if(!id)return;
  if(!await afConfirm({title:'版本回退确认',message:'回退会把当前定义换成这一版，并再记一条新历史。已经跑完的 SOP 不受影响。继续？',confirmText:'确认回退',isDanger:true}))return;
  try{
    const kind=state.revisionKind,entity=state.revisionEntity;
    const saved=await api(kind==='workflow'?`/api/workflows/${encodeURIComponent(entity)}/revisions/${encodeURIComponent(id)}/restore`:`/api/agents/${encodeURIComponent(entity)}/revisions/${encodeURIComponent(id)}/restore`,{method:'POST',body:'{}'});
    if(kind==='workflow'){state.workflow=clone(saved);const i=state.data.workflows.findIndex(w=>w.id===saved.id);if(i>=0)state.data.workflows[i]=clone(saved);markWorkflowClean();renderAll()}
    else{const i=state.data.agents.findIndex(a=>a.id===saved.id);if(i>=0)state.data.agents[i]=saved;else state.data.agents.push(saved);selectAgent(saved,true)}
    closeRevisionHistory();toast('已回退到所选版本');
  }catch(err){toast(err.message)}
}
