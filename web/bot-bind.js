(() => {
 const content=document.getElementById('content');
 const escape=s=>String(s||'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
 let token=new URLSearchParams(location.hash.slice(1)).get('request')||sessionStorage.getItem('wecomBindingToken')||'';
 if(token){sessionStorage.setItem('wecomBindingToken',token);history.replaceState(null,'',location.pathname);}
 async function request(path,body){const response=await fetch(path,{method:body?'POST':'GET',headers:body?{'Content-Type':'application/json'}:{},body:body?JSON.stringify(body):undefined,credentials:'same-origin',cache:'no-store'});const data=await response.json();if(!response.ok)throw new Error(data.error||'请求未完成');return data;}
 function failure(error){content.innerHTML=`<p class="error">${escape(error.message)}</p><p>请回到企业微信，向 Bot 发送“绑定账号”获取新链接。</p>`;}
 async function start(){
  if(!/^[a-f0-9]{64}$/.test(token))throw new Error('绑定链接无效');
  const auth=await request('/api/auth/current?return_url='+encodeURIComponent(location.origin+location.pathname));
  if(!auth.user||auth.user.isGuest){
   content.innerHTML='<p>请登录你自己的平台账号。登录后仍需由原企微发送者确认，绑定才会生效。</p><button id="login">登录平台</button><button class="secondary" id="local" hidden>本地验收登录</button>';
   document.getElementById('login').onclick=()=>{if(auth.loginUrl)location.href=auth.loginUrl;else failure(new Error('登录入口不可用'));};
   const local=await request('/api/auth/local').catch(()=>({enabled:false}));
   if(local.enabled){const button=document.getElementById('local');button.hidden=false;button.onclick=()=>request('/api/auth/local',{}).then(start).catch(failure);}
   return;
  }
  const data=await request('/api/bot-bindings/status',{token});
  if(data.status==='confirmed'){complete(data.loginName);return;}
  content.innerHTML=`<p>当前平台账号</p><p id="account">${escape(auth.user.chineseName)} · ${escape(data.loginName)}</p><p>绑定后，Bot 会使用此账号的个人凭据和项目权限执行你的指令。请确认这是你本人使用的账号。</p><button id="claim">确认平台账号</button><p><small>群里的其他人点击链接不会直接完成绑定。</small></p>`;
  document.getElementById('claim').onclick=async()=>{const button=document.getElementById('claim');button.disabled=true;try{const result=await request('/api/bot-bindings/claim',{token});if(result.status==='confirmed'){complete(result.loginName);return;}content.innerHTML=`<p>最后一步：由原企微发送者确认</p><p>复制下面的指令，发回提供绑定链接的 Bot。群聊请 @Bot，也可以在同一 Bot 的单聊里发送。</p><code id="command">${escape(result.command)}</code><p><button id="copy">复制确认指令</button></p><p>将关联到 <strong>${escape(result.loginName)}</strong>。请勿转发确认码。</p><small>等待企业微信确认，页面会自动更新…</small>`;document.getElementById('copy').onclick=()=>navigator.clipboard.writeText(result.command).then(()=>{document.getElementById('copy').textContent='已复制';}).catch(()=>{document.getElementById('copy').textContent='请选中上方指令复制';});poll(result.loginName);}catch(error){failure(error);}};
 }
 function complete(login){sessionStorage.removeItem('wecomBindingToken');content.innerHTML=`<h2>已完成关联</h2><p>当前企微账号已关联到 <strong>${escape(login)}</strong>。</p><p>请回到 Bot 重新发送刚才的请求。可在平台个人设置中解除绑定。</p><a class="button" href="/#/settings">查看个人设置</a>`;}
 function poll(login){const timer=setInterval(async()=>{try{const data=await request('/api/bot-bindings/status',{token});if(data.status==='confirmed'){clearInterval(timer);complete(login);}}catch(error){clearInterval(timer);failure(error);}},2500);}
 start().catch(failure);
})();
