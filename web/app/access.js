// Khóa truy cập: tạo, thu hồi, và sao chép prompt kết nối MCP sau khi tạo.
function accessMcpClientSpec(client,base,token){
  const json=JSON.stringify({mcpServers:{agentflow:{type:'streamable-http',url:`${base}/mcp`,headers:{Authorization:`Bearer ${token}`}}}},null,2);
  const specs={
    cursor:{label:'Cursor',location:'~/.cursor/mcp.json',codeTitle:'mcp.json',code:json,verify:'Trong danh sách MCP của phần cài đặt Cursor, hãy xác nhận agentflow đã được kết nối'},
    claude:{label:'Claude Code',location:'Claude CLI (ghi cấu hình cấp người dùng vào ~/.claude.json)',codeTitle:'Shell',code:`claude mcp add --transport http --scope user agentflow ${base}/mcp \\\n  --header "Authorization: Bearer ${token}"`,verify:'claude mcp get agentflow && claude mcp list'},
    codebuddy:{label:'CodeBuddy',location:'~/.codebuddy/mcp.json',codeTitle:'mcp.json',code:json,verify:'Trong trang quản lý MCP của CodeBuddy, hãy xác nhận agentflow đã được kết nối'},
    codex:{label:'Codex',location:'~/.codex/config.toml',codeTitle:'config.toml',code:`[mcp_servers.agentflow]\nurl = "${base}/mcp"\nhttp_headers = { Authorization = "Bearer ${token}" }`,verify:'codex mcp list'}
  };
  return specs[client]||specs.cursor;
}
function accessMcpAgentPrompt(spec,base,token){
  if(!token||token==='<YOUR_PERSONAL_TOKEN>')return '';
  return `Hãy cài đặt, khởi tạo và kiểm tra LoopForge MCP theo quy trình này:\n\n1. Dùng curl để tải tệp quy trình cài đặt, không dùng fetch: ${base}/mcp-setup.txt\n2. Thay hai placeholder bằng:\n   API_TOKEN = ${token}\n   ENDPOINT = ${base}/mcp`;
}
// 对外展示的固定接入地址：优先使用部署下发的 APP_DOMAIN，避免硬编码固定域名。
function getPublicBaseURL(){
  const configured = (typeof window !== 'undefined' && (window.__AGENTFLOW_APP_DOMAIN__ || window.__AGENTFLOW_PUBLIC_BASE_URL__))
    || (typeof state !== 'undefined' && state?.appDomain ? state.appDomain : '');
  return accessPublicBaseURL(configured, typeof window !== 'undefined' ? window.location : null);
}
// 接入片段会携带 Bearer Token：配置里显式的协议优先，否则跟随当前页面；
// HTTPS 页面生成的地址不降级为 http://。只有配置显式写了 http:// 和非 443 端口时
// 保留 http，因为把只提供 HTTP 的端口改成 https:// 会指向一个没有 TLS 的端口。
function accessPublicBaseURL(configured, page){
  const secure = page?.protocol === 'https:';
  const fallback = (secure ? 'https:' : 'http:') + '//' + (page?.host || 'loopforge.example.com');
  const raw = String(configured || '').trim();
  if(!raw) return fallback;
  const explicitScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(raw);
  let url;
  try{ url = new URL(explicitScheme ? raw : (secure ? 'https://' : 'http://') + raw) }catch(_){ return fallback }
  if(!['http:','https:'].includes(url.protocol) || !url.host) return fallback;
  const explicitHTTPPort = explicitScheme && url.protocol === 'http:' && url.port && url.port !== '443';
  if(secure && !explicitHTTPPort) url.protocol = 'https:';
  return url.protocol + '//' + url.host + url.pathname.replace(/\/+$/, '');
}
function accessViewActive(){return !!$('#guideView')?.classList.contains('active')}
let credentialFocusRequest=0;
function gotoCredential(name,sshRemote){
  const request=++credentialFocusRequest;
  if(!CREDENTIAL_FIELDS[name]){switchView('settings');return}
  if(name==='gitSshKey')return openGitSSHKeys(sshRemote);
  if(name==='gitHttpCredentials')return openGitHTTPCredentials();
  switchView('settings');
  const requestedRoute=settingsRouteTransition;
  const focusAfter=performance.now()+150;
  const focusCredential=()=>{
    const transition=settingsRouteTransition;
    if(request!==credentialFocusRequest||(transition&&transition!==requestedRoute))return;
    if(transition&&transition.target!=='settings')return;
    if(activeViewName()!=='settings'){
      // The outgoing workspace drawer closes before settings is mounted.
      // A different route cancels this request instead of stealing focus later.
      if(transition===requestedRoute&&transition?.phase==='closing')requestAnimationFrame(focusCredential);
      return;
    }
    if(performance.now()<focusAfter){requestAnimationFrame(focusCredential);return}
    const panel=document.querySelector('#personalSettingsContent #credentialPanel'),row=panel?.querySelector(`[data-credential-row="${name}"]`);
    if(!row||!panel.getClientRects().length)return;
    // Missing credentials open the wizard on its fill-in step; configured ones expand in place.
    if(row.classList.contains('is-configured'))revealCredentialRow(name);
    else openCredentialWizard(name);
  };
  requestAnimationFrame(focusCredential);
}
// Personal credential fields and the errors they cause. Keep the rules in
// sync with chatbot/src/credentials.js.
const CREDENTIAL_FIELDS={codeBuddyApiKey:'CodeBuddy API Key',codexApiKey:'Codex API Key',claudeCodeApiKey:'Claude Code API Key',gitToken:'GitHub Git Token',gitSshKey:'Khóa Git SSH',gitHttpCredentials:'Git access token'};
const CREDENTIAL_RULES=[
  ['codexApiKey',/Codex[^。\n]{0,16}API Key/i],
  ['claudeCodeApiKey',/Claude Code[^。\n]{0,16}API Key/i],
  ['codeBuddyApiKey',/CodeBuddy[^。\n]{0,20}(API Key|Key|凭据)|拒绝了你的 API Key/i],
  ['gitSshKey',/Git SSH (?:密钥|私钥)/i],
  ['gitToken',/Git Token|Gongfeng[^。\n]{0,12}令牌|could not read Username|Authentication failed|HTTP Basic: Access denied|terminal prompts disabled|requested URL returned error: 40[13]/i],
];
function credentialHint(text){
  const message=String(text||'');
  if(!message)return null;
  const rule=CREDENTIAL_RULES.find(([,re])=>re.test(message));
  return rule?{field:rule[0],label:CREDENTIAL_FIELDS[rule[0]],owner:/责任人|Owner|该账号/i.test(message)}:null;
}
function credentialActionHTML(text){
  const hint=credentialHint(text);
  if(!hint)return '';
  return `<div class="credential-action"><button type="button" class="button ghost small" data-goto-credential="${hint.field}">Đi cấu hình ${escapeHTML(hint.label)} →</button>${hint.owner?'<small>Người phụ trách phải tự cấu hình trong cài đặt cá nhân</small>':''}</div>`;
}
document.addEventListener('click',e=>{
  const button=e.target.closest?.('[data-goto-credential]');
  if(button)gotoCredential(button.dataset.gotoCredential);
});
// renderAll() repaints every page, so only the active Khóa truy cập page may fetch
// the token list; Bearer sessions get 401 there and would poison the state.
function renderGuide(openCreate=false){if($('#accessBody')&&accessViewActive())renderAccessTokens(openCreate)}
// loadAccessTokens always settles accessTokensStatus, and only repaints when the
// user is still on Khóa truy cập, so a slow or failing request can never leave the
// loading placeholder on top of another page.
async function loadAccessTokens(){
  state.accessTokensStatus='loading';state.accessTokensError='';
  try{
    const list=await api('/api/auth/tokens',{silent:true});
    state.accessTokens=Array.isArray(list)?list:[];
    state.accessTokensStatus='ready';
  }catch(e){
    state.accessTokens=[];state.accessTokensStatus='error';state.accessTokensError=e.message||'Không tải được';
  }
  if(accessViewActive())renderAccessTokens();
}
function renderAccessTokens(openCreate=false){
  const body=$('#accessBody'),user=state.currentUser,isGuest=!user||user.isGuest,tokens=Array.isArray(state.accessTokens)?state.accessTokens:[];
  if(isGuest){
    body.innerHTML=`<div class="access-card access-tokens"><div class="token-empty"><b>Hãy đăng nhập trước</b><p>Khóa truy cập phải gắn với người dùng đã đăng nhập.</p><button type="button" class="button primary" data-access-login>Đăng nhập rồi tạo khóa</button></div></div>`;
    body.querySelector('[data-access-login]').onclick=()=>{if(state.loginUrl)location.href=state.loginUrl};
    return;
  }
  if(state.accessTokensStatus==='idle'){loadAccessTokens()}
  if(state.accessTokensStatus==='loading'){body.innerHTML='<div class="access-card access-tokens"><div class="token-empty">Đang tải khóa truy cập…</div></div>';return}
  if(state.accessTokensStatus==='error'){
    body.innerHTML=`<div class="access-card access-tokens"><div class="token-empty"><b>Không tải được danh sách khóa truy cập</b><p>${escapeHTML(state.accessTokensError)}</p><div style="display:flex;gap:8px;justify-content:center"><button type="button" class="button primary small" data-retry-tokens>Thử lại</button><button type="button" class="button ghost small" data-access-login>Đăng nhập lại</button></div></div></div>`;
    body.querySelector('[data-retry-tokens]').onclick=()=>{state.accessTokensStatus='idle';renderAccessTokens()};
    body.querySelector('[data-access-login]').onclick=()=>{if(state.loginUrl)location.href=state.loginUrl};
    return;
  }
  const base=getPublicBaseURL();
  const revealedSpec=state.revealedToken?accessMcpClientSpec(state.accessClient,base,state.revealedToken):null;
  const revealedPrompt=revealedSpec?accessMcpAgentPrompt(revealedSpec,base,state.revealedToken):'';
  const revealClients=state.revealedToken?[['cursor','Cursor'],['claude','Claude Code'],['codebuddy','CodeBuddy'],['codex','Codex']].map(([id,label])=>`<button type="button" class="${state.accessClient===id?'active':''}" data-revealed-client="${id}">${label}</button>`).join(''):'';
  const reveal=state.revealedToken?`<div class="token-reveal"><b>Đã tạo khóa truy cập, bạn có thể sao chép luôn toàn bộ prompt cấu hình</b><p>Hãy chọn client trước; nội dung sao chép sẽ tự kèm khóa truy cập lần này và Endpoint, không cần thay placeholder nữa. Nền tảng chỉ lưu hash, đóng xong sẽ không xem lại được bản rõ.</p><div class="access-clients token-client-picker">${revealClients}</div><div class="token-reveal-line"><code>${escapeHTML(state.revealedToken)}</code><button type="button" class="button primary small" data-copy-revealed-prompt="${encodeURIComponent(revealedPrompt)}">⧉ Sao chép prompt cấu hình ${escapeHTML(revealedSpec.label)}</button><button type="button" class="button ghost small" data-copy-revealed>Chỉ sao chép khóa</button><button type="button" class="button ghost small" data-close-revealed>Đã lưu</button></div></div>`:'';
  const rows=tokens.length?tokens.map(t=>{const revoked=!!t.revokedAt,expired=t.expiresAt&&new Date(t.expiresAt)<new Date();return `<div class="token-row ${revoked||expired?'revoked':''}"><div class="token-row-main"><b>${escapeHTML(t.name)}</b><code>${escapeHTML(t.prefix)}••••••••</code></div><div class="token-meta">Tạo ${relativeTime(t.createdAt)} · ${t.lastUsedAt?`Dùng gần nhất ${relativeTime(t.lastUsedAt)}`:'Chưa dùng'} · ${revoked?'Đã thu hồi':expired?'Đã hết hạn':`Hết hạn ${new Date(t.expiresAt).toLocaleDateString()}`}</div>${!revoked&&!expired?`<button type="button" class="button danger-outline small" data-revoke-token="${escapeHTML(t.id)}">Thu hồi</button>`:''}</div>`}).join(''):'<div class="token-empty">Chưa có khóa truy cập; sau khi tạo có thể kết nối REST API và MCP.</div>';
  const activeCount=tokens.filter(t=>!t.revokedAt&&(!t.expiresAt||new Date(t.expiresAt)>new Date())).length;
  body.innerHTML=`<div class="access-card access-tokens">${reveal}<form id="accessTokenForm" class="token-create-form"><label>Tên khóa<input class="input" name="name" required maxlength="128" placeholder="Ví dụ: Cursor của tôi"></label><label>Thời hạn<select class="select" name="expiresInDays"><option value="30">30 ngày</option><option value="90" selected>90 ngày</option><option value="180">180 ngày</option><option value="365">365 ngày</option></select></label><button type="submit" class="button primary">Tạo khóa truy cập</button></form><div class="access-card-title"><i>K</i>Khóa truy cập của tôi <span style="margin-left:auto;color:var(--muted-2);font-size:10.5px">${activeCount} còn hiệu lực</span></div><div class="token-list">${rows}</div></div>`;
  body.querySelector('[data-copy-revealed]')?.addEventListener('click',()=>copyText(state.revealedToken,'Đã sao chép khóa truy cập'));
  body.querySelector('[data-copy-revealed-prompt]')?.addEventListener('click',b=>copyText(decodeURIComponent(b.currentTarget.dataset.copyRevealedPrompt),`Đã sao chép prompt cấu hình ${revealedSpec.label}`));
  body.querySelectorAll('[data-revealed-client]').forEach(b=>b.onclick=()=>{state.accessClient=b.dataset.revealedClient;renderAccessTokens()});
  body.querySelector('[data-close-revealed]')?.addEventListener('click',()=>{state.revealedToken='';renderAccessTokens()});
  body.querySelector('#accessTokenForm')?.addEventListener('submit',createPersonalToken);
  body.querySelectorAll('[data-revoke-token]').forEach(b=>b.onclick=()=>revokePersonalToken(b.dataset.revokeToken));
}
async function createPersonalToken(e){
  e.preventDefault();const form=e.currentTarget;
  try{const result=await api('/api/auth/tokens',{method:'POST',body:JSON.stringify({name:form.elements.name.value.trim(),expiresInDays:Number(form.elements.expiresInDays.value)})});state.revealedToken=result.token;await loadAccessTokens();toast('Đã tạo khóa truy cập, hãy sao chép ngay','ok')}catch(err){toast(err.message,'err')}
}
async function revokePersonalToken(id){
  if(!(await afConfirm({title:'Thu hồi khóa truy cập',message:'Sau khi thu hồi, các client API và MCP đang dùng khóa này sẽ hết hiệu lực ngay. Bạn chắc chắn muốn thu hồi?',danger:true,confirmText:'Xác nhận thu hồi'})))return;
  try{await api(`/api/auth/tokens/${encodeURIComponent(id)}`,{method:'DELETE'});await loadAccessTokens();toast('Đã thu hồi khóa truy cập','ok')}catch(err){toast(err.message,'err')}
}


