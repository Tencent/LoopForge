(() => {
 const content=document.getElementById('content');
 const escape=s=>String(s||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 // The draft ID travels in the fragment; chat clients may append punctuation.
 const pick=v=>(String(v||'').toLowerCase().match(/[a-f0-9]{32}/)||[''])[0];
 const linked=pick(new URLSearchParams(location.hash.slice(1)).get('draft'));
 const id=linked||sessionStorage.getItem('botApproveDraft')||'';
 if(linked){sessionStorage.setItem('botApproveDraft',linked);history.replaceState(null,'',location.pathname);}
 async function request(path,method,body){const response=await fetch(path,{method:method||'GET',headers:method?{'Content-Type':'application/json'}:{},body:method?JSON.stringify(body||{}):undefined,credentials:'same-origin',cache:'no-store'});const data=await response.json().catch(()=>({}));if(!response.ok&&!data.draft)throw new Error(data.error||'请求未完成');return response.ok?data:Object.assign(data.draft,{notice:data.error});}
 function failure(error){content.innerHTML=`<p class="error">${escape(error.message)}</p><p>请回到聊天中重新发起请求。</p>`;}
 const labels={pending:'等待确认',executing:'执行中',executed:'已执行',failed:'部分或全部未完成',needs_review:'执行中断，待核对',expired:'已过期',cancelled:'已取消'};
 function meta(d){const rows=[['机器人',d.botName],['项目',d.projectName],['状态',labels[d.status]||d.status],['有效期至',d.status==='pending'?new Date(d.expiresAt).toLocaleString():'']].filter(([,v])=>v);return `<dl>${rows.map(([k,v])=>`<dt>${escape(k)}</dt><dd>${escape(v)}</dd>`).join('')}</dl>`;}
 function render(d){
  const ops=`<ol>${(d.operations||[]).map(o=>`<li>${escape(String(o).replace(/^\d+\.\s*/,''))}</li>`).join('')}</ol>`;
  const results=d.results?.length?`<p>执行结果</p><ol>${d.results.map(r=>`<li class="${/^已(完成|启动)/.test(r)?'ok':'error'}">${escape(r)}</li>`).join('')}</ol>`:'';
  const notice=d.notice?`<p class="error">${escape(d.notice)}</p>`:d.reason?`<p class="${d.status==='needs_review'?'error':''}">${escape(d.reason)}</p>`:'';
  if(d.status==='pending'){
   // New requirements: pick the SOP and whether to start it right away.
   // The first option is the project default (empty value), like the
   // requirement form on the platform.
   const options=i=>`<option value="" ${i.workflowId?'':'selected'}>项目默认 SOP（${escape(i.defaultWorkflowName||'按来源默认')}）</option>`+(d.workflows||[]).map(w=>`<option value="${escape(w.id)}" ${w.id===i.workflowId?'selected':''}>${escape(w.name)}</option>`).join('');
   // The repository: the project default first (empty value), then the
   // project's repositories. Shown when the project has any.
   const repos=d.repositories||[];
   const repoOptions=i=>`<option value="" ${i.repositoryId?'':'selected'}>${i.defaultRepositoryName?`项目默认仓库（${escape(i.defaultRepositoryName)}）`:'不绑定仓库（项目未设默认仓库）'}</option>`+repos.map(r=>`<option value="${escape(r.id)}" ${r.id===i.repositoryId?'selected':''}>${escape(r.name)}</option>`).join('');
   const repoField=i=>repos.length?`<label>仓库<br><select class="repo">${repoOptions(i)}</select></label>`:'';
   const sops=(d.newIssues||[]).length?`<p>新需求使用的${repos.length?'仓库和 ':''}SOP</p>${d.newIssues.map(i=>`<div class="sop" data-index="${i.index}"><strong>${escape(i.title)}</strong>${repoField(i)}<label>SOP<br><select class="wf">${options(i)}</select></label><label><input type="checkbox"> 创建后立即启动这个 SOP</label></div>`).join('')}`:'';
   content.innerHTML=`${meta(d)}<p>Bot 将使用你的平台账号和项目权限执行以下操作：</p>${ops}${sops}${notice}<p><button id="confirm">确认执行</button> <button class="secondary" id="cancel">取消</button></p><small>每个确认链接只能执行一次；重复点击不会重复执行。</small>`;
   const choice=el=>{const c={index:Number(el.dataset.index),workflowId:el.querySelector('select.wf').value,start:el.querySelector('input[type=checkbox]').checked};const repo=el.querySelector('select.repo');if(repo)c.repositoryId=repo.value;return c;};
   document.getElementById('confirm').onclick=()=>act('confirm',{choices:[...content.querySelectorAll('.sop')].map(choice)});
   document.getElementById('cancel').onclick=()=>act('cancel');
   return;
  }
  content.innerHTML=`${meta(d)}${ops}${notice}${results}${d.status==='executing'?'<small>正在执行，页面会自动刷新…</small>':''}<p><a class="button" href="/#/dashboard">返回平台</a></p>`;
  if(d.status==='executing')setTimeout(()=>load().catch(failure),2500);
 }
 async function act(kind,body){content.querySelectorAll('button').forEach(b=>b.disabled=true);try{render(await request(`/api/bot-drafts/${id}/${kind}`,'POST',body))}catch(error){failure(error)}}
 async function load(){render(await request(`/api/bot-drafts/${id}`))}
 async function start(){
  if(!/^[a-f0-9]{32}$/.test(id))throw new Error('确认链接无效');
  const auth=await request('/api/auth/current?return_url='+encodeURIComponent(location.origin+location.pathname));
  if(!auth.user||auth.user.isGuest){
   content.innerHTML='<p>请登录与聊天账号绑定的平台账号后确认。</p><button id="login">登录平台</button>';
   document.getElementById('login').onclick=()=>{if(auth.loginUrl)location.href=auth.loginUrl;else failure(new Error('登录入口不可用'));};
   return;
  }
  await load();
 }
 start().catch(failure);
})();
