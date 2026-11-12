const $ = s => document.querySelector(s);
const $$ = s => [...document.querySelectorAll(s)];
const SIDEBAR_WIDTH_KEY='agentflow.sidebarWidth',SIDEBAR_LAYOUT_KEY='agentflow.sidebarLayoutVersion',SIDEBAR_LAYOUT_VERSION='grasp-reference-v3';
// Apply the Grasp reference width once before a workbench iframe can read old widths.
if(localStorage.getItem(SIDEBAR_LAYOUT_KEY)!==SIDEBAR_LAYOUT_VERSION){localStorage.removeItem(SIDEBAR_WIDTH_KEY);localStorage.setItem(SIDEBAR_LAYOUT_KEY,SIDEBAR_LAYOUT_VERSION)}
window.__AGENTFLOW_APP_DOMAIN__ = document.querySelector('meta[name="agentflow-app-domain"]')?.content || window.__AGENTFLOW_APP_DOMAIN__ || '';
// Go serialises an unset time as 0001-01-01; treat that like no time at all.
const knownTime = v => { const d = v ? new Date(v) : null; return d && d.getFullYear() > 1970 ? d : null; };
const clone = v => v==null ? v : JSON.parse(JSON.stringify(v));
const state = {
  data:null, currentUser:null, authRequired:false, currentView:'', analytics:null, workflow:null, workflowSaved:null, workflowDirty:false, selectedNode:null, connectFrom:null, connectCursor:null, activeRun:null,
  selectedAgent:null, selectedRun:null, selectedRunNode:null, runFilter:'all', runQuery:'', runWorkflowFilter:'',
  selectedIssue:null, projectFilter:'', projectDraft:null, requirementIssueId:null, sourceFilter:'',
  projectAdminDetailId:null, projectAdminTab:'basic', projectAdminQuery:'', projectAdminFilter:'all', projectDraftFor:'', projectDraftBase:null, projectIdentCustom:false, projectIdentSeed:'', projectAdminErrors:{},
  sopScope:'project', agentScope:'project', ruleScope:'project', skillScope:'project', dockerfileScope:'project', environmentScope:'project', sandboxScope:'project',
  issueRunFilter:'', issueStatusFilter:'', issueAssigneeFilter:'', issueQuery:'', issueView:localStorage.getItem('agentflow.issueView')||'table',
  assetType:'dockerfiles', selectedAsset:null, assetQuery:'', assetDirty:false, mcpTab:'installed', mcpScope:'project', mcpMarket:[], mcpQuery:'', editingMCPId:'', poll:null, runStream:null,
  obsTab:'overview', obsDeliveryFilter:'all', obsTraceRunId:null, obsTraceAdapter:'live', obsScope:localStorage.getItem('agentflow.obsScope')==='project'?'project':'user', obsUserFilter:'', obsSourceFilter:'', obsUserFilterReady:false, obsTrendGrain:['day','week','month','all'].includes(localStorage.getItem('agentflow.obsTrendGrain'))?localStorage.getItem('agentflow.obsTrendGrain'):'all', obsPeriodKey:'', obsTrendMetric:['runs','issues','cost'].includes(localStorage.getItem('agentflow.obsTrendMetric'))?localStorage.getItem('agentflow.obsTrendMetric'):'tokens', obsCrossProject:null, drawerOpen:false, issueDetailTab:'overview', issueDetailData:null, propTab:'basic',
  uiScroll:{runId:'', nodeId:'', graphLeft:0, feedTop:0, telTop:0, feedStick:true, telStick:true},
  runLogs:null, runTelemetry:null, runTelemetryAdapter:'live', runWorkspace:null,
  accessClient:'cursor',
  accessTokens:[], accessTokensStatus:'idle', accessTokensError:'', revealedToken:'',
  issueSelectionMode:false, issueSelection:new Set(), issueSelectionScope:'', issueBulkDeleting:false, issueBulkDeleteProgress:null, issueBulkDeleteResult:null,
  listPages:{}
};

const LIST_PAGE_SIZE={issues:20,runs:20,mcp:24,obs:20};
function listPageSize(key){return String(key||'').startsWith('obs-')?LIST_PAGE_SIZE.obs:(LIST_PAGE_SIZE[key]||20)}
function resetListPage(key){if(!state.listPages)state.listPages={};if(key)state.listPages[key]=1}
function resetListPages(prefix){
  if(!prefix){state.listPages={};return}
  Object.keys(state.listPages||{}).forEach(k=>{if(k===prefix||k.startsWith(prefix))state.listPages[k]=1});
}
function paginateList(key, items, size){
  const list=Array.isArray(items)?items:[];
  const pageSize=Math.max(1, Number(size)||listPageSize(key));
  const total=list.length;
  const pages=Math.max(1, Math.ceil(total/pageSize)||1);
  if(!state.listPages)state.listPages={};
  let page=Number(state.listPages[key]||1);
  if(!Number.isFinite(page)||page<1)page=1;
  if(page>pages)page=pages;
  state.listPages[key]=page;
  const start=(page-1)*pageSize;
  const end=Math.min(start+pageSize, total);
  return {key, page, pages, size:pageSize, start, end, total, items:list.slice(start, end)};
}
function pagerHTML(key, p){
  if(!p||p.total<=p.size)return '';
  const page=p.page, pages=p.pages, nums=[];
  const push=n=>{if(n>=1&&n<=pages&&!nums.includes(n))nums.push(n)};
  push(1);push(page-1);push(page);push(page+1);push(pages);
  nums.sort((a,b)=>a-b);
  let buttons='', prev=0;
  for(const n of nums){
    if(prev&&n>prev+1)buttons+=`<span class="pager-ellipsis">…</span>`;
    buttons+=`<button type="button" class="pager-page${n===page?' active':''}" data-page-key="${escapeHTML(key)}" data-page="${n}">${n}</button>`;
    prev=n;
  }
  return `<nav class="pager" data-pager="${escapeHTML(key)}" aria-label="分页"><span class="pager-meta">第 ${p.start+1}–${p.end} 条 / 共 ${p.total} 条</span><div class="pager-nav"><button type="button" class="pager-btn" data-page-key="${escapeHTML(key)}" data-page="${page-1}" ${page<=1?'disabled':''}>上一页</button>${buttons}<button type="button" class="pager-btn" data-page-key="${escapeHTML(key)}" data-page="${page+1}" ${page>=pages?'disabled':''}>下一页</button></div></nav>`;
}
function goListPage(key, page){
  if(!state.listPages)state.listPages={};
  state.listPages[key]=Math.max(1, Number(page)||1);
  if(String(key).startsWith('obs-')){if(typeof obsRefreshPaged==='function')obsRefreshPaged(key);else renderObservability()}
  else if(key==='issues')renderIssues();
  else if(key==='runs')renderRuns();
  else if(key==='mcp')renderMCPManagement();
  const el=document.querySelector(`[data-page-anchor="${key}"]`)||document.querySelector(`[data-pager="${key}"]`);
  if(el)el.scrollIntoView({block:'nearest'});
}
function bindPager(root){
  (root||document).querySelectorAll('[data-page-key]').forEach(btn=>{
    btn.onclick=e=>{
      e.preventDefault();
      e.stopPropagation();
      if(btn.disabled)return;
      goListPage(btn.dataset.pageKey, btn.dataset.page);
    };
  });
}

let apiInflight=0, toastTimer=null;
async function api(path, options={}) {
  const {silent, ...fetchOpts}=options;
  const bar=$('#globalProgress');
  if(!silent){
    apiInflight++;
    if(bar){bar.classList.add('active');bar.setAttribute('aria-hidden','false')}
  }
  try{
    const request={...fetchOpts};
    if(!request.headers&&!(request.body instanceof FormData))request.headers={'Content-Type':'application/json'};
    const res = await fetch(path, request);
    const body = await res.json().catch(()=>({}));
    if (!res.ok) {
      const err=new Error(body.error || `HTTP ${res.status}`);err.code=body.code||'';
      if(body.code==='repository_required'&&body.error)toastActions.set(body.error,{label:lfT('绑定仓库','Add repository'),run:()=>typeof openRepositoryModal==='function'&&openRepositoryModal()});
      throw err;
    }
    return body;
  } finally {
    if(!silent){
      apiInflight=Math.max(0, apiInflight-1);
      if(bar&&apiInflight===0){bar.classList.remove('active');bar.setAttribute('aria-hidden','true')}
    }
  }
}
const toastActions=new Map();
function toast(message, type='info', duration=2600){
  const container=$('#toastContainer');
  const legacy=$('#toast');
  if(!container&&legacy){
    const kind=type==='ok'||type==='err'?type:'info';
    legacy.textContent=message;
    legacy.className=`toast show toast-${kind}`;
    clearTimeout(toastTimer);
    toastTimer=setTimeout(()=>legacy.classList.remove('show'), 2200);
    return;
  }
  if(!container)return;
  const kind=['ok','err','warn','info'].includes(type)?type:'info';
  const icons={ok:'check',err:'alert',warn:'warning',info:'info'};
  const item=document.createElement('div');
  item.className=`toast-item toast-${kind}`;
  item.setAttribute('role','status');
  item.innerHTML=`<span class="toast-item-icon"><svg class="agent-icon harness-icon" data-icon="${icons[kind]}" width="16" height="16" aria-hidden="true" focusable="false"></svg></span><span class="toast-item-msg">${escapeHTML(message)}</span><button type="button" class="toast-item-close" title="关闭" aria-label="关闭"><svg class="agent-icon harness-icon" data-icon="close" width="14" height="14" aria-hidden="true" focusable="false"></svg></button>`;
  const action=toastActions.get(message);
  if(action){
    const button=document.createElement('button');button.type='button';button.className='button primary small toast-item-action';button.textContent=action.label;
    button.onclick=()=>{remove();action.run()};item.insertBefore(button,item.querySelector('.toast-item-close'));duration=Math.max(duration,8000);
  }
  container.appendChild(item);
  window.AgentFlowHarnessIcons?.hydrate?.(item);
  let timer=null;
  const remove=()=>{
    if(item.classList.contains('leaving'))return;
    item.classList.add('leaving');
    setTimeout(()=>{try{item.remove()}catch{}}, 200);
  };
  timer=setTimeout(remove, duration);
  item.onmouseenter=()=>clearTimeout(timer);
  item.onmouseleave=()=>{timer=setTimeout(remove, 1400)};
  item.querySelector('.toast-item-close').onclick=remove;
}

// Platform dialogs (#confirmModal) replace window.confirm/prompt/alert. Calls
// queue while one is open; Escape / backdrop cancel, Enter confirms (Ctrl/⌘+Enter
// in a textarea), Tab stays inside, and focus returns to the opener.
let confirmPromiseResolve = null;
const afDialogQueue=[];
let afDialogOpener=null;
const AF_CONFIRM_MODAL_HTML=`<div id="confirmModal" class="modal">
  <div class="modal-card confirm-modal-card" role="dialog" aria-modal="true" aria-labelledby="confirmTitle" aria-describedby="confirmMessage">
    <div class="confirm-modal-header">
      <div class="confirm-icon-wrap" id="confirmIconWrap"><span id="confirmIcon"></span></div>
      <div class="confirm-header-text"><h3 id="confirmTitle">确认操作</h3><p id="confirmMessage"></p></div>
    </div>
    <div id="confirmPromptWrap" class="confirm-prompt-wrap" hidden>
      <label id="confirmPromptLabel">请输入信息</label>
      <input type="text" id="confirmPromptInput" class="input" autocomplete="off">
      <textarea id="confirmPromptMultiline" class="input" rows="6" maxlength="65536" hidden></textarea>
    </div>
    <div class="confirm-modal-footer">
      <button type="button" class="button ghost" id="confirmCancelBtn">取消</button>
      <button type="button" class="button primary" id="confirmOkBtn">确定</button>
    </div>
  </div>
</div>`;
function afConfirmModal(){
  let modal=$('#confirmModal');
  if(!modal){
    const host=document.createElement('div');
    host.innerHTML=AF_CONFIRM_MODAL_HTML;
    modal=host.firstElementChild;
    document.body.appendChild(modal);
  }
  bindConfirmModal(modal);
  return modal;
}
function setConfirmIcon(tone,name){
  const wrap=$('#confirmIconWrap'),icon=$('#confirmIcon');
  if(wrap)wrap.className=`confirm-icon-wrap ${tone}`;
  if(icon){
    icon.innerHTML=`<svg class="agent-icon harness-icon" data-icon="${name}" width="17" height="17" aria-hidden="true" focusable="false"></svg>`;
    window.AgentFlowHarnessIcons?.hydrate?.(icon);
  }
}
// setup(modal) fills the dialog and returns {focus, value(isOk)}; the promise
// resolves with value(isOk), or isOk when no value() is given.
function afOpenDialog(setup){
  return new Promise(resolve=>{
    afDialogQueue.push({setup,resolve});
    afNextDialog();
  });
}
function afNextDialog(){
  if(confirmPromiseResolve){
    if($('#confirmModal')?.classList.contains('open'))return;
    return confirmPromiseResolve(false);
  }
  const job=afDialogQueue.shift();
  if(!job)return;
  const modal=afConfirmModal();
  if(!afDialogOpener){const active=document.activeElement;afDialogOpener=active&&active!==document.body&&!modal.contains(active)?active:null}
  $('#confirmPromptWrap').hidden=true;
  const rerunFields=$('#rerunFromNodeFields');
  if(rerunFields)rerunFields.hidden=true;
  const okBtn=$('#confirmOkBtn'),cancelBtn=$('#confirmCancelBtn');
  cancelBtn.hidden=false;
  let opened;
  try{opened=job.setup(modal)||{}}catch(err){job.resolve(null);console.error(err);return afNextDialog()}
  window.HuiPopover?.closeAll?.();
  window.HuiTooltip?.hide?.();
  modal.classList.add('open');
  const focus=opened.focus||okBtn;
  focus.focus();
  if(opened.select)focus.select?.();
  confirmPromiseResolve=isOk=>{
    const result=opened.value?opened.value(!!isOk):!!isOk;
    modal.classList.remove('open');
    confirmPromiseResolve=null;
    job.resolve(result);
    if(afDialogQueue.length)return afNextDialog();
    const back=afDialogOpener;
    afDialogOpener=null;
    if(back?.isConnected)back.focus?.({preventScroll:true});
  };
}
function afDialogFocusables(modal){
  return [...modal.querySelectorAll('button,input,textarea,select,[tabindex]:not([tabindex="-1"])')].filter(el=>!el.disabled&&!el.closest('[hidden]')&&el.tabIndex!==-1&&el.getClientRects().length);
}
function bindConfirmModal(modal){
  if(!modal||modal.dataset.dialogBound)return;
  modal.dataset.dialogBound='1';
  $('#confirmOkBtn')?.addEventListener('click',()=>confirmPromiseResolve?.(true));
  $('#confirmCancelBtn')?.addEventListener('click',()=>confirmPromiseResolve?.(false));
  modal.addEventListener('click',e=>{if(e.target===modal)confirmPromiseResolve?.(false)});
  // Anything else that drops .open (generic modal handlers) still settles the
  // pending call, otherwise the queue would never open another dialog.
  if(typeof MutationObserver==='function'){
    new MutationObserver(()=>{if(confirmPromiseResolve&&!modal.classList.contains('open'))confirmPromiseResolve(false)})
      .observe(modal,{attributes:true,attributeFilter:['class']});
  }
  // Capture on document: Escape cancels wherever focus is, before other
  // Escape handlers close an underlying modal or drawer.
  document.addEventListener('keydown',e=>{
    if(!confirmPromiseResolve||!modal.classList.contains('open')||e.isComposing||e.defaultPrevented)return;
    if(e.key==='Escape'){e.preventDefault();e.stopPropagation();confirmPromiseResolve(false);return}
    if(e.key==='Tab'&&!modal.contains(document.activeElement)){
      const items=afDialogFocusables(modal);
      if(items.length){e.preventDefault();items[e.shiftKey?items.length-1:0].focus()}
    }
  },true);
  modal.addEventListener('keydown',e=>{
    if(!confirmPromiseResolve||e.isComposing)return;
    if(e.key==='Tab'){
      const items=afDialogFocusables(modal);
      if(!items.length)return;
      const at=items.indexOf(document.activeElement);
      if(e.shiftKey&&at<=0){e.preventDefault();items[items.length-1].focus()}
      else if(!e.shiftKey&&at===items.length-1){e.preventDefault();items[0].focus()}
      return;
    }
    if(e.key!=='Enter')return;
    const tag=e.target?.tagName;
    if(tag==='BUTTON'||tag==='SELECT')return;
    if(tag==='TEXTAREA'&&!(e.ctrlKey||e.metaKey))return;
    e.preventDefault();
    confirmPromiseResolve(true);
  });
}
function afConfirm(options){
  const opt=typeof options==='string'?{message:options}:(options||{});
  return afOpenDialog(()=>{
    const danger=!!opt.danger;
    $('#confirmTitle').textContent=opt.title||(danger?'危险操作确认':'操作确认');
    $('#confirmMessage').textContent=opt.message||'确定要继续吗？';
    const okBtn=$('#confirmOkBtn');
    okBtn.textContent=opt.confirmText||(danger?'确定执行':'确定');
    okBtn.className=`button ${danger?'danger-btn':'primary'}`;
    $('#confirmCancelBtn').textContent=opt.cancelText||'取消';
    if(danger)setConfirmIcon('danger','trash');
    else if(opt.info)setConfirmIcon('info','info');
    else setConfirmIcon('warn','warning');
    return {focus:okBtn};
  });
}

