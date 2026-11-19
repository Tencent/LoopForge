// Both the SOP editor and the conversation composer use the same selection UI.
window.editKnowledgeRefs = async function editKnowledgeRefs(project, refs, save) {
  const dialog = document.createElement('dialog');
  dialog.className = 'knowledge-ref-dialog';
  const title = document.createElement('h3');
  title.textContent = '关联知识库';
  const status = document.createElement('p');
  status.setAttribute('role','status');
  const list = document.createElement('div');
  const footer = document.createElement('div');
  footer.className = 'knowledge-ref-footer';
  const cancel = document.createElement('button');
  cancel.type = 'button';
  cancel.className = 'button ghost';
  cancel.textContent = '取消';
  cancel.onclick = () => dialog.close();
  const apply = document.createElement('button');
  apply.type = 'button';
  apply.className = 'button primary';
  apply.textContent = '保存';
  apply.disabled = true;
  footer.append(cancel,apply);
  dialog.append(title,status,list,footer);
  dialog.onclose = () => dialog.remove();
  document.body.append(dialog);
  dialog.showModal();
  const inputs = [];
  const load = async path => {
    const response = await fetch(path+(path.includes('?')?'&':'?')+'projectId='+encodeURIComponent(project));
    if (!response.ok) throw new Error('知识库列表加载失败');
    return response.json();
  };
  try {
    status.textContent = '正在加载服务…';
    const options = await load('/api/knowledge/reference-options');
    for (const ref of refs) {
      if (!options.some(s=>s.id===ref.mcpServerId)) options.push({id:ref.mcpServerId,name:ref.mcpServerId+'（已失效）',missing:true});
    }
    for (const s of options) {
      const row=document.createElement('fieldset');
      const label=document.createElement('label');
      const enabled=document.createElement('input');
      enabled.type='checkbox';
      const previous=refs.find(r=>r.mcpServerId===s.id);
      enabled.checked=!!previous;
      label.append(enabled,document.createTextNode(s.name));
      row.append(label);
      const sources=document.createElement('input');
      sources.placeholder='来源 ID，以逗号分隔';
      sources.setAttribute('aria-label',s.name+'允许的来源 ID');
      sources.value=(previous?.sources||[]).join(', ');
      const mode=document.createElement('select');
      mode.add(new Option('当前身份可读的全部来源','all'));
      mode.add(new Option('仅指定来源','allowlist'));
      mode.value=previous?.sources?.length?'allowlist':'all';
      const choices=document.createElement('div');
      let loaded=false;
      const update=async()=>{
        mode.disabled=!enabled.checked;
        sources.hidden=choices.hidden=!enabled.checked||mode.value!=='allowlist';
        if(!sources.hidden && !loaded && !s.missing){
          loaded=true;
          try {
            const available=await load('/api/knowledge/reference-options?sources=true');
            for(const source of available){
              const pick=document.createElement('button');
              pick.type='button';
              pick.textContent=source.name||source.id;
              pick.title=source.id;
              pick.onclick=()=>{
                const ids=new Set(sources.value.split(/[,，]/).map(x=>x.trim()).filter(Boolean));
                if(ids.has(source.id))ids.delete(source.id);else ids.add(source.id);
                sources.value=[...ids].join(', ');
              };
              choices.append(pick);
            }
          }catch(e){loaded=false;choices.textContent=e.message}
        }
      };
      enabled.onchange=mode.onchange=update;
      if(s.scoped||previous?.sources?.length) row.append(mode,sources,choices);
      update();
      list.append(row);
      inputs.push(()=> {
        if(!enabled.checked)return null;
        const selected=mode.value==='allowlist'?[...new Set(sources.value.split(/[,，]/).map(x=>x.trim()).filter(Boolean))]:[];
        if(s.missing)throw new Error('请移除已失效服务：'+s.name);
        if(mode.value==='allowlist'&&!selected.length)throw new Error('请填写允许的来源 ID');
        return {mcpServerId:s.id,mode:mode.value,sources:selected};
      });
    }
    status.textContent=options.length?'外部 HTTP MCP 使用当前身份的范围，仅提供服务声明为只读的工具。':'当前项目没有可用的知识服务。';
    apply.disabled=false;
    apply.onclick=async()=>{
      apply.disabled=true;
      try{
        const refs=inputs.map(read=>read()).filter(Boolean);
        const response=await fetch('/api/knowledge/reference-check?projectId='+encodeURIComponent(project),{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({knowledgeRefs:refs})});
        if(!response.ok)throw new Error((await response.json()).error||'引用校验失败');
        await save(refs);
        dialog.close();
      }
      catch(e){status.textContent=e.message;apply.disabled=false}
    };
  } catch(e){status.textContent=e.message}
};
