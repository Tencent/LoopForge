// Read-only browsing of live workspace files, separate from immutable artifacts.
async function previewRunFiles(run){
  const host=$('#artifactPreview'),modal=$('#artifactModal');
  if(!run||!host||!modal)return;
  host.innerHTML=`<section class="run-file-browser"><header class="artifact-preview-head"><div class="artifact-preview-titles"><h2>工作区文件</h2><div class="artifact-preview-sub">只读预览 · 当前工作区内容</div></div></header><div class="run-file-layout"><aside class="run-file-sidebar" aria-label="工作区文件"><div class="run-file-navigation"><button type="button" class="button ghost small" data-run-file-up disabled>上一级</button><span data-run-file-directory>工作区</span></div><label class="run-file-search">搜索文件<input type="search" data-run-file-search placeholder="文件名或路径"></label><div class="run-file-list" data-run-file-list aria-live="polite">正在读取文件列表…</div></aside><div class="run-file-document" data-run-file-document aria-live="polite"><div class="empty-properties"><b>选择文件查看</b><p>Markdown 默认渲染，可切换源码；文件不会被修改。</p></div></div></div></section>`;
  const root=host.firstElementChild,list=root.querySelector('[data-run-file-list]'),documentPane=root.querySelector('[data-run-file-document]');
  const search=root.querySelector('[data-run-file-search]'),up=root.querySelector('[data-run-file-up]');
  const isOpen=()=>host.contains(root)&&modal.classList.contains('open');
  let entries=[],directory='',selected='',fileRequest=0,truncated=false;
  modal.classList.add('open');
  search.focus();
  function renderFiles(){
    const query=search.value.trim().toLowerCase();
    const matches=entries.filter(entry=>query?entry.type==='file'&&entry.path.toLowerCase().includes(query):entry.path.slice(0,entry.path.lastIndexOf('/')+1)===directory);
    matches.sort((a,b)=>(a.type==='directory'?0:1)-(b.type==='directory'?0:1)||a.path.localeCompare(b.path));
    up.disabled=!directory;
    root.querySelector('[data-run-file-directory]').textContent=directory||'工作区';
    list.innerHTML=matches.slice(0,200).map(entry=>`<button type="button" class="run-file-entry" data-run-file-path="${escapeHTML(entry.path)}" data-run-file-type="${escapeHTML(entry.type)}"${entry.path===selected?' aria-current="true"':''} title="${escapeHTML(entry.path)}"><span class="run-file-kind">${entry.type==='directory'?'目录':'文件'}</span><span>${escapeHTML(query?entry.path:entry.name||entry.path)}</span></button>`).join('')||'<p class="run-file-notice">没有匹配的文件</p>';
    if(matches.length>200||truncated)list.insertAdjacentHTML('beforeend',`<p class="run-file-notice">${matches.length>200?'仅显示前 200 项，请搜索缩小范围。':''}${truncated?'文件列表已达到服务端上限；可在 IDE 中查看其余文件。':''}</p>`);
    list.querySelectorAll('[data-run-file-path]').forEach(button=>button.onclick=()=>{
      if(button.dataset.runFileType==='directory'){directory=button.dataset.runFilePath+'/';search.value='';renderFiles()}
      else openFile(button.dataset.runFilePath);
    });
  }
  async function openFile(path){
    selected=path;
    const request=++fileRequest;
    renderFiles();
    documentPane.innerHTML='<div class="artifact-loading" role="status">正在读取文件…</div>';
    try{
      const data=await api(`/api/runs/${encodeURIComponent(run.id)}/workspace/file?path=${encodeURIComponent(path)}`,{silent:true});
      if(!isOpen()||request!==fileRequest)return;
      const content=String(data.content??''),markdown=/\.(md|markdown|mdown|mkd)$/i.test(path);
      let mode=markdown?'preview':'source';
      function renderDocument(){
        documentPane.innerHTML=`<div class="run-file-toolbar"><b title="${escapeHTML(path)}">${escapeHTML(path)}</b>${markdown?`<div class="artifact-view-mode"><button type="button" class="artifact-mode-btn${mode==='preview'?' active':''}" data-run-file-mode="preview">预览</button><button type="button" class="artifact-mode-btn${mode==='source'?' active':''}" data-run-file-mode="source">源码</button></div>`:''}</div><div class="artifact-body" tabindex="0" aria-label="文件内容">${mode==='preview'?`<div class="markdown-body">${renderMarkdown(content)}</div>`:`<pre class="artifact-content">${escapeHTML(content)}</pre>`}</div>`;
        documentPane.querySelectorAll('[data-run-file-mode]').forEach(button=>button.onclick=()=>{mode=button.dataset.runFileMode;renderDocument();documentPane.querySelector(`[data-run-file-mode="${mode}"]`)?.focus()});
        documentPane.querySelectorAll('.md-code-copy').forEach(button=>button.onclick=()=>copyText(button.closest('.md-code-block')?.querySelector('pre code')?.textContent||'','代码片段已复制'));
      }
      renderDocument();
    }catch(error){
      if(isOpen()&&request===fileRequest)documentPane.innerHTML=`<div class="empty-properties"><b>文件无法预览</b><p>${escapeHTML(error.message)}</p><small>支持服务端允许的文本文件；二进制文件或超出大小限制的文件请在 IDE 中查看。</small></div>`;
    }
  }
  search.oninput=renderFiles;
  up.onclick=()=>{directory=directory.replace(/[^/]+\/$/,'');search.value='';renderFiles()};
  try{
    const data=await api(`/api/runs/${encodeURIComponent(run.id)}/workspace/tree`,{silent:true});
    if(!isOpen())return;
    entries=(data.entries||[]).filter(entry=>entry&&typeof entry.path==='string'&&['file','directory'].includes(entry.type));
    truncated=!!data.truncated;
    renderFiles();
  }catch(error){
    if(!isOpen())return;
    list.textContent='文件列表不可用';
    documentPane.innerHTML=`<div class="empty-properties"><b>工作区暂不可浏览</b><p>${escapeHTML(error.message)}</p><small>工作区已回收时，请关闭预览并在运行详情的「工作区」中按需恢复。已保存的交付物仍可单独查看。</small></div>`;
  }
}