function afPrompt(options, defaultVal=''){
  const opt=typeof options==='string'?{message:options, defaultValue:defaultVal}:(options||{});
  return afOpenDialog(()=>{
    const title=opt.title||'请输入内容';
    $('#confirmTitle').textContent=title;
    $('#confirmMessage').textContent=opt.message||'';
    const single=$('#confirmPromptInput'),multi=$('#confirmPromptMultiline');
    const promptInput=opt.multiline?multi:single;
    single.hidden=!!opt.multiline;
    multi.hidden=!opt.multiline;
    const promptLabel=$('#confirmPromptLabel');
    $('#confirmPromptWrap').hidden=false;
    promptLabel.textContent=opt.label||title;
    promptLabel.htmlFor=promptInput.id;
    promptInput.value=opt.defaultValue||'';
    promptInput.placeholder=opt.placeholder||'';
    const okBtn=$('#confirmOkBtn');
    okBtn.textContent=opt.confirmText||'确定';
    okBtn.className=`button ${opt.danger?'danger-btn':'primary'}`;
    $('#confirmCancelBtn').textContent=opt.cancelText||'取消';
    setConfirmIcon(opt.danger?'danger':'info','edit');
    return {focus:promptInput,select:true,value:isOk=>isOk?promptInput.value:null};
  });
}

function afAlert(options){
  const opt=typeof options==='string'?{message:options}:(options||{});
  return afOpenDialog(()=>{
    $('#confirmTitle').textContent=opt.title||'提示';
    $('#confirmMessage').textContent=opt.message||'';
    const okBtn=$('#confirmOkBtn');
    okBtn.textContent='我知道了';
    okBtn.className='button primary';
    $('#confirmCancelBtn').hidden=true;
    if(opt.type==='err')setConfirmIcon('danger','alert');
    else if(opt.type==='ok')setConfirmIcon('ok','check');
    else setConfirmIcon('info','info');
    return {focus:okBtn,value:()=>undefined};
  });
}

function highlightCode(code, lang){
  const s=String(code||'');
  const language=String(lang||'').toLowerCase();
  if(language==='shell'||language==='bash'||language==='sh'){
    return highlightShell(s);
  } else if(language==='python'||language==='py'){
    return highlightPython(s);
  } else if(language==='json'){
    return highlightJSON(s);
  }
  return escapeHTML(s);
}

