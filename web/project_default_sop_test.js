"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const source = fs.readFileSync(process.argv[2], "utf8");
const requests = [];
const notices = [];
const project = {id:"pay",key:"PAY",name:"Payments",workflowId:"deleted-sop",members:[],ownerId:1};
const ctx = {
  state:{projectFilter:"pay",projectDraft:null,data:{projects:[project],repositories:[],workflows:[
    {id:"new-sop",name:"New SOP",projectId:"pay"},
    {id:"foreign",name:"Foreign",projectId:"other"},
    {id:"generic-backend-delivery",name:"Backend",generic:true},
  ]}},
  escapeHTML:s=>String(s).replace(/[&<>"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;"}[c])),
  clone:v=>JSON.parse(JSON.stringify(v)),captureRepoRows:()=>{},
  toast:s=>notices.push(s),renderAll:()=>{},localStorage:{setItem:()=>{}},
  api:async(url,init)=>{const body=JSON.parse(init.body);requests.push({url,body});return body;},
};
vm.createContext(ctx);
for(const name of ["projectDefaultSOPField","applyProjectDefaultSOP","buildProjectPayload","validateProjectPayload","storeSavedProject","saveProjectAdmin"]){
  const start=source.search(new RegExp("^(?:async )?function "+name+"\\(","m"));
  assert.ok(start>=0,name);
  const rest=source.slice(start),next=/\n(?:async )?function \w+\(/.exec(rest);
  vm.runInContext(next?rest.slice(0,next.index):rest,ctx);
}
const html=ctx.projectDefaultSOPField(project);
assert.match(html,/select name="workflowId"/);
assert.match(html,/已失效：deleted-sop/);
assert.match(html,/value="new-sop"/);
assert.match(html,/value="generic-backend-delivery"/);
assert.ok(!html.includes('value="foreign"'));
assert.match(ctx.projectDefaultSOPField({}),/value="generic-backend-delivery" selected/);

for(const p of [
  {sopBindings:[{workflowId:"old",order:1},{workflowId:"next",order:2}]},
  {sopPlaybooks:[{default:true,bindings:[{workflowId:"old",order:1},{workflowId:"next",order:2}]}]},
]){
  ctx.applyProjectDefaultSOP(p,"new-sop");
  const bindings=p.sopBindings||p.sopPlaybooks[0].bindings;
  assert.equal(p.workflowId,"new-sop");
  assert.equal(bindings[0].workflowId,"new-sop");
  assert.equal(bindings[1].workflowId,"next");
}

(async()=>{
  const fields={id:"pay",key:"PAY",name:"Payments",description:"",color:"#536dfe",ownerId:"1",defaultRepositoryId:"",workflowId:"new-sop"};
  const form={elements:Object.fromEntries(Object.entries(fields).map(([k,value])=>[k,{value}]))};
  await ctx.saveProjectAdmin({preventDefault(){},currentTarget:form});
  assert.equal(requests.length,1,notices.join("; "));
  assert.equal(requests[0].body.workflowId,"new-sop","saving must replace the old project default");
  assert.equal(ctx.state.data.projects[0].workflowId,"new-sop");
  console.log("project default SOP passed");
})().catch(err=>{console.error(err);process.exitCode=1;});
