// AI 助手设置：没有 @ 时是否更主动地派角色、走 SOP（默认都关），以及每轮都带上的自定义规则。只对当前用户生效。
const ASSISTANT_RULES_MAX=4000;
const assistantPrefsUI={saved:null,loading:false,saving:false,error:''};

function assistantPrefsSignedIn(){return !!state.currentUser&&!state.currentUser.isGuest}
function assistantPrefsForm(){return document.querySelector('#assistantPrefsForm')}
function assistantPrefsDraft(form){return {proactiveRoles:form.proactiveRoles.checked,proactiveSops:form.proactiveSops.checked,instructions:form.instructions.value.trim()}}
function assistantPrefsDirty(form){
 const saved=assistantPrefsUI.saved;if(!saved||!form)return false;
 const draft=assistantPrefsDraft(form);
 return draft.proactiveRoles!==!!saved.proactiveRoles||draft.proactiveSops!==!!saved.proactiveSops||draft.instructions!==(saved.instructions||'');
}
function assistantPrefsSwitchRow(name,title,help,on){
 return `<label class="asst-switch-row"><span><b>${title}</b><small>${help}</small></span><input type="checkbox" role="switch" name="${name}" ${on?'checked':''}></label>`;
}
function assistantPrefsFormHTML(p){
 const rules=p.instructions||'';
 return `<form id="assistantPrefsForm" class="asst-form" novalidate><fieldset ${assistantPrefsUI.saving?'disabled':''}>
  <section class="asst-panel"><header class="asst-panel-head"><h2>主动派发</h2><p>打开后 AI 助手会更主动地把事情交给角色或 SOP。派角色不需要确认；走 SOP 仍按审批模式先征求同意，完全权限下直接启动。</p></header>
   <div class="asst-rows">
    ${assistantPrefsSwitchRow('proactiveRoles','主动派角色','整件事是某个角色的本职（如独立评审、整套测试），或改了高风险代码需要复核时，直接交给这个角色。',p.proactiveRoles)}
    ${assistantPrefsSwitchRow('proactiveSops','主动走 SOP','遇到要落到仓库的新需求时，先交给项目里用途对得上的 SOP，而不是自己直接改代码。',p.proactiveSops)}
   </div></section>
  <section class="asst-panel"><header class="asst-panel-head"><h2>自定义规则</h2><p>每轮对话都会带上这些规则，适合写你的习惯和偏好；不会越过审批模式和平台规则。</p></header>
   <label class="asst-rules-label" for="assistantPrefsRules">规则</label>
   <textarea id="assistantPrefsRules" class="input asst-rules" name="instructions" rows="8" maxlength="${ASSISTANT_RULES_MAX}" placeholder="例如：回复先给结论再给细节；改代码前先说明要改哪些文件；不要自动发起 MR。">${escapeHTML(rules)}</textarea>
   <div class="asst-rules-foot"><span id="assistantPrefsCount" class="asst-count">${rules.length} / ${ASSISTANT_RULES_MAX}</span></div></section>
  <div class="asst-actions"><button type="submit" class="button primary" id="assistantPrefsSave" disabled>${assistantPrefsUI.saving?'保存中…':'保存'}</button></div>
 </fieldset></form>`;
}
function renderAssistantSettings(){
 const host=document.querySelector('#assistantContent');if(!host)return;
 if(!assistantPrefsSignedIn()){host.innerHTML='<section class="asst-panel asst-empty"><p>登录后可以设置 AI 助手。</p></section>';return}
 if(assistantPrefsUI.error&&!assistantPrefsUI.saved){host.innerHTML=`<section class="asst-panel asst-empty"><p>${escapeHTML(assistantPrefsUI.error)}</p><button type="button" class="button" data-assistant-prefs-retry>重试</button></section>`;host.querySelector('[data-assistant-prefs-retry]').onclick=loadAssistantSettings;return}
 if(!assistantPrefsUI.saved){host.innerHTML='<section class="asst-panel asst-empty"><p>正在读取设置…</p></section>';return}
 host.innerHTML=assistantPrefsFormHTML(assistantPrefsUI.saved);
 const form=assistantPrefsForm(),save=form.querySelector('#assistantPrefsSave'),count=form.querySelector('#assistantPrefsCount');
 const refresh=()=>{count.textContent=`${form.instructions.value.length} / ${ASSISTANT_RULES_MAX}`;save.disabled=assistantPrefsUI.saving||!assistantPrefsDirty(form)};
 form.addEventListener('input',refresh);
 form.addEventListener('change',refresh);
 form.onsubmit=async event=>{
  event.preventDefault();
  if(assistantPrefsUI.saving||!assistantPrefsDirty(form))return;
  const draft=assistantPrefsDraft(form);
  assistantPrefsUI.saving=true;save.disabled=true;save.textContent='保存中…';
  try{
   assistantPrefsUI.saved=await api('/api/assistant/preferences',{method:'PUT',body:JSON.stringify(draft)});
   toast('AI 助手设置已保存');
  }catch(err){toast(err.message||'保存失败，请重试')}
  finally{assistantPrefsUI.saving=false;save.textContent='保存';refresh()}
 };
}
async function loadAssistantSettings(){
 if(!assistantPrefsSignedIn()){renderAssistantSettings();return}
 if(assistantPrefsUI.loading)return;
 const form=assistantPrefsForm();
 if(form&&assistantPrefsDirty(form))return;
 assistantPrefsUI.loading=true;assistantPrefsUI.error='';
 if(!assistantPrefsUI.saved)renderAssistantSettings();
 try{assistantPrefsUI.saved=await api('/api/assistant/preferences',{silent:true})}
 catch(err){assistantPrefsUI.error=err.message||'读取设置失败'}
 finally{assistantPrefsUI.loading=false;renderAssistantSettings()}
}