function highlightShell(src){
  const lines=src.split('\n');
  return lines.map(line=>{
    let commentIdx=-1;
    let inSingle=false, inDouble=false;
    for(let i=0;i<line.length;i++){
      const ch=line[i];
      if(ch==="'"&&!inDouble) inSingle=!inSingle;
      else if(ch==='"'&&!inSingle&&(i===0||line[i-1]!=='\\')) inDouble=!inDouble;
      else if(ch==='#'&&!inSingle&&!inDouble){
        commentIdx=i;
        break;
      }
    }
    let codePart=commentIdx>=0?line.slice(0, commentIdx):line;
    let comment=commentIdx>=0?line.slice(commentIdx):'';

    let html=escapeHTML(codePart);
    html=html.replace(/(&quot;[\s\S]*?&quot;|&#39;[\s\S]*?&#39;)/g, '<span class="tok-str">$1</span>');
    html=html.replace(/(\$(?:[a-zA-Z_0-9]+|\{[^}]+\}|[?@*#0-9]))/g, '<span class="tok-var">$1</span>');
    html=html.replace(/\b(if|then|elif|else|fi|for|in|do|done|while|until|case|esac|function|return|exit|export|local|set|unset|shift|trap)\b/g, '<span class="tok-kw">$1</span>');
    html=html.replace(/\b(echo|cd|ls|grep|cat|rm|mkdir|cp|mv|chmod|chown|touch|test|git|go|python|node|npm|pnpm|yarn|pip|curl|wget|tar|zip|unzip|sed|awk|printf|tee|find|kill|ps|docker|source|read)\b/g, '<span class="tok-cmd">$1</span>');
    html=html.replace(/(\s)(-[a-zA-Z0-9_-]+)/g, '$1<span class="tok-op">$2</span>');

    if(comment){
      html+=`<span class="tok-comment">${escapeHTML(comment)}</span>`;
    }
    return html;
  }).join('\n');
}

function highlightPython(src){
  // Tokenize the source once: later replacements must never rewrite generated
  // span attributes (Python's "class" and "str" are also words in that markup).
  const tokens=/(#[^\n]*)|("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')|(\b(?:def|class|import|from|return|if|elif|else|for|while|try|except|finally|with|as|lambda|pass|raise|yield|async|await|assert|break|continue|global|nonlocal|del|in|is|not|and|or)\b)|(\b(?:print|len|range|str|int|float|dict|list|set|tuple|bool|open|isinstance|issubclass|type|enumerate|zip|map|filter|sum|min|max|any|all|abs|round|input)\b)|(\b(?:True|False|None)\b)|(@[a-zA-Z_0-9.]+)|(\b\d+(?:\.\d+)?\b)/g;
  const classes=['comment','str','kw','built','cmd','var','num'];
  let html='',offset=0;
  for(const match of src.matchAll(tokens)){
    const kind=match.slice(1).findIndex(value=>value!==undefined);
    html+=escapeHTML(src.slice(offset,match.index))+`<span class="tok-${classes[kind]}">${escapeHTML(match[0])}</span>`;
    offset=match.index+match[0].length;
  }
  return html+escapeHTML(src.slice(offset));
}

function highlightJSON(src){
  let html=escapeHTML(src);
  html=html.replace(/(&quot;[\s\S]*?&quot;)(\s*:)?/g, (match, p1, p2)=>{
    if(p2) return `<span class="tok-kw">${p1}</span>${p2}`;
    return `<span class="tok-str">${p1}</span>`;
  });
  html=html.replace(/\b(true|false|null)\b/g, '<span class="tok-cmd">$1</span>');
  html=html.replace(/\b(\d+(?:\.\d+)?)\b/g, '<span class="tok-num">$1</span>');
  return html;
}

function renderMarkdown(md){
  if(!md)return '';
  const text=String(md).replace(/\r\n/g, '\n').replace(/\r/g, '\n');
  const lines=text.split('\n');
  const out=[];
  let inCode=false, codeLang='', codeLines=[];
  let inList=false, listType='', listItems=[];
  let inTable=false, tableRows=[];

  function flushList(){
    if(!inList)return;
    const tag=listType==='ol'?'ol':'ul';
    out.push(`<${tag}>${listItems.join('')}</${tag}>`);
    inList=false;listType='';listItems=[];
  }

  function flushTable(){
    if(!inTable||!tableRows.length)return;
    const parseRow=(r)=>r.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(c=>c.trim());
    const headers=parseRow(tableRows[0]);
    const bodyRows=tableRows.slice(2);
    const thead=`<thead><tr>${headers.map(h=>`<th>${parseInlineMarkdown(h)}</th>`).join('')}</tr></thead>`;
    const tbody=`<tbody>${bodyRows.map(row=>{
      const cells=parseRow(row);
      return `<tr>${headers.map((_,idx)=>`<td>${parseInlineMarkdown(cells[idx]||'')}</td>`).join('')}</tr>`;
    }).join('')}</tbody>`;
    out.push(`<table>${thead}${tbody}</table>`);
    inTable=false;tableRows=[];
  }

  for(let i=0;i<lines.length;i++){
    const line=lines[i];

    if(line.trim().startsWith('```')){
      if(inCode){
        const rawCode=codeLines.join('\n');
        const highlighted=highlightCode(rawCode, codeLang);
        out.push(`<div class="md-code-block"><div class="md-code-header"><span class="md-code-lang">${escapeHTML(codeLang||'TEXT')}</span><button type="button" class="md-code-copy">复制</button></div><pre><code>${highlighted}</code></pre></div>`);
        inCode=false;codeLang='';codeLines=[];
      } else {
        flushList();
        flushTable();
        inCode=true;
        codeLang=line.trim().slice(3).trim();
        codeLines=[];
      }
      continue;
    }
    if(inCode){
      codeLines.push(line);
      continue;
    }

    if(line.trim().startsWith('|') && line.trim().endsWith('|')){
      flushList();
      if(!inTable){
        const next=lines[i+1]||'';
        if(next.trim().startsWith('|') && /\|[-:| ]+\|/.test(next)){
          inTable=true;
          tableRows=[line];
          continue;
        }
      } else {
        tableRows.push(line);
        continue;
      }
    } else if(inTable){
      flushTable();
    }

    if(!line.trim()){
      flushList();
      continue;
    }

    const alertMatch=line.match(/^>\s*\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]/i);
    if(alertMatch){
      flushList();
      const kind=alertMatch[1].toLowerCase();
      const alertLines=[];
      while(i+1<lines.length && lines[i+1].trim().startsWith('>')){
        i++;
        alertLines.push(lines[i].replace(/^>\s?/, ''));
      }
      const titleIcons={note:'ℹ️ NOTE', tip:'💡 TIP', important:'🟣 IMPORTANT', warning:'⚠️ WARNING', caution:'🛑 CAUTION'};
      const titleText=titleIcons[kind]||kind.toUpperCase();
      out.push(`<div class="md-alert md-alert-${kind}"><div class="md-alert-title">${titleText}</div><div class="md-alert-content">${renderMarkdown(alertLines.join('\n'))}</div></div>`);
      continue;
    }

    if(line.startsWith('>')){
      flushList();
      const bqLines=[line.replace(/^>\s?/, '')];
      while(i+1<lines.length && lines[i+1].trim().startsWith('>')){
        i++;
        bqLines.push(lines[i].replace(/^>\s?/, ''));
      }
      out.push(`<blockquote>${renderMarkdown(bqLines.join('\n'))}</blockquote>`);
      continue;
    }

    if(/^(?:---|\*\*\*|___)\s*$/.test(line.trim())){
      flushList();
      out.push('<hr>');
      continue;
    }

    const hMatch=line.match(/^(#{1,6})\s+(.+)$/);
    if(hMatch){
      flushList();
      const level=hMatch[1].length;
      out.push(`<h${level}>${parseInlineMarkdown(hMatch[2])}</h${level}>`);
      continue;
    }

    const taskMatch=line.match(/^[-*]\s+\[([ xX])\]\s+(.+)$/);
    if(taskMatch){
      if(!inList||listType!=='ul'){
        flushList();
        inList=true;listType='ul';
      }
      const checked=taskMatch[1].toLowerCase()==='x';
      listItems.push(`<li class="task-list-item"><input type="checkbox" class="task-checkbox" disabled ${checked?'checked':''}><span>${parseInlineMarkdown(taskMatch[2])}</span></li>`);
      continue;
    }

    const ulMatch=line.match(/^[-*+]\s+(.+)$/);
    if(ulMatch){
      if(!inList||listType!=='ul'){
        flushList();
        inList=true;listType='ul';
      }
      listItems.push(`<li>${parseInlineMarkdown(ulMatch[1])}</li>`);
      continue;
    }

    const olMatch=line.match(/^\d+\.\s+(.+)$/);
    if(olMatch){
      if(!inList||listType!=='ol'){
        flushList();
        inList=true;listType='ol';
      }
      listItems.push(`<li>${parseInlineMarkdown(olMatch[1])}</li>`);
      continue;
    }

    flushList();
    out.push(`<p>${parseInlineMarkdown(line)}</p>`);
  }

  flushList();
  flushTable();
  if(inCode){
    const rawCode=codeLines.join('\n');
    out.push(`<div class="md-code-block"><pre><code>${highlightCode(rawCode, codeLang)}</code></pre></div>`);
  }
  return out.join('');
}

function parseInlineMarkdown(text){
  let s=escapeHTML(text);
  s=s.replace(/`([^`]+)`/g, '<code>$1</code>');
  s=s.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  s=s.replace(/__([^_]+)__/g, '<strong>$1</strong>');
  s=s.replace(/\*([^*]+)\*/g, '<em>$1</em>');
  s=s.replace(/_([^_]+)_/g, '<em>$1</em>');
  s=s.replace(/~~([^~]+)~~/g, '<del>$1</del>');
  s=s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (match, label, url)=>{
    const cleanUrl=safeURL(url.trim());
    return `<a href="${escapeHTML(cleanUrl)}" target="_blank" rel="noopener noreferrer">${label}</a>`;
  });
  return s;
}

function isMarkdownArtifact(artifact, content){
  const name=String(artifact?.name||'').toLowerCase();
  const path=String(artifact?.path||'').toLowerCase();
  const type=String(artifact?.mediaType||'').toLowerCase();
  if(name.endsWith('.md') || name.endsWith('.markdown') || path.endsWith('.md') || path.endsWith('.markdown') || type==='text/markdown'){
    return true;
  }
  if(typeof content==='string'){
    if(/^\s*#+\s+/m.test(content) || /\|[^\n]+\|\n\|[-:| ]+\|/.test(content)){
      return true;
    }
  }
  return false;
}

let currentCodeEditorSave = null;
function openCodeEditorModal({ title, subtitle, language='shell', value='', onSave }){
  const modal=$('#codeEditorModal');
  if(!modal)return;
  currentCodeEditorSave=onSave;

  $('#codeEditorTitle').textContent=title||'代码编辑';
  $('#codeEditorSubtitle').textContent=subtitle||'编辑脚本与配置';
  const langSelect=$('#codeEditorLang');
  langSelect.value=language==='python'?'python':language==='plaintext'?'plaintext':'shell';
  $('#codeEditorBadge').textContent=langSelect.value.toUpperCase();

  const textarea=$('#codeEditorTextarea');
  const highlight=$('#codeEditorHighlight');
  const gutter=$('#codeEditorGutter');
  const stats=$('#codeEditorStats');
  const card=$('.code-editor-card');
  card?.classList.remove('fullscreen');

  textarea.value=value;
  textarea.scrollTop=textarea.scrollLeft=0;

  function updateView(){
    const text=textarea.value;
    const lines=text.split('\n');
    const lineCount=lines.length;

    gutter.innerHTML=Array.from({length:lineCount}, (_,i)=>i+1).join('\n');
    const lang=langSelect.value;
    highlight.innerHTML=highlightCode(text, lang)+'\n';

    const selStart=textarea.selectionStart||0;
    const beforeCursor=text.slice(0, selStart);
    const curLine=beforeCursor.split('\n').length;
    const curCol=selStart - beforeCursor.lastIndexOf('\n');
    stats.textContent=`行 ${curLine}，列 ${curCol} · 共 ${lineCount} 行 · ${text.length} 字符`;
    syncScroll();
  }

  function syncScroll(){
    highlight.scrollTop=textarea.scrollTop;
    highlight.scrollLeft=textarea.scrollLeft;
    gutter.scrollTop=textarea.scrollTop;
  }

  textarea.oninput=updateView;
  textarea.onscroll=syncScroll;
  textarea.onclick=textarea.onkeyup=updateView;

  langSelect.onchange=()=>{
    $('#codeEditorBadge').textContent=langSelect.value.toUpperCase();
    updateView();
  };

  updateView();
  modal.classList.add('open');
  setTimeout(()=>{
    textarea.focus();
    syncScroll();
  }, 50);
}
function lines(value){return String(value||'').split('\n').map(v=>v.trim()).filter(Boolean)}
function escapeHTML(v){return String(v??'').replace(/[&<>'"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]))}
function clarifyOptionLabel(opt){
  if(opt==null)return '';
  if(typeof opt==='object'){
    if(Array.isArray(opt))return '';
    return clarifyOptionLabel(opt.label||opt.text||opt.value||opt.title||opt.question||opt.name||'');
  }
  const text=String(opt).trim();
  if(!text)return '';
  if(/^[\[{]/.test(text)){
    try{const inner=JSON.parse(text);const label=clarifyOptionLabel(inner);if(label)return label;}catch{}
    return '';
  }
  return text;
}
function clarifyQuestionOptions(q){
  const raw=q&&q.options;
  let list=[];
  if(Array.isArray(raw))list=raw;
  else if(typeof raw==='string'&&raw.trim()){
    try{const parsed=JSON.parse(raw);list=Array.isArray(parsed)?parsed:[raw];}catch{list=[raw];}
  }
  return list.map(clarifyOptionLabel).filter(Boolean);
}
function safeURL(value){try{const url=new URL(String(value||''),location.origin);return ['http:','https:'].includes(url.protocol)?url.href:'#'}catch{return '#'}}
function safeColor(value){return /^#[0-9a-f]{3,8}$/i.test(String(value||''))?value:'#74829a'}
function uid(prefix){return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2,6)}`}
const PRODUCTION_SOPS=new Set(['issue-fix-production','requirement-development-production']);
function catalogOwned(projectId){return !!state.projectFilter&&projectId===state.projectFilter}
function catalogForeign(projectId){return !!projectId&&projectId!==state.projectFilter}
function catalogShared(projectId,shared){return !projectId||(!!shared&&projectId!==state.projectFilter)}
function catalogVisible(projectId,shared){return catalogOwned(projectId)||catalogShared(projectId,shared)}
// Generic templates are platform-provided and project independent: every
// project sees them, but they are read-only blueprints until adopted.
function isPlatformAdmin(){return !!(state.currentUser&&!state.currentUser.isGuest&&state.currentUser.isPlatformAdmin)}
function canEditGeneric(wf){return catalogGeneric(wf)&&isPlatformAdmin()}
function catalogGeneric(item){return !!item?.generic}
// Generic roles live in generic_templates, but the first project that
// adopted a template often reused the same role id. Looking the wizard
// subject up in the project catalog then marks that role as foreign and
// hides Save in favour of「复制到本项目」.
function agentWizardSubject(){
  if(catalogGeneric(state.workflow))return (state.genericTemplateAgents||[]).find(x=>x.id===state.selectedAgent)||state.agentDraft||{};
  return (state.data.agents||[]).find(x=>x.id===state.selectedAgent)||state.agentDraft||{};
}
function agentWizardForeign(a){
  if(catalogGeneric(state.workflow))return !isPlatformAdmin();
  return catalogForeign(a?.projectId);
}
function genericTemplateMCPOptions(selected){
  const wanted=['github'];
  const out=[];
  const seen=new Set();
  const add=(id,name,description)=>{
    if(!id||seen.has(id))return;
    seen.add(id);
    out.push({id,name:name||id,description:description||'通用模板按市场别名绑定，运行时解析到当前项目已安装的 MCP'});
  };
  for(const item of state.mcpMarket||[]){
    const alias=(item.aliases||[]).find(key=>wanted.includes(key));
    if(alias||wanted.includes(item.id))add(alias||item.id,item.name,item.description);
  }
  // The server resolves these aliases from its embedded catalog, so they stay
  // selectable even if the catalog request has not finished or failed.
  for(const id of wanted)add(id,id==='github'?'GitHub':id);
  for(const id of selected||[]){
    if(seen.has(id))continue;
    const item=(state.mcpMarket||[]).find(m=>m.id===id||(m.aliases||[]).includes(id));
    add(id,item?.name,item?.description);
  }
  return out;
}
function genericMCPChecked(id,selected){
  if((selected||[]).includes(id))return true;
  const item=(state.mcpMarket||[]).find(m=>m.id===id||(m.aliases||[]).includes(id));
  if(!item)return false;
  return (selected||[]).some(s=>s===item.id||(item.aliases||[]).includes(s));
}
function assetVisible(item){return !item?.projectId||item.projectId===state.projectFilter}
function catalogWorkflows(){return (state.data?.workflows||[]).filter(w=>!catalogGeneric(w)&&catalogVisible(w.projectId,w.shared))}
// Runnable options = stored SOPs + platform generic templates (run by reference).
function runnableWorkflowOptions(){return catalogWorkflows().concat((state.data?.workflows||[]).filter(catalogGeneric).map(w=>({...w,name:`通用 · ${w.name}`})))}
function scopedWorkflows(){
  const all=state.data?.workflows||[];
  if(state.sopScope==='generic')return all.filter(w=>catalogGeneric(w));
  if(state.sopScope==='shared')return all.filter(w=>!catalogGeneric(w)&&catalogShared(w.projectId,w.shared));
  return all.filter(w=>!catalogGeneric(w)&&catalogOwned(w.projectId));
}
function catalogAgents(){return (state.data?.agents||[]).filter(a=>catalogVisible(a.projectId,a.shared))}
function workflowAgents(){
  if(catalogGeneric(state.workflow))return state.genericTemplateAgents||[];
  const projectId=state.workflow?.projectId||state.projectFilter||'';
  return (state.data?.agents||[]).filter(a=>!!a.projectId&&(a.projectId===projectId||(a.shared&&a.projectId!==projectId)));
}
function workflowAgent(id){return workflowAgents().find(a=>a.id===id)}
function clearGenericTemplateAgents(){state.genericTemplateAgents=[];state.genericTemplateAgentWorkflowId=''}
async function hydrateGenericWorkflowRoles(wf=state.workflow){
  if(!catalogGeneric(wf)){
    if(state.genericTemplateAgentWorkflowId)clearGenericTemplateAgents();
    return false;
  }
  if(state.genericTemplateAgentWorkflowId===wf.id)return false;
  const id=wf.id;
  try{
    const agents=await api(`/api/workflows/${encodeURIComponent(id)}/agents`);
    if(state.workflow?.id!==id)return false;
    state.genericTemplateAgents=Array.isArray(agents)?agents:[];
    state.genericTemplateAgentWorkflowId=id;
    return true;
  }catch(e){
    // 不要把失败记成「已加载」：那个标记是用来跳过重复请求的，
    // 一旦写上，这个模板就再也不会重拉，门禁会一直显示成没有配置。
    if(state.workflow?.id===id)clearGenericTemplateAgents();
    toast(e.message||'加载通用模板角色失败');
    return false;
  }
}
async function refreshGenericWorkflowRoles(){
  if(await hydrateGenericWorkflowRoles()){
    renderCanvas();
    renderProperties();
  }
}
function scopedAgents(){const all=state.data?.agents||[];return state.agentScope==='shared'?all.filter(a=>catalogShared(a.projectId,a.shared)):all.filter(a=>catalogOwned(a.projectId))}
function visibleAssets(items){return (items||[]).filter(assetVisible)}
function visibleMCPAssets(items){const uid=state.currentUser&&!state.currentUser.isGuest?Number(state.currentUser.id):0;return (items||[]).filter(item=>item.scope==='project'?item.projectId===state.projectFilter:item.scope==='personal'&&!item.marketId&&uid>0&&Number(item.ownerUserId)===uid)}
function projectQuery(){return state.projectFilter?`?projectId=${encodeURIComponent(state.projectFilter)}`:''}
function projectQueryWith(extra){const q=projectQuery();return q?q+`&${extra}`:`?${extra}`}
function runProjectId(r){
  if(r?.executionContext?.projectId)return r.executionContext.projectId;
  const issue=runContext(r).issue;
  if(issue?.projectId)return issue.projectId;
  const rid=r?.repository?.repositoryId;
  if(rid){const repo=(state.data.repositories||[]).find(x=>x.id===rid);if(repo?.projectId)return repo.projectId}
  return '';
}
function scopedRuns(runs){const pid=state.projectFilter,list=runs||[];return pid?list.filter(r=>runProjectId(r)===pid):list}
// Conversation workspaces (Run.kind=workspace) are the chat's sandbox, not SOP
// runs: they have no nodes and stay "waiting" until reclaimed. They are reached
// from the conversation and the requirement, never from SOP run lists or counts.
function isSOPRun(r){return r?.kind!=='workspace'}
function sopRuns(runs){return (runs||[]).filter(isSOPRun)}
function preferredWorkflow(){
  const usable=catalogWorkflows(), mine=usable.filter(w=>catalogOwned(w.projectId));
  return mine.find(w=>w.id==='issue-fix-production')||mine[0]||usable.find(w=>w.id==='issue-fix-production')||usable[0]||null;
}
function syncWorkflowToProject(){
  if(state.workflow&&!catalogGeneric(state.workflow)&&catalogVisible(state.workflow.projectId,state.workflow.shared))return;
  state.workflow=clone(preferredWorkflow());
  state.workflowSaved=clone(state.workflow);
  state.workflowDirty=false;
  rememberOpenWorkflow();
}
function isStaleRun(r){if(r?.status!=='running')return false;const t=new Date(r.updatedAt||r.createdAt||0).getTime();return !t||(Date.now()-t)>4*60*1000}
function isGhostRun(r){if(!['failed','cancelled'].includes(r?.status))return false;return Object.values(r?.nodes||{}).some(n=>n?.status==='running'||n?.status==='pending_input')}
function liveRuns(){return scopedRuns(sopRuns(state.data?.runs).filter(r=>r.status==='waiting'||(r.status==='running'&&!isStaleRun(r))||isGhostRun(r)))}
function staleRuns(){return scopedRuns(sopRuns(state.data?.runs).filter(isStaleRun))}
async function refreshDashboard(){
  try{
    const opt={silent:true};
    const [runs,tasks,issues]=await Promise.all([api('/api/runs',opt),api('/api/tasks',opt),api('/api/issues',opt)]);
    state.data.runs=runs;state.data.tasks=tasks;state.data.issues=issues;
    renderDashboard();
  }catch{}
}
function currentProject(){return (state.data.projects||[]).find(p=>p.id===state.projectFilter)||(state.data.projects||[])[0]}
// Newcomers (no project in the URL or saved from an earlier visit) start in a
// project they belong to, then the welcome project, then the first project.
const DEFAULT_PROJECT_ID='welcome';
function defaultProjectId(projects,userId=globalThis.state?.currentUser?.id){const list=projects||[];const mine=userId?list.find(p=>p.ownerId===userId||(p.members||[]).some(m=>m.userId===userId)):null;return (mine||list.find(p=>p.id===DEFAULT_PROJECT_ID)||list[0])?.id||''}
function currentRepository(project=currentProject()){const id=project?.defaultRepositoryId;return (state.data.repositories||[]).find(r=>r.id===id)}
function userById(id){return (state.data.users||[]).find(u=>Number(u.id)===Number(id))}
function userDisplay(user){return user?(user.chineseName&&user.loginName?`${user.loginName}(${user.chineseName})`:user.loginName||user.chineseName||`用户 ${user.id}`):''}
function projectMembers(projectId){return ((state.data.projects||[]).find(p=>p.id===projectId)||{}).members||[]}
function issueAssigneeName(issue){
	if(issue?.assigneeUserId)return userDisplay(userById(issue.assigneeUserId));
	if(!issue?.assigneeId)return '';
  if(issue.assigneeType==='agent')return (state.data.agents||[]).find(a=>a.id===issue.assigneeId)?.name||'';
  return '';
}
function runIssueTitle(r){const task=(state.data.tasks||[]).find(t=>t.runId===r.id);const issue=task&&(state.data.issues||[]).find(i=>i.id===task.issueId);if(issue?.title)return issue.title;const line=String(r.input||'').split('\n')[0].trim();return line||r.workflowSnapshot?.name||r.workflowId||r.id}

function isLoopbackHost(hostname){
  return hostname==='localhost'||hostname==='127.0.0.1'||hostname==='[::1]'||hostname==='0.0.0.0';
}
function isOAHost(hostname){
  if(isLoopbackHost(hostname))return false;
  if(typeof state!=='undefined' && state?.appDomain && hostname===state.appDomain)return true;
  if(typeof window!=='undefined' && window.__AGENTFLOW_APP_DOMAIN__ && hostname===window.__AGENTFLOW_APP_DOMAIN__)return true;
  return false;
}
function shouldAutoSSORedirect({isGuest, hostname, bypassAutoAuth, authErr, loginUrl}){
  return !!(isGuest && isOAHost(hostname) && !bypassAutoAuth && !authErr && loginUrl);
}

async function boot(){
  state.data = {runs:[],issues:[],tasks:[],workflowMetrics:[],syncLogs:[],agents:[],workflows:[],projects:[],repositories:[],users:[],rules:[],skills:[],mcpServers:[],executors:[],dockerfiles:[],environments:[],sandboxes:[]};
  bind(); renderProjectSwitcher(); updateApprovalBadge();

  // 1. 检查 URL 中是否有 SSO 回调的 code
  const urlParams = new URLSearchParams(window.location.search);
  const authCode = urlParams.get('code');
  if (authCode) {
    try {
      const authRes = await api('/api/auth/exchange?code=' + encodeURIComponent(authCode), {silent: true});
      state.currentUser = authRes.user || null;
      state.authRequired = !!authRes.authRequired;
      state.appDomain = authRes.appDomain || (typeof window !== 'undefined' ? window.__AGENTFLOW_APP_DOMAIN__ : '') || '';
      if (state.currentUser && !state.currentUser.isGuest) {
        sessionStorage.removeItem('agentflow_auth_err');
      } else {
        sessionStorage.setItem('agentflow_auth_err', 'exchange_failed');
      }
    } catch (err) {
      console.warn('[auth] exchange code failed:', err);
      sessionStorage.setItem('agentflow_auth_err', 'network_err');
    } finally {
      urlParams.delete('code');
      const cleanSearch = urlParams.toString();
      const newUrl = window.location.pathname + (cleanSearch ? '?' + cleanSearch : '') + window.location.hash;
      window.history.replaceState({}, document.title, newUrl);
    }
  }

  // 2. 若尚未获取当前登录身份，则请求 /api/auth/current
  if (!state.currentUser) {
    try {
      const returnUrl = encodeURIComponent(window.location.href);
      const curRes = await api('/api/auth/current?return_url=' + returnUrl, {silent: true});
      state.currentUser = curRes.user || null;
      state.loginUrl = curRes.loginUrl || '';
      state.authRequired = !!curRes.authRequired;
      state.appDomain = curRes.appDomain || (typeof window !== 'undefined' ? window.__AGENTFLOW_APP_DOMAIN__ : '') || '';
    } catch (err) {
      console.warn('[auth] get current user failed:', err);
    }
  }

  // 3. 自动拉起认证：只在配置的应用域名下把访客送到 SSO 登录页。localhost / 127.0.0.1 即使开启 AUTH_REQUIRED 也不跳转，改走本地验收登录。
  const isGuest = !state.currentUser || state.currentUser.isGuest;
  const bypassAutoAuth = new URLSearchParams(window.location.search).has('guest');
  const authErr = sessionStorage.getItem('agentflow_auth_err');

  if (shouldAutoSSORedirect({isGuest, hostname: window.location.hostname, bypassAutoAuth, authErr, loginUrl: state.loginUrl})) {
    window.location.href = state.loginUrl;
    return;
  }

  // Explicitly enabled loopback acceptance login is available before protected bootstrap.
  if(isGuest&&state.authRequired&&['localhost','127.0.0.1','[::1]'].includes(window.location.hostname)){
    const local=await api('/api/auth/local',{silent:true}).catch(()=>({enabled:false}));
    if(local.enabled){
      finishBootLoader();
      document.body.innerHTML='<main style="max-width:480px;margin:15vh auto;padding:32px"><h1>本地验收环境</h1><p>登录配置的本地验收账号后查看项目和业务域。</p><button id="localBootLogin" class="button primary">本地验收登录</button><p id="localBootError"></p></main>';
      $('#localBootLogin').onclick=async()=>{const button=$('#localBootLogin');button.disabled=true;try{await api('/api/auth/local',{method:'POST',body:'{}'});location.reload()}catch(e){$('#localBootError').textContent=e.message;button.disabled=false}};
      return;
    }
  }
  if(isGuest&&state.authRequired){location.replace('/login.html');return}
  renderUserBadge();

  const view=restoredView();
  const shell = await api('/api/bootstrap', {silent: view==='runs'});
  state.data = Object.assign(state.data, shell);
  const savedProject=urlParams.get('projectId')||localStorage.getItem('agentflow.activeProject');
  state.projectFilter=(state.data.projects||[]).some(p=>p.id===savedProject)?savedProject:defaultProjectId(state.data.projects);
  state.workflow = clone(restoreOpenWorkflow());
  state.workflowSaved = clone(state.workflow);
  state.workflowDirty = false;
  if(state.workflow?.id)localStorage.setItem('agentflow.activeWorkflow',state.workflow.id);
  renderProjectSwitcher(); updateApprovalBadge();
  ensureMCPMarketCatalog().then(()=>{if(activeViewName()==='sops')renderSOPCatalog()});
  listenForGongfengOAuth();
  window.addEventListener('hashchange',async()=>{
    const name=restoredView();
    const assetDetailHash=state.selectedAsset&&isProjectSharedAsset(activeViewName())?assetViewHash(state.assetType,state.selectedAsset):'';
    if(assetDetailHash&&location.hash!==assetDetailHash&&!(await confirmAssetLeave())){
      history.pushState({assetFrom:assetViewHash(state.assetType)},'',assetDetailHash);
      return;
    }
    if(activeViewName()!==name){
      if(name==='flow')await applyFlowRoute();
      if(name==='projects')syncProjectAdminRouteState();
      if(restoredView()===name)switchView(name);
      return;
    }
    if(name==='runs'){
      const id=runRouteId();
      if(id){if(state.selectedRun!==id)showRunDetail(id)}
      else if(state.selectedRun)showRunList();
      return;
    }
    if(name==='flow')applyFlowRoute();
    if(name==='agents')applyAgentRoute();
    if(isProjectSharedAsset(name))applyAssetRoute();
    if(name==='projects')applyProjectAdminRoute();
    if(name==='collaboration'){
      const id=routePart(1);
      if(id){const url=new URL(location.href);url.searchParams.set('chatSession',id);history.replaceState({},'',url);const frame=$('#chatNextFrame');if(frame){frame.dataset.src='';mountNativeChat()}}
    }
  });
  if(view==='projects')syncProjectAdminRouteState();
  renderAll();
  switchView(view);
  settleBootLoader();
  if(!['approvals','issues','runs','sops'].includes(state.currentView))preloadApprovalBadge();
  restoreRequirementComposer();
}
const BOOT_LOADER_CHAT_TIMEOUT_MS=8000;
let nativeChatReady=false,bootLoaderTimer=0;
function finishBootLoader(){
  clearTimeout(bootLoaderTimer);bootLoaderTimer=0;
  document.body.classList.remove('booting');
  const loader=document.getElementById('bootLoader');
  if(!loader?.isConnected||loader.classList.contains('leaving'))return;
  loader.setAttribute('aria-busy','false');
  if(matchMedia('(prefers-reduced-motion: reduce)').matches){loader.remove();return}
  loader.classList.add('leaving');
  const remove=()=>loader.remove();
  loader.addEventListener('transitionend',remove,{once:true});
  setTimeout(remove,250);
}
function settleBootLoader(){
  if(!document.body.classList.contains('chat-next-active')||nativeChatReady){finishBootLoader();return}
  clearTimeout(bootLoaderTimer);
  bootLoaderTimer=setTimeout(finishBootLoader,BOOT_LOADER_CHAT_TIMEOUT_MS);
}
window.addEventListener('pageshow',event=>{if(event.persisted)finishBootLoader()});
// Shared with the /chatbot/ iframe (chatbot/src/theme.js): light|dark|system, while data-theme stays light|dark.
const THEME_KEY='agentflow.theme.v3',LEGACY_THEME_KEY='agentflow.native-chat.theme',EARLIER_THEME_KEY='loopforge.theme.v3',themeMedia=matchMedia('(prefers-color-scheme: dark)');
function readThemePreference(){try{const current=localStorage.getItem(THEME_KEY),legacy=localStorage.getItem(LEGACY_THEME_KEY),earlier=localStorage.getItem(EARLIER_THEME_KEY),valid=value=>['light','dark','system'].includes(value);let preference='light';if(valid(current))preference=current;else if(valid(legacy)||valid(earlier)){preference=valid(legacy)?legacy:earlier;localStorage.setItem(THEME_KEY,preference)}if(legacy!==null)localStorage.removeItem(LEGACY_THEME_KEY);if(earlier!==null)localStorage.removeItem(EARLIER_THEME_KEY);return preference}catch{return'light'}}
function resolvedTheme(preference){return preference==='system'?(themeMedia.matches?'dark':'light'):preference==='dark'?'dark':'light'}
// Kept in memory so the switcher still reflects the choice when localStorage is unavailable.
let activeThemePreference='light',themeFadeTimer=0;
function applyTheme(preference=readThemePreference()){const root=document.documentElement,previous=root.dataset.theme,next=resolvedTheme(preference);activeThemePreference=preference;if(previous&&previous!==next&&!matchMedia('(prefers-reduced-motion: reduce)').matches){root.classList.add('theme-fading');clearTimeout(themeFadeTimer);themeFadeTimer=setTimeout(()=>root.classList.remove('theme-fading'),320)}root.dataset.theme=next;if(typeof renderThemeToggle==='function')renderThemeToggle()}
applyTheme();
themeMedia.addEventListener('change',()=>applyTheme());
window.addEventListener('storage',event=>{if(!event.key||event.key===THEME_KEY)applyTheme()});

function bind(){
	prepareMCPFormHints();
  $$('.nav[data-view]').forEach(btn=>btn.onclick=async()=>{
    if(!(await confirmAssetLeave()))return;
    if(btn.dataset.view==='projects'&&state.projectAdminDetailId){
      if(!(await confirmDiscardProjectDraft()))return;
      resetProjectAdminDetail();
    }
    if(btn.id==='railSettingsEntry'&&(settingsRouteTransition?.target?isSettingsManagementView(settingsRouteTransition.target):document.body.classList.contains('management-mode'))){
      switchView('collaboration');
    }else switchView(btn.dataset.view);
  });
  $('#settingsSidebarToggle')?.addEventListener('click',()=>{cancelSettingsRouteTransition();setSettingsSidebarExpanded(!settingsSidebarExpanded())});
  $('#railSidebarToggle')?.addEventListener('click',()=>{
    setPlatformRailExpanded(!platformRailExpanded());
  });
  document.addEventListener('click',event=>{
    if(document.body.classList.contains('management-mode')&&event.target instanceof Element&&event.target.closest('.collab-rail-toggle')){
      event.preventDefault();event.stopImmediatePropagation();
      setSettingsSidebarExpanded(!settingsSidebarExpanded());
    }
  },true);
  matchMedia('(max-width:1000px)').addEventListener('change',()=>setSettingsSidebarExpanded(settingsSidebarExpanded()));
  bindFlowDesigner();
  $('#workflowSelect').onchange=e=>switchFlowWorkflow(e.target.value);
  $('#workflowName').oninput=e=>{if(!state.workflow)return;state.workflow.name=e.target.value;dirty()};
  $('#workflowOwner').oninput=e=>{if(!state.workflow)return;state.workflow.owner=e.target.value;dirty()};
  $('#workflowSandbox').onchange=e=>{if(!state.workflow)return;state.workflow.sandboxProfileId=e.target.value;dirty()};
  $('#workflowStatus').onchange=e=>{if(!state.workflow)return;state.workflow.status=e.target.value;dirty()};
  $('#workflowTags').oninput=e=>{if(!state.workflow)return;state.workflow.tags=e.target.value.split(',').map(x=>x.trim()).filter(Boolean);dirty()};
  $('#workflowMetricCategory').onchange=e=>{if(!state.workflow)return;state.workflow.metricCategory=e.target.value;dirty()};
  $('#workflowSOPLane').onchange=e=>{if(!state.workflow)return;state.workflow.sopLane=e.target.value;dirty()};
  $('#workflowDescription').oninput=e=>{if(!state.workflow)return;state.workflow.description=e.target.value;dirty()};
  $('#saveWorkflow').onclick=saveWorkflow; $('#discardWorkflow').onclick=discardWorkflow; $('#publishWorkflow').onclick=publishWorkflow;
  $('#flowHistory')&&($('#flowHistory').onclick=()=>openRevisionHistory('workflow', state.workflow?.id, state.workflow?.name));
  $('#runButton').onclick=()=>openRunModal(state.workflow?.id);
  $('#flowSettingsToggle').onclick=()=>{const panel=$('#flowSettingsPanel');panel.hidden=!panel.hidden;$('#flowSettingsToggle').classList.toggle('active',!panel.hidden)};
  const closeFlowSettings=()=>{const panel=$('#flowSettingsPanel');if(panel){panel.hidden=true;$('#flowSettingsToggle')?.classList.remove('active')}};
  $('#flowSettingsClose')?.addEventListener('click',closeFlowSettings);
  $('#flowSettingsCollapse')?.addEventListener('click',closeFlowSettings);
  $('#flowSettingsQuickSave')?.addEventListener('click',saveWorkflow);
  const sandboxChip=$('#sandboxChip');
  if(sandboxChip)sandboxChip.onclick=()=>{
    const panel=$('#flowSettingsPanel');
    if(!panel)return;
    panel.hidden=false;
    $('#flowSettingsToggle')?.classList.add('active');
    const field=$('#workflowSandboxField');
    if(field){field.classList.add('highlight');setTimeout(()=>field.classList.remove('highlight'),1600)}
    const sel=$('#workflowSandbox');
    if(sel)sel.focus();
  };
  $$('.canvas-view-toggle [data-flow-view]').forEach(btn=>btn.onclick=()=>setFlowView(btn.dataset.flowView));
  const refreshRunnersBtn=$('#refreshRunners');if(refreshRunnersBtn)refreshRunnersBtn.onclick=()=>refreshRunners();
  $('#langToggle').onclick=()=>window.lfSetLang(window.lfLang()==='en'?'zh':'en');
  $('#themeToggle').onclick=toggleTheme;renderThemeToggle();
  const profileWrap = $('#userProfileWrap');
  const userBadge = $('#userBadge');
  const credentialsButton = $('#userCredentialsButton');
  if (userBadge && profileWrap) {
    // The card's visibility transition still reports hidden on its first frame, so wait until the switcher can be measured.
    const snapThemeSegment=(tries=12)=>requestAnimationFrame(()=>{const active=$('#userProfilePopover .upp-theme-seg .active');if(active&&getComputedStyle(active).visibility==='hidden'&&tries>0)return snapThemeSegment(tries-1);renderThemeSegment({snap:true})});
    profileWrap.addEventListener('mouseenter',()=>snapThemeSegment());
    const toggleProfile=()=>{profileWrap.classList.toggle('is-open');userBadge.setAttribute('aria-expanded',String(profileWrap.classList.contains('is-open')));if(profileWrap.classList.contains('is-open'))snapThemeSegment()};
    userBadge.onclick = (e) => {
      e.stopPropagation();
      toggleProfile();
    };
    if(credentialsButton)credentialsButton.onclick=e=>{e.stopPropagation();profileWrap.classList.add('is-open');userBadge.setAttribute('aria-expanded','true');setTimeout(()=>$('#credentialAddButton')?.focus(),0)};
    userBadge.onkeydown = (e) => {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        toggleProfile();
      }
    };
    const popover = $('#userProfilePopover');
    if (popover) {
      popover.onclick = (e) => e.stopPropagation();
    }
    document.addEventListener('click', (e) => {
      if (!profileWrap.contains(e.target)) {
        profileWrap.classList.remove('is-open');
        userBadge.setAttribute('aria-expanded', 'false');
      }
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        profileWrap.classList.remove('is-open');
        userBadge.setAttribute('aria-expanded', 'false');
      }
    });
  }
  $('#commandPaletteTrigger').onclick=openCommandPalette;
  $('#commandPalette').onclick=e=>{if(e.target===$('#commandPalette'))closeCommandPalette()};
  $('#commandSearch').oninput=renderCommandResults;
  $('#commandResults').onclick=e=>{const item=e.target.closest('[data-command-index]');if(item)executeCommand(Number(item.dataset.commandIndex))};
  $('#commandResults').onmousemove=e=>{const item=e.target.closest('[data-command-index]');if(!item)return;const idx=Number(item.dataset.commandIndex);if(idx!==commandSelectedIndex){commandSelectedIndex=idx;highlightCommandSelection()}};
  $$('[data-issue-view]').forEach(btn=>btn.onclick=()=>setIssueView(btn.dataset.issueView));
  setIssueView(state.issueView,false);
  $$('#runModal .modal-close').forEach(b=>b.onclick=()=>$('#runModal').classList.remove('open'));
  $$('.generic-template-close').forEach(b=>b.onclick=()=>$('#genericTemplateModal').classList.remove('open'));
  $('#confirmGenericTemplate')&&($('#confirmGenericTemplate').onclick=adoptGenericTemplate);
  $('#runGenericTemplateDirect')&&($('#runGenericTemplateDirect').onclick=()=>{const id=state.genericTemplateId;$('#genericTemplateModal').classList.remove('open');if(!id)return toast('请先选择通用模板');openRunModal(id)});
  $('#confirmRun').onclick=startRun; $('#newAgent').onclick=()=>openAgentWizard();
  $('#agentForm').onsubmit=saveAgent;
  $('#agentForm').elements.executorId?.addEventListener('change',e=>applyAgentModelUI(e.target.value,'',''));
  bindAgentPicker('#agentModelPicker',renderAgentModelPicker);
  bindAgentPicker('#agentEffortPicker',renderAgentEffortPicker);
  $('#agentClone')&&($('#agentClone').onclick=()=>cloneAgent(state.selectedAgent));
  $('#agentForm').addEventListener('input',()=>renderAgentPreview(state.agentDraft));
  $('#agentForm').addEventListener('change',()=>renderAgentPreview(state.agentDraft)); $('#refreshRuns').onclick=()=>refreshRuns(false);$('#runBackBtn')&&($('#runBackBtn').onclick=()=>showRunList());
	$('#refreshDashboard').onclick=refreshDashboard;
	$('#refreshIssues').onclick=refreshIssues;
	$('#refreshSOPs').onclick=refreshWorkflowMetrics;
  $('#agentSearch').oninput=renderAgents;$('#agentPrev').onclick=agentWizardPrev;$('#agentNext').onclick=agentWizardNext;$$('[data-wizard-steps="agent"] [data-agent-tab]').forEach(b=>b.onclick=()=>setAgentTab(b.dataset.agentTab));$$('[data-wizard-steps="agent-create"] [data-create-step]').forEach(b=>b.onclick=()=>{if(!b.disabled)setAgentCreateStep(Number(b.dataset.createStep))});bindAgentModalInputs();$$('.agent-close').forEach(b=>b.onclick=()=>$('#agentModal').classList.remove('open'));
  $('#newRunFromRuns')&&($('#newRunFromRuns').onclick=()=>switchView('issues'));
  $('#runSearch').oninput=e=>{state.runQuery=e.target.value.trim().toLowerCase();resetListPage('runs');renderRuns()};
  $('#runWorkflowFilter').onchange=e=>{state.runWorkflowFilter=e.target.value;resetListPage('runs');renderRuns()};
  $('#runFilters').onclick=e=>{const button=e.target.closest('[data-run-filter]');if(!button)return;state.runFilter=button.dataset.runFilter;resetListPage('runs');$$('[data-run-filter]').forEach(x=>x.classList.toggle('active',x===button));renderRuns()};
  $$('[data-asset-scope]').forEach(btn=>btn.onclick=()=>{if(state.selectedAsset)return;const key=assetScopeKey(state.assetType);if(state[key]!==btn.dataset.assetScope)state.assetQuery='';state[key]=btn.dataset.assetScope;renderAssets()});
  $('#newAsset').onclick=()=>openAssetDetail('__new__');
  $('#assetSearch').oninput=e=>{state.assetQuery=e.target.value;renderAssetGrid()};
  $('#assetForm').onsubmit=saveAsset;
  const markAssetFormDirty=e=>{if(e.target instanceof HTMLInputElement&&e.target.type==='file')return;markAssetDirty()};
  $('#assetForm').addEventListener('input',markAssetFormDirty);
  $('#assetForm').addEventListener('change',markAssetFormDirty);
  // 技能文件上传：事件委托绑定在持久表单节点上，表单 innerHTML 重建不影响。
  // 每次渲染直接挂 onchange 会因轮询重建表单时 input 被替换而丢事件。
  $('#assetForm').addEventListener('change',e=>{
    if(e.target.id==='skillFolderPick'||e.target.id==='skillZipPick')uploadSkillFiles(e.target);
    if(e.target.id==='skillMdPick')uploadSkillMd(e.target);
  });
  $('#assetForm').addEventListener('click',e=>{
    const previewBtn=e.target.closest('[data-skill-file-preview]');
    if(previewBtn){
      e.preventDefault();
      if(previewBtn.disabled)return;
      openSkillFilePreview(selectedSkill(), previewBtn.getAttribute('data-skill-file-preview'));
      return;
    }
    const delBtn=e.target.closest('[data-skill-file-del]');
    if(delBtn){
      e.preventDefault();
      if(skillWriteBlocked())return toast('共享资产只读');
      deleteSkillFile(delBtn.getAttribute('data-skill-file-del'));
      return;
    }
    const tree=e.target.closest('[data-tree-toggle]');
    if(tree){
      const key=tree.getAttribute('data-tree-toggle');
      if(!(state.skillTreeCollapsed instanceof Set))state.skillTreeCollapsed=new Set();
      if(state.skillTreeCollapsed.has(key))state.skillTreeCollapsed.delete(key);else state.skillTreeCollapsed.add(key);
      refreshSkillFileTree();
      return;
    }
    const folderBtn=e.target.closest('#skillFolderBtn');
    const zipBtn=e.target.closest('#skillZipBtn');
    const mdBtn=e.target.closest('#skillMdBtn');
    if(folderBtn||zipBtn||mdBtn){
      if(state.selectedAsset==='__new__'||!state.selectedAsset)return toast('先保存技能，再上传文件');
      if(skillWriteBlocked())return toast('共享资产只读');
    }
    if(folderBtn){
      const inp=$('#skillFolderPick');if(!inp)return;
      inp.click();
      // 兜底：个别场景 change 不触发，轮询 files 变化
      let seen=0;
      const poll=setInterval(()=>{
        const n=inp.files?inp.files.length:0;
        if(n>0&&n!==seen){seen=n;clearInterval(poll);uploadSkillFiles(inp)}
      },300);
      setTimeout(()=>clearInterval(poll),180000);
    }
    if(zipBtn){
      const inp=$('#skillZipPick');if(!inp)return;
      inp.click();
      let seen=0;
      const poll=setInterval(()=>{
        const n=inp.files?inp.files.length:0;
        if(n>0&&n!==seen){seen=n;clearInterval(poll);uploadSkillFiles(inp)}
      },300);
      setTimeout(()=>clearInterval(poll),180000);
    }
    if(mdBtn){
      const inp=$('#skillMdPick');if(!inp)return;
      inp.click();
      let seen=0;
      const poll=setInterval(()=>{
        const n=inp.files?inp.files.length:0;
        if(n>0&&n!==seen){seen=n;clearInterval(poll);uploadSkillMd(inp)}
      },300);
      setTimeout(()=>clearInterval(poll),180000);
    }
  });
  $$('[data-asset-type-tab]').forEach(btn=>{
    btn.onclick=async()=>{
      const next=btn.dataset.assetTypeTab;
      if(next===activeViewName()||!(await confirmAssetLeave()))return;
      switchView(next);
      $$(`[data-asset-type-tab="${next}"]`).find(tab=>tab.getClientRects().length)?.focus();
    };
    btn.onkeydown=e=>{
      if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;
      const tabs=[...btn.parentElement.querySelectorAll('[data-asset-type-tab]')],i=tabs.indexOf(btn);
      const to=e.key==='Home'?0:e.key==='End'?tabs.length-1:(i+(e.key==='ArrowRight'?1:-1)+tabs.length)%tabs.length;
      e.preventDefault();tabs[to].click();
    };
  });
  const mcpScopeSelect=$('#mcpScopeSelect');
  if(mcpScopeSelect){window.enhanceSelect?.(mcpScopeSelect);mcpScopeSelect.onchange=()=>{state.mcpScope=mcpScopeSelect.value==='personal'?'personal':'project';resetListPage('mcp');renderMCPManagement()}}
  let mcpSearchTimer=0;
  $('#mcpMarketSearch')&&($('#mcpMarketSearch').oninput=e=>{state.mcpQuery=e.target.value.trim().toLowerCase();resetListPage('mcp');clearTimeout(mcpSearchTimer);mcpSearchTimer=setTimeout(()=>renderMCPManagement(),120)});
  $('#newCustomMcp')&&($('#newCustomMcp').onclick=()=>openCustomMCPModal());
  $$('.custom-mcp-close').forEach(btn=>btn.onclick=()=>$('#customMcpModal').classList.remove('open'));
  $('#customMcpForm')&&($('#customMcpForm').onsubmit=saveCustomMCP);
  $('#customMcpForm')?.addEventListener('change',e=>{
    if(e.target.name==='transport')mcpSetTransport(e.target.value);
    if(e.target.id==='mcpFolderPick'||e.target.id==='mcpZipPick'){
      const other=$(e.target.id==='mcpFolderPick'?'#mcpZipPick':'#mcpFolderPick');if(other)other.value='';mcpUpdateFileSummary(e.target.files);
    }
  });
  $('#customMcpForm')?.addEventListener('click',async e=>{
    const add=e.target.closest('[data-mcp-kv-add]');if(add){e.preventDefault();mcpAddKVRow(add.dataset.mcpKvAdd);return}
    const del=e.target.closest('[data-mcp-kv-del]');if(del){e.preventDefault();del.closest('.kv-editor-row')?.remove();return}
    if(e.target.closest('#mcpJsonParseBtn')){e.preventDefault();try{await importMCPJSONText($('#mcpJsonInput')?.value||'')}catch(err){toast(err.message,'err')}return}
    if(e.target.closest('#mcpFolderBtn')){e.preventDefault();$('#mcpFolderPick')?.click()}
    if(e.target.closest('#mcpZipBtn')){e.preventDefault();$('#mcpZipPick')?.click()}
  });
  $('#agentHistory')&&($('#agentHistory').onclick=()=>openRevisionHistory('agent', state.selectedAgent, $('#agentForm')?.elements?.name?.value));
  $('#revisionClose')&&($('#revisionClose').onclick=closeRevisionHistory);
  $('#revisionClose2')&&($('#revisionClose2').onclick=closeRevisionHistory);
  $('#revisionRestore')&&($('#revisionRestore').onclick=restoreSelectedRevision);
  $('#newIssue').onclick=openRequirementWizard;
  $('#importGitHubIssues').onclick=syncNow;
  $('#importTAPDIssue')&&($('#importTAPDIssue').onclick=openImportTAPDModal);
  $$('.import-tapd-close').forEach(b=>b.onclick=()=>$('#importTAPDModal').classList.remove('open'));
  $('#importTAPDForm')&&($('#importTAPDForm').onsubmit=importTAPDStory);
  $('#editScheduleTrigger')&&($('#editScheduleTrigger').onclick=()=>openScheduleJobModal('dispatch'));
  $('#newAISchedule').onclick=()=>{selectAIScheduleTab('project');openScheduleJobModal('assistant')};
  $('#refreshAISchedules').onclick=refreshAISchedules;
  bindSharedScheduleUI();
  bindProjectAdmin();bindRepoDrawer();
  $$('.requirement-close').forEach(b=>b.onclick=()=>closeRequirementComposer());
  $('#reqNext').onclick=()=>startReqClarify();
  $('#reqSkip').onclick=()=>skipReqClarify();
  $('#reqBack').onclick=()=>backReqStep();
  $('#reqGoCreate').onclick=()=>goReqStep(3);
  $('#reqCreate').onclick=()=>createIssueFromChat();
  $('#chatStop').onclick=()=>abortChatClarify();
  $('#reqReclarify').onclick=()=>reclarifyRequirement();
  $('#reqTraceToggle').onclick=()=>toggleReqTrace();
  $('#reqRepoChipToggle').onclick=e=>{e.stopPropagation();toggleReqRepoPopover()};
  $$('#reqSteps [data-req-step]').forEach(b=>b.onclick=()=>openReqStep(Number(b.dataset.reqStep)));
  $$('#reqTarget [data-req-target]').forEach(b=>b.onclick=()=>setReqTarget(b.dataset.reqTarget));
  document.addEventListener('pointerdown',e=>{
    const pop=$('#reqRepoPopover');
    if(pop&&!pop.hidden&&!e.target.closest?.('#reqRepoChip, .hui-select-panel'))toggleReqRepoPopover(false);
  });
  $('#chatInput').addEventListener('keydown',e=>{if(e.key==='Enter'&&(e.metaKey||e.ctrlKey)){e.preventDefault();startReqClarify()}});
  $('#reqWorkflow')&&($('#reqWorkflow').onchange=()=>{renderReqFooter();persistRequirementComposer()});
  $('#reqBaseBranch')?.addEventListener('input',onReqBaseBranchInput);
  $('#reqTitle').addEventListener('input',onReqTitleInput);
  $('#chatInput').addEventListener('input',onReqDescriptionInput);
  window.addEventListener('beforeunload',()=>persistRequirementComposer());
  if($('#sopCreateForm')){$('#sopCreateForm').onsubmit=createSOPFromWizard;$('#sopPrev').onclick=()=>setSOPWizardStep(state.sopWizardStep-1);$('#sopNext').onclick=()=>setSOPWizardStep(state.sopWizardStep+1);$$('[data-wizard-steps="sop"] [data-step-target]').forEach(b=>b.onclick=()=>setSOPWizardStep(Number(b.dataset.stepTarget)));$$('.sop-create-close').forEach(b=>b.onclick=()=>$('#sopCreateModal').classList.remove('open'))}
  $('#newSOP')&&($('#newSOP').onclick=openSOPWizard);
  $('#newSOPFromFlow')&&($('#newSOPFromFlow').onclick=openSOPWizard);
  $('#sopHealthFilter').onchange=renderSOPCatalog;$('#sopSort').onchange=renderSOPCatalog;
  if($('#manageRepositories'))$('#manageRepositories').onclick=openRepositoryModal;
  $$('.repository-close').forEach(b=>b.onclick=()=>$('#repositoryModal').classList.remove('open'));
  if($('#repositoryForm'))$('#repositoryForm').onsubmit=saveRepository;
  $('#sourceFilter').onchange=e=>{state.sourceFilter=e.target.value;resetListPage('issues');renderIssues()};
  $('#issueStatusFilter')&&($('#issueStatusFilter').onchange=e=>{state.issueStatusFilter=e.target.value;resetListPage('issues');renderIssues()});
  $('#issueAssigneeFilter')&&($('#issueAssigneeFilter').onchange=e=>{state.issueAssigneeFilter=e.target.value;resetListPage('issues');renderIssues()});
  $('#issueRunFilter').onchange=e=>{state.issueRunFilter=e.target.value;resetListPage('issues');renderIssues()};
  $('#issueSearch').oninput=e=>{state.issueQuery=e.target.value.trim().toLowerCase();resetListPage('issues');renderIssues()};
  $$('.issue-close').forEach(b=>b.onclick=()=>$('#issueModal').classList.remove('open'));
  $('#issueForm').onsubmit=saveIssue;
  $('#dispatchIssue').onclick=dispatchIssue;
  $('#syncNowBtn').onclick=syncNow;
  $('#saveSyncConfig').onclick=saveSyncConfig;
  $('#scheduleJobForm').onsubmit=saveScheduleJob;
  $('#scheduleTimingTabs')?.addEventListener('click',e=>{
    const tab=e.target.closest('[data-schedule-tab]');
    if(!tab)return;
    const f=$('#scheduleJobForm');
    f.elements.scheduleMode.value=tab.dataset.scheduleTab;
    syncScheduleJobTiming();
    if(f.elements.scheduleMode.value==='cron')f.elements.cron.focus();
    else f.elements.intervalSec.focus();
  });
  bindScheduleModelPicker();
  bindClarifyModelPicker($('#reqClarifyModelPicker'));
  $('#pushToGongfeng').onclick=()=>pushIssue('gongfeng');
  $('#pushToTAPD').onclick=()=>pushIssue('tapd');
  $('#openSyncModal').onclick=()=>{$('#syncModal').classList.add('open');refreshSyncStatus()};
  bindIssueHeadingMenu();
  enhanceIssueFilters();
  $$('.sync-close').forEach(b=>b.onclick=()=>$('#syncModal').classList.remove('open'));
  $$('.schedule-close').forEach(b=>b.onclick=closeScheduleJobModal);
  $('#syncAutoEnabledInline').onchange=e=>{$('#syncAutoEnabled').checked=e.target.checked;saveSyncConfig()};
  $$('.artifact-close').forEach(b=>b.onclick=()=>$('#artifactModal').classList.remove('open'));
  $$('.modal').forEach(modal=>modal.addEventListener('pointerdown',e=>{
    if(e.target!==modal)return;
    if(modal.id==='requirementModal')return;
    if(modal.id==='confirmModal')return;
    if(modal.id==='scheduleModal')return closeScheduleJobModal();
    modal.classList.remove('open');
  }));
  document.addEventListener('keydown',e=>{
    if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='k'){e.preventDefault();openCommandPalette();return}
    if(!$('#commandPalette').hidden){
      if(e.key==='Escape'){closeCommandPalette();return}
      if(e.key==='ArrowDown'){e.preventDefault();moveCommandSelection(1);return}
      if(e.key==='ArrowUp'){e.preventDefault();moveCommandSelection(-1);return}
      if(e.key==='Enter'){e.preventDefault();executeCommand(commandSelectedIndex);return}
    }
    if(e.key==='Escape'){
      if(flowDesignerEscape(e))return;
      if($('#artifactModal')?.classList.contains('open')){$('#artifactModal').classList.remove('open');return}
      if(state.drawerOpen)return closeIssueDrawer();
      if($('#requirementModal')?.classList.contains('open')){
        if(!$('#reqRepoPopover')?.hidden){toggleReqRepoPopover(false);return}
        closeRequirementComposer();return
      }
      const open=[...$$('.modal.open')].pop();
      if(open?.id==='scheduleModal')return closeScheduleJobModal();
      if(open)return open.classList.remove('open');
      const panel=$('#flowSettingsPanel');
      if(panel&&!panel.hidden){panel.hidden=true;$('#flowSettingsToggle')?.classList.remove('active');return}
      if($('#flowView')?.classList.contains('active')&&closeNodeDrawer())return;
    }
  });

  // --- 工作台 / 待办验收 / 可观测与分析 / Issue 详情抽屉 ---
  $('.platform-sidebar .brand')?.addEventListener('click',()=>switchView('collaboration'));
  $('.rail-brand') && ($('.rail-brand').onclick=()=>switchView('collaboration'));
  $('#railUserBadge') && ($('#railUserBadge').onclick=()=>switchView('settings'));
  (function bindSidebarResize(){
    const KEY=SIDEBAR_WIDTH_KEY,MIN=220,MAX=420,wide=matchMedia('(min-width:1001px)'),sidebar=$('.platform-sidebar');
    const handle=document.createElement('button');handle.type='button';handle.className='sidebar-resize';handle.title='拖动调整侧栏宽度，双击恢复默认';handle.setAttribute('aria-label','调整侧栏宽度');sidebar.append(handle);
    const clamp=px=>Math.round(Math.min(MAX,Math.max(MIN,px)));
    const apply=()=>{const saved=Number(localStorage.getItem(KEY));if(wide.matches&&saved)document.documentElement.style.setProperty('--sidebar-w',clamp(saved)+'px');else document.documentElement.style.removeProperty('--sidebar-w')};
    const save=px=>{localStorage.setItem(KEY,String(clamp(px)));apply()};
    apply();wide.addEventListener('change',apply);
    window.addEventListener('storage',event=>{if(!event.key||event.key===KEY||event.key===SIDEBAR_LAYOUT_KEY)apply()});
    handle.onpointerdown=ev=>{
      if(ev.button!==0)return;ev.preventDefault();
      const startX=ev.clientX,startW=sidebar.getBoundingClientRect().width;
      handle.setPointerCapture(ev.pointerId);document.body.classList.add('sidebar-resizing');
      handle.onpointermove=move=>document.documentElement.style.setProperty('--sidebar-w',clamp(startW+move.clientX-startX)+'px');
      handle.onpointerup=handle.onpointercancel=()=>{handle.onpointermove=handle.onpointerup=handle.onpointercancel=null;document.body.classList.remove('sidebar-resizing');save(sidebar.getBoundingClientRect().width)};
    };
    handle.ondblclick=()=>{localStorage.removeItem(KEY);apply()};
    handle.onkeydown=ev=>{const cur=sidebar.getBoundingClientRect().width;if(ev.key==='ArrowRight'){ev.preventDefault();save(cur+16)}if(ev.key==='ArrowLeft'){ev.preventDefault();save(cur-16)}};
  })();
  $('#dashImportTAPD')&&($('#dashImportTAPD').onclick=openImportTAPDModal);
  $('#dashNewRequirement').onclick=openRequirementWizard;
  $('#dashNewRun').onclick=()=>switchView('issues');
  $$('[data-jump]').forEach(el=>el.onclick=()=>switchView(el.dataset.jump));
  $('#refreshApprovals').onclick=()=>renderApprovals(false);
  $('#refreshObs').onclick=refreshObs;
  $('#exportObsReport').onclick=exportObsReport;
  $$('.obs-tab').forEach(btn=>btn.onclick=()=>switchObsTab(btn.dataset.obsTab));
  $$('.drawer-tab').forEach(btn=>btn.onclick=()=>{state.issueDetailTab=btn.dataset.dtab;$$('.drawer-tab').forEach(x=>x.classList.toggle('active',x===btn));renderIssueDetailTab()});
  const issueDrawer=$('#issueDetailDrawer');
  if(issueDrawer&&issueDrawer.parentElement!==document.body)document.body.appendChild(issueDrawer);
  $$('[data-close-drawer]').forEach(el=>el.onclick=closeIssueDrawer);
  $('#projectSwitcher').onchange=async e=>{
    if(!await confirmAssetLeave()){e.target.value=state.projectFilter||'';return}
    const frame=$('#chatNextFrame');
    if(frame?.contentWindow)frame.contentWindow.postMessage({type:'agentflow-project-context',projectId:e.target.value},location.origin);
    closeScheduleJobModal();
    state.projectFilter=e.target.value;state.selectedIssue=null;
    if(state.currentView==='aiSchedules')refreshAISchedules();
    localStorage.setItem('agentflow.activeProject',state.projectFilter);
    if($('#botsView')?.classList.contains('active'))loadBots();
    resetListPages();
    syncWorkflowToProject();
    renderProjectSwitcher();renderIssues();renderSync();renderProjectAdmin();
    renderSOPCatalog();renderAgents();renderAssets();renderMCPManagement();renderRuns();renderWorkflowMeta();
    renderApprovals(true);
    renderObsTabs();
    if($('#dashboardView').classList.contains('active'))renderDashboard();
    if($('#observabilityView').classList.contains('active'))refreshObs();
    if($('#sopsView').classList.contains('active'))refreshWorkflowMetrics();
  };
  document.querySelectorAll('[data-scope-for]').forEach(bar=>{
    bar.querySelectorAll('[data-scope]').forEach(btn=>btn.onclick=()=>{
      const key=bar.dataset.scopeFor==='agent'?'agentScope':'sopScope';
      state[key]=btn.dataset.scope;
      if(key==='agentScope')renderAgents();else renderSOPCatalog();
    });
  });
  bindGlobalModals();
}
function bindGlobalModals(){
  bindConfirmModal($('#confirmModal'));

  const codeModal=$('#codeEditorModal');
  if(codeModal){
    const textarea=$('#codeEditorTextarea');
    const closeBtn=$('#codeEditorClose');
    const cancelBtn=$('#codeEditorCancel');
    const saveBtn=$('#codeEditorSave');
    const copyBtn=$('#codeEditorCopy');
    const fullscreenBtn=$('#codeEditorFullscreen');

    const closeEditor=()=>{
      codeModal.classList.remove('open');
      currentCodeEditorSave=null;
    };
    closeBtn?.addEventListener('click', closeEditor);
    cancelBtn?.addEventListener('click', closeEditor);
    codeModal.addEventListener('click', (e)=>{
      if(e.target===codeModal) closeEditor();
    });

    copyBtn?.addEventListener('click', ()=>{
      if(textarea) copyText(textarea.value, '代码已复制到剪贴板');
    });

    fullscreenBtn?.addEventListener('click', ()=>{
      const card=codeModal.querySelector('.code-editor-card');
      if(card){
        card.classList.toggle('fullscreen');
        fullscreenBtn.textContent=card.classList.contains('fullscreen')?'缩回窗口':'⛶ 全屏';
      }
    });

    saveBtn?.addEventListener('click', ()=>{
      if(currentCodeEditorSave&&textarea){
        const lang=$('#codeEditorLang')?.value||'shell';
        currentCodeEditorSave(textarea.value, lang);
      }
      closeEditor();
      toast('代码已保存并应用', 'ok');
    });

    if(textarea){
      textarea.addEventListener('keydown', (e)=>{
        if(e.key==='Tab'){
          e.preventDefault();
          const start=textarea.selectionStart;
          const end=textarea.selectionEnd;
          const val=textarea.value;
          if(e.shiftKey){
            const lineStart=val.lastIndexOf('\n', start-1)+1;
            if(val.slice(lineStart, lineStart+2)==='  '){
              textarea.value=val.slice(0, lineStart)+val.slice(lineStart+2);
              textarea.selectionStart=Math.max(lineStart, start-2);
              textarea.selectionEnd=Math.max(lineStart, end-2);
              textarea.dispatchEvent(new Event('input'));
            }
          } else {
            textarea.value=val.substring(0, start)+'  '+val.substring(end);
            textarea.selectionStart=textarea.selectionEnd=start+2;
            textarea.dispatchEvent(new Event('input'));
          }
        } else if(e.key==='Enter'){
          const start=textarea.selectionStart;
          const val=textarea.value;
          const lastNewline=val.lastIndexOf('\n', start-1);
          const prevLine=val.slice(lastNewline+1, start);
          const match=prevLine.match(/^(\s+)/);
          let indent=match?match[1]:'';
          if(prevLine.trim().endsWith(':') || prevLine.trim().endsWith('then') || prevLine.trim().endsWith('do')){
            indent+='  ';
          }
          if(indent){
            e.preventDefault();
            textarea.value=val.substring(0, start)+'\n'+indent+val.substring(textarea.selectionEnd);
            textarea.selectionStart=textarea.selectionEnd=start+1+indent.length;
            textarea.dispatchEvent(new Event('input'));
          }
        } else if((e.metaKey||e.ctrlKey) && (e.key==='s'||e.key==='S')){
          e.preventDefault();
          saveBtn?.click();
        } else if(e.key==='Escape'){
          e.preventDefault();
          closeEditor();
        }
      });
    }
  }
}
// Until someone picks a project (here, in the chat or through a ?projectId=
// link) the chat opens as a personal conversation, so a first-time user sees
// the project picker instead of silently landing in the default project. The
// default project still scopes the workbench pages.
function nativeChatProjectId(){
  const chosen=new URL(location.href).searchParams.has('projectId')||!!localStorage.getItem('agentflow.activeProject');
  return chosen?($('#projectSwitcher')?.value||state.projectFilter||''):'';
}
function postNativeChatProject(frame){
  frame.contentWindow?.postMessage({type:'agentflow-project-context',projectId:nativeChatProjectId()},location.origin);
  frame.contentWindow?.postMessage({type:'agentflow-environments-changed'},location.origin);
}
// A project created, deleted or restored from the chat must reach the project
// switcher without a page refresh.
function reloadShellProjects(){
  return api('/api/bootstrap',{silent:true}).then(shell=>{Object.assign(state.data,shell);renderAll();return state.data.projects||[]});
}
function mountNativeChat(){
  const frame=$('#chatNextFrame');
  if(!frame)return;
  if(!frame.dataset.nativeBridge){
    frame.dataset.nativeBridge='1';
    frame.addEventListener('load',()=>postNativeChatProject(frame));
    window.addEventListener('message',event=>{
      if(event.origin!==location.origin||event.source!==frame.contentWindow)return;
      const message=event.data||{};
      if(message.type==='agentflow-chat-ready'){
        nativeChatReady=true;
        postNativeChatProject(frame);
        finishBootLoader();
      }
      if(message.type==='agentflow-chat-session'){
        const id=message.sessionId||'';
        if(id&&!/^[a-zA-Z0-9_-]{1,64}$/.test(id))return;
        const url=new URL(location.href);
        if(id)url.searchParams.set('chatSession',id);else url.searchParams.delete('chatSession');
        url.searchParams.delete('chatIssue');
        if(activeViewName()==='collaboration')url.hash=id?'#/collaboration/'+encodeURIComponent(id):'#/collaboration';
        history.replaceState({},'',url);
      }
      if(message.type==='agentflow-chat-project'){
        const projectId=typeof message.projectId==='string'?message.projectId:'';
        const known=()=>!projectId||(state.data.projects||[]).some(project=>project.id===projectId);
        const follow=()=>{
          if(!known())return;
          localStorage.setItem('agentflow.activeProject',projectId);
          if(state.projectFilter===projectId)return;
          const select=$('#projectSwitcher');
          const option=[...select.options].find(item=>item.value===projectId);
          if(option)option.selected=true;else select.value=projectId;
          select.dispatchEvent(new Event('change',{bubbles:true}));
        };
        if(known())follow();
        else reloadShellProjects().then(follow).catch(()=>{});
      }
      if(message.type==='agentflow-projects-changed')reloadShellProjects().catch(()=>{});
      if(message.type==='agentflow-welcome-demo'&&!frame.dataset.demoBusy){
        frame.dataset.demoBusy='true';
        api('/api/runs',{method:'POST',body:JSON.stringify({workflowId:'welcome-workflow',projectId:'welcome',input:'首次体验：运行脚本、审批并交付产物'})}).then(run=>{
          location.hash='#/runs/'+encodeURIComponent(run.id);
        }).catch(error=>toast(error.message)).finally(()=>{delete frame.dataset.demoBusy});
      }
      if(message.type==='agentflow-quick-access-completed'){
        api('/api/bootstrap',{silent:true}).then(shell=>{
          Object.assign(state.data,shell);
          const pid=typeof message.projectId==='string'?message.projectId:'';
          if(pid&&(state.data.projects||[]).some(p=>p.id===pid)){
            state.projectFilter=pid;
            localStorage.setItem('agentflow.activeProject',pid);
          }
          renderAll();
        }).catch(()=>{});
      }
      if(message.type==='agentflow-chat-open-view'&&['settings','runners','sops','projects','runs','agents','environments','dockerfiles','sandboxes','flow','mcp'].includes(message.view)){
        if(message.view==='settings'&&CREDENTIAL_FIELDS[message.credential])gotoCredential(message.credential,message.sshRemote);
        else if(message.view==='sandboxes'&&message.action==='create')createConversationSandbox(message.projectId).catch(error=>toast(error.message));
        else if(message.view==='runs'&&/^run-[\w-]+$/.test(message.runId||''))location.hash='#/runs/'+encodeURIComponent(message.runId);
        else switchView(message.view);
      }
    });
  }
  if(frame.dataset.src!=='native'){
    frame.dataset.src='native';
    const topParams=new URL(location.href).searchParams;
    const projectId=nativeChatProjectId();
    const sessionId=topParams.get('chatSession')||'';
    const issueId=topParams.get('chatIssue')||'';
    const quickAccess=topParams.get('quickAccess')==='1';
    if(quickAccess||topParams.get('gongfeng')==='ok'){
      // renderPersonalSettings still owes the OAuth return toast after the URL is cleaned.
      if(topParams.get('gongfeng')==='ok')state.gongfengReturned=true;
      topParams.delete('quickAccess');
      topParams.delete('gongfeng');
      const clean=topParams.toString();
      history.replaceState({},'',location.pathname+(clean?'?'+clean:'')+location.hash);
    }
    frame.src='/chatbot/?projectId='+encodeURIComponent(projectId)+(sessionId?'&session='+encodeURIComponent(sessionId):issueId?'&issueId='+encodeURIComponent(issueId):'')+(quickAccess?'&quickAccess=1':'');
  }
  postNativeChatProject(frame);
}
function settingsSidebarExpanded(){
  return document.body.classList.contains('management-mode')&&!document.body.classList.contains('settings-sidebar-hidden');
}
let settingsSidebarEntryFrame=0;
let settingsSidebarWasManaged=false;
const settingsManagementViews=['settings','projects','bots','sops','flow','runs','aiSchedules','agents','mcp','rules','skills','runners','dockerfiles','environments','sandboxes','guide','platform'];
const settingsRouteMotion=matchMedia('(prefers-reduced-motion: reduce)');
let settingsRouteTransition=null;
let settingsRouteApplying=false;
function isSettingsManagementView(name){return settingsManagementViews.includes(name)}
function settingsRouteSurface(name){
  if(isSettingsManagementView(name)){
    const card=$('#settingsSidebar'),nav=card?.querySelector('nav');
    const header=card?.querySelector('.settings-navigation-header');
    if(!card||!nav||!header)return null;
    const rect=card.getBoundingClientRect(),top=header.getBoundingClientRect().bottom;
    return {nav,expanded:settingsSidebarExpanded(),geometry:{left:rect.left+1,top,width:rect.width-2,height:Math.max(0,rect.bottom-top-1),background:getComputedStyle(card).backgroundColor}};
  }
  const frame=$('#chatNextFrame');
  try{
    const shell=frame?.contentDocument?.querySelector('.chat-shell');
    const nav=shell?.querySelector('.conversation-sidebar'),header=shell?.querySelector('.brand');
    if(!shell||!nav||!header)return null;
    const frameRect=frame.getBoundingClientRect(),width=parseFloat(shell.style.getPropertyValue('--sidebar-width'))||304;
    const top=frameRect.top+header.getBoundingClientRect().bottom;
    return {nav,shell,expanded:!shell.classList.contains('sidebar-hidden'),geometry:{left:frameRect.left+1,top,width:width-2,height:Math.max(0,frameRect.bottom-14-top-1),background:shell.ownerDocument.defaultView.getComputedStyle(shell).backgroundColor}};
  }catch{return null}
}
function cancelSettingsRouteTransition(){
  const transition=settingsRouteTransition;
  settingsRouteTransition=null;
  clearTimeout(transition?.timer);
  transition?.motion?.cancel();
  transition?.observer?.disconnect();
  for(const held of transition?.held||[]){
    if(held.visibility)held.nav.style.setProperty('visibility',held.visibility,held.priority);
    else held.nav.style.removeProperty('visibility');
    held.nav.inert=held.inert;
  }
  document.body.classList.remove('settings-mode-switch');
}
function startSettingsRouteTransition(name){
  const apply=()=>{
    settingsRouteApplying=true;
    try{switchView(name)}finally{settingsRouteApplying=false}
    const focused=document.activeElement;
    if(focused&&focused!==document.body&&!focused.getClientRects().length){
      $('#railSettingsEntry')?.focus({preventScroll:true});
    }
  };
  const current=settingsRouteTransition;
  if(current?.motion){
    const reversing=isSettingsManagementView(current.target)!==isSettingsManagementView(name);
    current.target=name;
    apply();
    if(reversing)current.motion.reverse();
    return;
  }
  cancelSettingsRouteTransition();
  const fromName=activeViewName(),from=settingsRouteSurface(fromName);
  const home=settingsRouteSurface('collaboration');
  const homeSettled=!home?.shell?.matches('.sidebar-entering,.sidebar-transitioning,.sidebar-resizing');
  const preserveGeometry=!!from?.expanded&&homeSettled&&(isSettingsManagementView(name)||!!home?.expanded);
  const transition={from:fromName,to:name,target:name,preserveGeometry,held:[],motion:null,observer:null,timer:0};
  settingsRouteTransition=transition;
  if(from?.expanded&&!isSettingsManagementView(name)&&home&&!home.expanded){
    // Returning to an intentionally closed drawer needs one close, not a
    // close/reopen cycle. Keep the current page mounted until it completes.
    setSettingsSidebarExpanded(false);
    transition.timer=setTimeout(()=>{
      if(settingsRouteTransition!==transition)return;
      apply();
      cancelSettingsRouteTransition();
    },240);
    return;
  }
  // Grasp keeps its card mounted: only the navigation inside it changes mode.
  // Suppress the host's unrelated two/three-column grid interpolation here.
  document.body.classList.add('settings-mode-switch');
  if(home?.geometry.width>0)document.documentElement.style.setProperty('--sidebar-w',`${home.geometry.width+2}px`);
  const hold=nav=>{
    if(!nav||transition.held.some(held=>held.nav===nav))return;
    const doc=nav.ownerDocument;
    if(nav.contains(doc.activeElement))$('#railSettingsEntry')?.focus({preventScroll:true});
    transition.held.push({nav,visibility:nav.style.getPropertyValue('visibility'),priority:nav.style.getPropertyPriority('visibility'),inert:nav.inert});
    nav.style.setProperty('visibility','hidden','important');
    nav.inert=true;
  };
  try{
    const helper=window.AgentFlowSidebarMotion;
    const oldPane=preserveGeometry&&helper?.capture(from.nav,from.geometry);
    if(oldPane)hold(from.nav);
    apply();
    const target=settingsRouteSurface(name);
    const newPane=oldPane&&target?.expanded&&helper.capture(target.nav,target.geometry);
    if(!newPane){cancelSettingsRouteTransition();return}
    hold(target.nav);
    transition.motion=helper.play(oldPane,newPane,{direction:isSettingsManagementView(name)?'settings':'workspace',duration:320});
    if(home?.shell){
      transition.observer=new MutationObserver(()=>{
        if(home.shell.matches('.sidebar-hidden,.sidebar-resizing'))cancelSettingsRouteTransition();
      });
      transition.observer.observe(home.shell,{attributes:true,attributeFilter:['class']});
    }
    transition.motion.finished.finally(()=>{
      if(settingsRouteTransition===transition)cancelSettingsRouteTransition();
    });
  }catch(error){
    cancelSettingsRouteTransition();
    if(activeViewName()!==name)apply();
    console.error('Sidebar navigation transition failed',error);
  }
}
window.addEventListener('resize',()=>{
  const pending=settingsRouteTransition?.target;
  cancelSettingsRouteTransition();
  if(pending&&activeViewName()!==pending){
    settingsRouteApplying=true;
    try{switchView(pending)}finally{settingsRouteApplying=false}
  }
});
settingsRouteMotion.addEventListener('change',()=>{
  const pending=settingsRouteTransition?.target;
  cancelSettingsRouteTransition();
  if(pending)switchView(pending);
  setSettingsSidebarExpanded(settingsSidebarExpanded());
});
function setSettingsSidebarExpanded(expanded){
  const managing=document.body.classList.contains('management-mode');
  const open=managing&&expanded;
  const entering=managing&&!settingsSidebarWasManaged;
  const animateEntry=entering&&open&&!settingsRouteTransition?.preserveGeometry&&!settingsRouteMotion.matches;
  settingsSidebarWasManaged=managing;
  if(settingsSidebarEntryFrame)cancelAnimationFrame(settingsSidebarEntryFrame);
  settingsSidebarEntryFrame=0;
  document.body.classList.toggle('settings-sidebar-entering',animateEntry);
  document.body.classList.toggle('settings-sidebar-hidden',managing&&!open);
  document.body.classList.toggle('sidebar-open',open&&matchMedia('(max-width:1000px)').matches);
  const settingsEntry=$('#railSettingsEntry');
  if(settingsEntry){
    const label=managing?'返回首页':'设置';
    settingsEntry.setAttribute('aria-expanded',String(open));
    settingsEntry.setAttribute('aria-pressed',String(managing));
    settingsEntry.setAttribute('aria-label',label);
    settingsEntry.setAttribute('title',label);
    settingsEntry.dataset.tooltip=label;
  }
  const toggle=$('#settingsSidebarToggle');
  if(toggle){
    toggle.setAttribute('aria-expanded',String(open));
    toggle.setAttribute('aria-label',open?'收起设置导航':'展开设置导航');
    toggle.title=open?'收起设置导航':'展开设置导航';
  }
  const mobileToggle=$('.collab-rail-toggle');
  if(mobileToggle){
    mobileToggle.setAttribute('aria-expanded',String(open));
    mobileToggle.setAttribute('aria-label',open?'收起导航':'展开导航');
  }
  const sidebar=$('#settingsSidebar');
  if(!open&&sidebar?.contains(document.activeElement)){
    const focusTarget=managing&&mobileToggle?.getClientRects().length?mobileToggle:settingsEntry;
    focusTarget?.focus({preventScroll:true});
  }
  if(sidebar){
    sidebar.inert=managing&&!open;
    if(managing&&!open)sidebar.setAttribute('aria-hidden','true');
    else sidebar.removeAttribute('aria-hidden');
  }
  if(animateEntry){
    // Keep the mounted panel at its closed geometry for one layout before reveal.
    settingsSidebarEntryFrame=requestAnimationFrame(()=>{
      settingsSidebarEntryFrame=0;
      sidebar?.getBoundingClientRect();
      document.body.classList.remove('settings-sidebar-entering');
    });
  }
}
function platformRailExpanded(){return document.body.classList.contains('platform-rail-expanded')}
function setPlatformRailExpanded(expanded){
  expanded=!!expanded;
  document.body.classList.toggle('platform-rail-expanded',expanded);
  const button=$('#railSidebarToggle');
  if(!button)return;
  const label=expanded?'只显示图标':'展开导航';
  button.disabled=false;
  button.removeAttribute('aria-disabled');
  button.removeAttribute('title');
  button.setAttribute('aria-controls','platformRail');
  button.setAttribute('aria-expanded',String(expanded));
  button.setAttribute('aria-label',label);
  if(expanded)button.removeAttribute('data-tooltip');
  else button.dataset.tooltip=label;
  const text=button.querySelector('.rail-label');
  if(text)text.textContent=label;
}
function viewTitles(){return {management:'设置',collaboration:'AI 工作台',issues:'需求列表',aiSchedules:'定时任务',bots:'通道管理',runs:'运行记录',approvals:'待我确认',sops:'流程模板',flow:'流程设计',agents:'角色定义',mcp:'能力资产',rules:'能力资产',skills:'能力资产',dockerfiles:'环境资产',environments:'环境资产',sandboxes:'环境资产',observability:'统计',projects:'项目配置',platform:'平台管理',runners:'执行机器',settings:'个人配置',access:'访问密钥',guide:'访问密钥'}}
// 设置侧栏只列一级入口；被合并进同一页的子视图（能力资产、环境资产的类型 Tab，
// 从流程模板打开的流程设计）按 data-nav-match 高亮它们所属的入口。
function settingsNavMatches(btn,name){return btn.dataset.view===name||String(btn.dataset.navMatch||'').split(/\s+/).includes(name)}
const assetTypeGroups={capability:['mcp','rules','skills'],environment:['dockerfiles','environments','sandboxes']};
function assetTypeGroup(name){return Object.keys(assetTypeGroups).find(key=>assetTypeGroups[key].includes(name))||''}
function syncAssetTypeTabs(name){
  const group=assetTypeGroup(name);
  $$('[data-asset-type-group]').forEach(bar=>{if(bar.closest('#assetsView'))bar.hidden=bar.dataset.assetTypeGroup!==group});
  $$('[data-asset-type-tab]').forEach(btn=>{const on=btn.dataset.assetTypeTab===name;btn.classList.toggle('active',on);btn.setAttribute('aria-selected',String(on));btn.tabIndex=on?0:-1});
}
function activeViewName(){if(state.currentView)return state.currentView;const el=document.querySelector('.view.active');return el?el.id.replace(/View$/,''):''}
function restoredView(){
  const titles=viewTitles();
  const hash=(location.hash||'').replace(/^#\/?/,'').split(/[/?]/)[0];
  if(hash==='access')return 'guide';
  if(hash==='assets')return 'dockerfiles';
  if(hash==='playbooks')return 'sops';
  if(hash==='management')return 'settings';
  if(titles[hash])return hash;
  const saved=localStorage.getItem('agentflow.activeView');
  if(saved==='playbooks')return 'sops';
  if(saved==='management')return 'settings';
  if(titles[saved]&&saved!=='dashboard')return saved;
  return 'collaboration';
}
function routePart(index){
  const raw=(location.hash||'').replace(/^#\/?/,'').split('?')[0];
  const parts=raw.split('/').filter(Boolean);
  const part=parts[index]||'';
  try{return decodeURIComponent(part)}catch{return part}
}
function runRouteId(){
  return routePart(0)==='runs'?routePart(1):'';
}
function flowRouteId(){
  return routePart(0)==='flow'?routePart(1):'';
}
function agentRouteId(){return routePart(0)==='agents'?routePart(1):''}
function assetRouteId(){return isProjectSharedAsset(routePart(0))?routePart(1):''}
function assetViewHash(type,id){if(!id)return `#/${type}`;return `#/${type}/${id==='__new__'?'new':encodeURIComponent(id)}`}
// Syncs the open asset detail with #/<type>/<id>; a no-op until the catalog is loaded.
function applyAssetRoute(){
  const type=routePart(0);
  if(!isProjectSharedAsset(type)||state.assetType!==type||!state.data?.[type])return;
  const id=assetRouteId();
  if(!id){if(state.selectedAsset){resetAssetDetailState();renderAssets()}return}
  const hit=assetFindVisible(id);
  if(!hit&&id==='new'){
    if(state.selectedAsset==='__new__')return;
    state[assetScopeKey(type)]='project';
    resetAssetDetailState();state.selectedAsset='__new__';state.skillUploadStatus='';
    renderAssets();scrollAssetTop();return;
  }
  if(!hit){
    toast('资产不存在或已删除');
    history.replaceState(null,'',assetViewHash(type));
    resetAssetDetailState();renderAssets();return;
  }
  if(state.selectedAsset===id)return;
  state[assetScopeKey(type)]=hit.scope;
  resetAssetDetailState();state.selectedAsset=id;state.skillUploadStatus='';
  renderAssets();scrollAssetTop();
}
async function applyAgentRoute(){
  const id=agentRouteId();
  if(!id)return;
  try{
    const agent=await api(`/api/agents/${encodeURIComponent(id)}`);
    if(agentRouteId()!==id||activeViewName()!=='agents')return;
    const index=(state.data.agents||[]).findIndex(a=>a.id===id);
    if(index>=0)state.data.agents[index]=agent;else state.data.agents.push(agent);
    openAgentWizard(agent);
  }catch(error){toast(error.message,'err')}
}
// Only SOPs visible in the active project can be restored from the URL or localStorage.
function openableWorkflow(id){return id?catalogWorkflows().find(w=>w.id===id)||null:null}
function savedWorkflowId(){
  const id=state.workflow?.id||'';
  return id&&(state.data?.workflows||[]).some(w=>w.id===id)?id:'';
}
function flowViewHash(){const id=savedWorkflowId();return id?`#/flow/${encodeURIComponent(id)}`:'#/flow'}
function restoreOpenWorkflow(){
  return openableWorkflow(flowRouteId())||openableWorkflow(localStorage.getItem('agentflow.activeWorkflow')||'')||preferredWorkflow();
}
function rememberOpenWorkflow(){
  const id=savedWorkflowId();
  if(id)localStorage.setItem('agentflow.activeWorkflow',id);
  if((state.currentView||activeViewName())!=='flow')return;
  const next=flowViewHash();
  if(location.hash!==next)history.replaceState(null,'',next);
}
async function applyFlowRoute(){
  const id=flowRouteId();
  if(!id||state.workflow?.id===id)return;
  const wf=openableWorkflow(id);
  if(!wf){rememberOpenWorkflow();return}
  if(state.workflowDirty&&!(await afConfirm({title:'未保存更改',message:'当前流程有未保存修改，切换将放弃这些修改。继续？',danger:true}))){rememberOpenWorkflow();return}
  if(flowRouteId()!==id)return;
  state.workflow=clone(wf);
  state.workflowSaved=clone(wf);
  state.workflowDirty=false;
  state.selectedNode=null;
  localStorage.setItem('agentflow.activeWorkflow',id);
  renderWorkflowMeta();renderCanvas();renderProperties();
  refreshGenericWorkflowRoles();
}
function runViewHash(id){return id?`#/runs/${encodeURIComponent(id)}`:'#/runs'}
function syncRunHash(id){
  if((state.currentView||activeViewName())!=='runs')return;
  const next=runViewHash(id||'');
  if(location.hash!==next)history.replaceState(null,'',next);
}
function persistView(name,push=false){
  localStorage.setItem('agentflow.activeView',name);
  let next=`#/${name}`;
  if(name==='runs'){
    const linked=runRouteId();
    if(linked)next=runViewHash(linked);
  }
  if(name==='flow')next=flowViewHash();
  if(name==='agents'&&agentRouteId())next=`#/agents/${encodeURIComponent(agentRouteId())}`;
  if(isProjectSharedAsset(name)&&routePart(0)===name&&assetRouteId())next=`#/${name}/${encodeURIComponent(assetRouteId())}`;
  if(name==='projects')next=projectAdminHash(state.projectAdminDetailId||'',state.projectAdminTab);
  if(name==='collaboration'){
    const current=(location.hash||'').replace(/^#\/?/,'').split('?')[0].split('/').filter(Boolean);
    let fromHash=current[0]==='collaboration'?current[1]||'':'';
    try{fromHash=decodeURIComponent(fromHash)}catch{}
    const id=new URL(location.href).searchParams.get('chatSession')||fromHash;
    if(id)next='#/collaboration/'+encodeURIComponent(id);
  }
  if(location.hash===next)return;
  // push=true 时压入新历史记录，让浏览器「后退」能在平台页面间回退；
  // 否则原地替换，避免 boot 时把首个视图压栈。
  if(push)history.pushState(null,'',next);
  else history.replaceState(null,'',next);
}
function switchView(name){
  $$('details.collab-settings').forEach(menu=>menu.open=false);
  const titles=viewTitles();
  if(name==='dashboard')name='collaboration';
  if(name==='access')name='guide';
  if(name==='assets')name='dockerfiles';
  if(name==='playbooks')name='sops';
  if(name==='management')name='settings';
  const assetViews=['rules','skills','dockerfiles','environments','sandboxes'];
  const targetView=assetViews.includes(name)?'assets':name;
  if(!titles[name]||!$(`#${targetView}View`)){name='collaboration'}
  // 手敲 #/platform 或者从别的账号留下的 localStorage 恢复进来，都要挡住。
  if(name==='platform'&&!isPlatformAdmin())name='collaboration';
  const previous=activeViewName();
  if(!settingsRouteApplying){
    const crossingSettings=previous!==name&&((previous==='collaboration'&&isSettingsManagementView(name))||(isSettingsManagementView(previous)&&name==='collaboration'));
    if(crossingSettings&&!settingsRouteMotion.matches){startSettingsRouteTransition(name);return}
    cancelSettingsRouteTransition();
  }
  const target=assetViews.includes(name)?'assets':name;
  const staying=activeViewName()===name;
  const prevView=state.currentView;
  if(previous==='observability'&&name!=='observability'&&typeof obsDisposeCharts==='function'){obsCollapse?.({silent:true});obsDisposeCharts();obsDisposeSelects?.()}
  state.currentView=name;
  if(name!=='runs')stopQueuedRunRefresh();
  $$('.nav[data-view]').forEach(n=>n.classList.toggle('active',settingsNavMatches(n,name)));
  syncAssetTypeTabs(name);
  $$('.view').forEach(v=>v.classList.remove('active'));
  $(`#${target}View`).classList.add('active');
  $('#pageTitle').textContent=titles[name]||name;
  // Views that render their own H1 hide the topbar title; #pageTitle keeps its text for tests and assistive tools.
  document.body.classList.toggle('view-has-heading',!!$(`#${target}View`).querySelector('.page-heading,.page-header,.kb-head,.pa-ph'));
  const isPureManagement = isSettingsManagementView(name);
  document.body.classList.toggle('management-mode', isPureManagement);
  // At ≤1000px the settings navigation is an overlay drawer: it opens when the
  // user comes into settings from another page, and closes once a settings
  // page is picked (or deep-linked) so the page itself is visible.
  const narrowSettings=isPureManagement&&matchMedia('(max-width:1000px)').matches;
  setSettingsSidebarExpanded(!narrowSettings?isPureManagement:staying?settingsSidebarExpanded():!!prevView&&!isSettingsManagementView(previous));
  $('#managementEntry')?.classList.toggle('active', isPureManagement);
  $('#railSettingsEntry')?.classList.toggle('active', isPureManagement);
  const noSidebarViews = ['issues', 'approvals', 'observability'];
  document.body.classList.toggle('no-sidebar', noSidebarViews.includes(name));
  document.body.classList.toggle('chat-next-active', name==='collaboration');
  if(name==='collaboration'||isPureManagement)mountNativeChat();
  showDirectRunButton(name==='flow');
  closeIssueDrawer();
  if(name!=='sops'&&typeof hideSOPCardMenu==='function')hideSOPCardMenu();
  // 仅真实用户切换时压栈：boot 首次进入（prevView 为空）与同视图重复调用
  //（staying）不压；hashchange 后退触发的调用 prevView 非空且 hash 已是目标值，
  // persistView 内部会短路，不会重复压栈。
  persistView(name,!staying&&!!prevView);
  const nav=$('.platform-sidebar nav');
  if(nav){
    if(['dashboard','issues','approvals'].includes(name)){
      nav.scrollTop=0;
    }else{
      const activeBtn=$$('.platform-sidebar .nav[data-view]').find(btn=>settingsNavMatches(btn,name));
      if(activeBtn){
        const navRect=nav.getBoundingClientRect(), btnRect=activeBtn.getBoundingClientRect();
        if(btnRect.top < navRect.top || btnRect.bottom > navRect.bottom){
          activeBtn.scrollIntoView({block:'nearest',behavior:'instant'});
        }
      }
    }
  }
  if(name==='runs'){
    const linkedRun=runRouteId();
    const hashAlreadyRuns=(location.hash||'').replace(/^#\/?/,'').split(/[/?]/)[0]==='runs';
    if(!staying){
      if(linkedRun&&(hashAlreadyRuns||!prevView))showRunDetail(linkedRun);
      else showRunList();
    }
    refreshRuns(false,true);
  }
  if(name==='issues'){refreshIssues();renderSync();const issueId=new URL(location.href).searchParams.get('issueId');if(issueId)openIssueDetail(issueId)}

  if(name==='aiSchedules'){renderScheduleJobs();refreshAISchedules()}
  if(name==='bots')loadBots();
  if(name==='sops'){if(!staying&&state.data){sopCatalogMotion.reset();renderSOPCatalog()}refreshWorkflowMetrics()}
  if(name==='dashboard')refreshDashboard()
  if(name==='approvals')renderApprovals(false)
  if(name==='observability')refreshObs()
  if(name==='agents'){api('/api/agents',{silent:true}).then(agents=>{state.data.agents=agents;renderAgents()}).catch(e=>toast(e.message));renderAgents();applyAgentRoute()}
  if(name==='settings')renderPersonalSettings();
  if(name==='platform')renderPlatformAdminPanel();
  if(name==='projects')renderProjectAdmin();
  if(assetViews.includes(name)){
    if(staying&&state.assetType===name){if(state.selectedAsset)closeAssetDetail({force:true});else renderAssets()}
    else{
      if(state.assetType!==name)state.assetQuery='';
      state.assetType=name;resetAssetDetailState();
      if(routePart(0)===name&&assetRouteId())applyAssetRoute();else renderAssets();
    }
  }
  if(name==='mcp')loadMCPMarket();
  if(name==='runners')refreshRunners();
  if(name==='guide')renderGuide();
  if(name==='flow'){if(!state.workflow?.nodes?.some(n=>n.id===state.selectedNode))state.selectedNode=null;renderWorkflowMeta();renderCanvas();renderProperties();setFlowView('canvas');refreshGenericWorkflowRoles()}
}
function renderAll(){renderUserBadge();renderPlatformNav();renderObsTabs();renderProjectSwitcher();renderIssues();renderSOPCatalog();renderWorkflowMeta();renderCanvas();renderProperties();renderAgents();renderAssets();renderMCPManagement();renderRuns();renderSync();renderProjectAdmin();renderDashboard();renderGuide();updateApprovalBadge();showDirectRunButton($('#flowView').classList.contains('active'))}