let commandMatches=[],commandSelectedIndex=0;
function commandCatalog(){
  const pages=[['collaboration','Bàn làm việc AI','Trò chuyện với AI, giao việc và theo dõi tác vụ'],['issues','Danh sách yêu cầu','Xem tiến độ, kích hoạt hoặc thử lại'],['approvals','Chờ tôi xác nhận','Phê duyệt hoặc trả lại các yêu cầu bị kẹt'],['observability','Mức dùng','Phân tích mức dùng · Token và chất lượng'],['settings','Cài đặt cá nhân','Tài khoản, model và thông tin xác thực thực thi'],['projects','Cấu hình dự án','Dự án, thành viên và repository'],['bots','Quản lý kênh','Kênh thông báo WeCom chat đơn hoặc nhóm'],['sops','Mẫu quy trình','Sửa Issue và phát triển yêu cầu'],['runs','Nhật ký chạy','Xem nhật ký bước và sản phẩm'],['aiSchedules','Tác vụ hẹn giờ','Tác vụ prompt, kích hoạt SOP và tác vụ dùng chung'],['agents','Định nghĩa vai trò','Quản lý vai trò và tài sản năng lực'],['mcp','Tài sản năng lực · MCP','Thêm MCP cấp dự án hoặc cá nhân'],['rules','Tài sản năng lực · Quy tắc','Ràng buộc hành vi và thực thi của dự án hiện tại'],['skills','Tài sản năng lực · Kỹ năng','Kỹ năng và tệp đính kèm của dự án hiện tại'],['runners','Máy thực thi','Chạy tác vụ trên máy của bạn hoặc CVM'],['dockerfiles','Tài sản môi trường · Dockerfile','Quản lý định nghĩa build container của dự án'],['environments','Tài sản môi trường · Script cài đặt','Quản lý script cài công cụ cho môi trường tách biệt'],['sandboxes','Tài sản môi trường · Sandbox','Quản lý chính sách tách môi trường chạy của dự án'],['guide','Khóa truy cập','Tạo và thu hồi khóa truy cập']].map(([view,title,meta])=>({kind:'Trang',title,meta,action:()=>switchView(view)}));
  const issues=(state.data.issues||[]).map(i=>({kind:'Yêu cầu',title:i.title,meta:`${i.priority||'—'} · ${issueStatusLabel(i.status)}`,action:()=>openIssueDetail(i.id)}));
  const runs=sopRuns(state.data.runs).map(r=>({kind:'Lần chạy',title:runIssueTitle(r),meta:`${runDisplayStatus(r)} · ${r.workflowSnapshot?.name||r.workflowId}`,action:()=>openRunFromId(r.id)}));
  const sops=catalogWorkflows().map(w=>({kind:'SOP',title:w.name,meta:`${w.status==='published'?'Đã xuất bản':'Bản nháp'} · ${w.nodes?.length||0} bước`,action:()=>{state.workflow=clone(w);state.selectedNode=null;switchView('flow')}}));
  const agents=(state.data.agents||[]).map(a=>({kind:'Agent',title:a.name,meta:a.description||a.id,action:()=>{switchView('agents');openAgentWizard(a)}}));
  const actions=[{kind:'Thao tác',title:'Tạo SOP mới',meta:'Tạo bản nháp quy trình',action:openSOPWizard}];
  return [...actions,...pages,...issues,...runs,...sops,...agents];
}
function openCommandPalette(){const host=$('#commandPalette');host.hidden=false;$('#commandSearch').value='';renderCommandResults();requestAnimationFrame(()=>$('#commandSearch').focus())}
function closeCommandPalette(){$('#commandPalette').hidden=true}
function moveCommandSelection(delta){
  if(!commandMatches.length)return;
  commandSelectedIndex=(commandSelectedIndex+delta+commandMatches.length)%commandMatches.length;
  highlightCommandSelection();
}
function highlightCommandSelection(){
  $$('#commandResults [data-command-index]').forEach((el,i)=>{
    el.classList.toggle('selected',i===commandSelectedIndex);
    if(i===commandSelectedIndex)el.scrollIntoView({block:'nearest'});
  });
}
function renderCommandResults(){
  const q=$('#commandSearch').value.trim().toLowerCase();
  commandMatches=commandCatalog().filter(x=>!q||`${x.kind} ${x.title} ${x.meta}`.toLowerCase().includes(q)).slice(0,12);
  commandSelectedIndex=0;
  $('#commandResults').innerHTML=commandMatches.length
    ?commandMatches.map((x,i)=>`<button data-command-index="${i}" class="${i===0?'selected':''}"><span class="command-kind">${escapeHTML(x.kind)}</span><span><b>${escapeHTML(x.title)}</b><small>${escapeHTML(x.meta||'')}</small></span><em>↵</em></button>`).join('')
    :`<div class="command-empty"><b>Không có kết quả khớp</b><span>Hãy thử đổi tiêu đề, mã hoặc tên trang</span></div>`;
}
function executeCommand(index){const item=commandMatches[index];if(!item)return;closeCommandPalette();item.action()}
