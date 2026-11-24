'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const source=fs.readFileSync(process.argv[2],'utf8');
const elements={
 '#reqAttachments':{hidden:true,innerHTML:'',querySelectorAll:()=>[]},
 '#reqAttach':{disabled:false},
};
const state={currentUser:{id:42},reqAttachments:[]};
const requests=[];const notices=[];
const ctx={state,FormData,console,$:id=>elements[id],currentProject:()=>({id:'p'}),escapeHTML:s=>String(s).replaceAll('<','&lt;'),window:{},toast:s=>notices.push(s),fetch:async(url,options)=>{requests.push({url,options});return {ok:true,json:async()=>({id:'a',name:'spec.zip',size:30})}}};
vm.createContext(ctx);
ctx.fmtBytes=n=>n+' B';
for(const name of ['renderReqAttachments','uploadReqAttachments','requirementComposerBusy']){
 const start=source.search(new RegExp('^(?:async )?function '+name+'\\(','m'));
 assert.ok(start>=0,name);
 const next=source.slice(start+1).search(/\n(?:async )?function \w+\(/);
 vm.runInContext(next<0?source.slice(start):source.slice(start,start+1+next),ctx);
}
ctx.reqIcon=()=>'';ctx.hydrateReqIcons=()=>{};ctx.renderReqFooter=()=>{};ctx.persistRequirementComposer=()=>{};
(async()=>{
 const upload=ctx.uploadReqAttachments([new File(['zip'],'spec.zip')]);
 assert.equal(state.reqUploading,true);assert.equal(elements['#reqAttach'].disabled,true);
 await upload;
 assert.equal(requests[0].url,'/api/assistant/attachments');assert.equal(requests[0].options.body.get('projectId'),'p');
 assert.equal(state.reqAttachments[0].name,'spec.zip');assert.equal(state.reqUploading,false);
 assert.ok(elements['#reqAttachments'].innerHTML.includes('data-remove-req-attachment="a"'));
 const count=requests.length;await ctx.uploadReqAttachments([{name:'empty',size:0}]);assert.equal(requests.length,count);
 state.reqCreating='issue';await ctx.uploadReqAttachments([new File(['a'],'a.md')]);assert.equal(requests.length,count);
 state.reqCreating='';state.reqAttachments=Array(8).fill({id:'a',name:'x',size:1});await ctx.uploadReqAttachments([new File(['a'],'a.md')]);assert.equal(requests.length,count);
 ctx.fetch=async()=>({ok:false,json:async()=>({error:'Upload failed'})});state.reqAttachments=[{id:'previous',name:'keep.md',size:3}];
 await ctx.uploadReqAttachments([new File(['a'],'a.md')]);assert.equal(state.reqAttachments[0].id,'previous');assert.equal(state.reqUploading,false);assert.equal(notices.at(-1),'Upload failed');
})().catch(err=>{console.error(err);process.exitCode=1});
