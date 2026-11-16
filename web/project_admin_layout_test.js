"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const [projectsPath, drawerPath, issuesPath] = process.argv.slice(2);

const same=(a,b,m)=>assert.deepEqual(JSON.parse(JSON.stringify(a)),b,m);

function extract(source, name) {
  const start = source.search(new RegExp("^(?:async )?function " + name + "\\(", "m"));
  assert.ok(start >= 0, name);
  const rest = source.slice(start), next = /\n(?:async )?function \w+\(|\n\/\//.exec(rest.slice(1));
  return next ? rest.slice(0, next.index + 1) : rest;
}

const requests = [], notices = [], failures = [];
const users = [{id:1,loginName:"alice",chineseName:"爱丽丝"},{id:2,loginName:"bob"},{id:3,loginName:"carol"},{id:4,loginName:"dave"},{id:5,loginName:"erin"}];
const projects = [
  {id:"agentflow",key:"AF",name:"AgentFlow",ownerId:1,members:[{userId:2},{userId:3},{userId:4},{userId:5}],workflowId:"wf",defaultRepositoryId:"r1",tapdWorkspaceId:"203",description:"平台本体"},
  {id:"web-console",key:"WC",name:"Web Console",ownerId:2,members:[],workflowId:"wf",defaultRepositoryId:""},
  {id:"lonely",key:"LO",name:"Lonely",ownerId:3,members:[{userId:1}],workflowId:"wf",defaultRepositoryId:""},
];
const repositories = [
  {id:"r1",projectId:"agentflow",name:"agent-flow",gongfengProjectId:"example/agent-flow",webUrl:"https://git.example.com/example/agent-flow",defaultBranch:"preview",integrateBranch:"agentflow/integration",updatedAt:new Date(Date.now()-3*86400e3).toISOString()},
  {id:"r2",projectId:"agentflow",name:"agent-test",gongfengProjectId:"example-maintainer/agent-test",defaultBranch:"master",integrateBranch:"",updatedAt:"0001-01-01T00:00:00Z"},
  {id:"r3",projectId:"web-console",name:"web",gongfengProjectId:"web/web",defaultBranch:"main"},
];
const issues = [{projectId:"agentflow",h:"active"},{projectId:"agentflow",h:"active"},{projectId:"agentflow",h:"done"}];

// Minimal element stubs for the drawer save path.
function el(props = {}) {
  const classes = new Set(props.classes || []);
  return Object.assign({
    hidden:false,disabled:false,textContent:"",value:"",style:{},
    classList:{add:c=>classes.add(c),remove:c=>classes.delete(c),contains:c=>classes.has(c),toggle:(c,on)=>{(on===undefined?!classes.has(c):on)?classes.add(c):classes.delete(c)}},
    setAttribute(){},removeAttribute(){},focus(){},querySelector:()=>null,querySelectorAll:()=>[],
  }, props);
}
const dom = {};
const ctx = {
  URL,
  console,
  state:{projectFilter:"agentflow",currentUser:{id:1},projectAdminDetailId:null,projectAdminTab:"basic",projectDraft:null,projectDraftFor:"",projectDraftBase:null,projectIdentSeed:"ab12",
    data:{projects,repositories,issues,users,workflows:[{id:"wf",name:"Backend",generic:true}],businessDomains:[]}},
  escapeHTML:s=>String(s??"").replace(/[&<>'"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;","'":"&#39;",'"':"&quot;"}[c])),
  safeColor:v=>/^#[0-9a-f]{3,8}$/i.test(String(v||""))?v:"#74829a",
  clone:v=>v==null?v:JSON.parse(JSON.stringify(v)),
  userById:id=>users.find(u=>u.id===Number(id)),
  userDisplay:u=>u.chineseName||u.loginName,
  userAvatarHTML:u=>`<i>${(u&&u.loginName||"?")[0]}</i>`,
  issueRunHealth:i=>i.h,
  knownTime:v=>{const d=v?new Date(v):null;return d&&d.getFullYear()>1970?d:null},
  relativeTime:()=>"3 天前",
  defaultRunEval:()=>({enabled:true}),
  isPlatformAdmin:()=>true,
  toast:(m,t)=>notices.push(m),
  renderAll:()=>{},
  localStorage:{setItem(){}},
  activeViewName:()=>"projects",
  location:{hash:""},
  history:{state:null,replaceState(s,t,h){ctx.location.hash=h},pushState(s,t,h){ctx.location.hash=h},back(){}},
  businessState:{report:null,seq:0},
  requestAnimationFrame:()=>{},
  setTimeout:()=>0,
  api:async(url,init)=>{const body=init&&init.body?JSON.parse(init.body):null;requests.push({url,method:init&&init.method,body});if(url==="/api/repositories")return {...body,id:body.id||"r9",updatedAt:new Date().toISOString()};return body},
  $:sel=>dom[sel]||null,
  $$:()=>[],
};
ctx.document={activeElement:null,contains:()=>false};
vm.createContext(ctx);
for (const name of ["parseGongfengRepoURL","repoDisplayPath","repoProviderLabel"]) vm.runInContext(extract(fs.readFileSync(issuesPath,"utf8"),name),ctx);
vm.runInContext(fs.readFileSync(projectsPath,"utf8"),ctx);
vm.runInContext(fs.readFileSync(drawerPath,"utf8"),ctx);
vm.runInContext("projectAdminFail=(form,name,message)=>{globalThis.__fail={name,message};return false}",ctx);

// Identity generation.
const gen=(name,list=[],seed="")=>ctx.generateProjectIdentity(name,list,seed);
same({...gen("Web Console")},{id:"web-console",key:"WC"});
same({...gen("AgentFlow")},{id:"agent-flow",key:"AF"});
same({...gen("WebConsole")},{id:"web-console",key:"WC"});
same({...gen("Portal")},{id:"portal",key:"PORTAL"});
same({...gen("Console")},{id:"console",key:"CON"});
same({...gen("Observability")},{id:"observability",key:"OBS"});
const taken=[{id:"agent-flow",key:"AF"},{id:"agent-flow-2",key:"AFA"}];
same({...gen("AgentFlow",taken)},{id:"agent-flow-3",key:"AFB"});
same({...gen("Web Console",projects)},{id:"web-console-2",key:"WCA"},"dedupe against existing projects");
same({...gen("域名控制台",[],"x7k2")},{id:"proj-x7k2",key:"PRJ"});
same({...gen("纯中文",[{id:"proj-x7k2",key:"PRJ"}],"x7k2")},{id:"proj-x7k2-2",key:"PRJA"});
assert.match(gen("中文").id,/^proj-[a-z0-9]{1,4}$/);
assert.equal(gen("new").id,"new-2","new is reserved for #/projects/new");
for(const name of ["Alpha Beta Gamma Delta Epsilon Zeta Eta","v2 API Gateway","E2E 测试"]){const {key}=gen(name);assert.match(key,/^[A-Z]{1,6}$/,name)}

// List filtering.
const ids=list=>list.map(p=>p.id);
same(ids(ctx.filterAdminProjects(projects,repositories,"","all",1)),["agentflow","web-console","lonely"]);
same(ids(ctx.filterAdminProjects(projects,repositories,"","setup",1)),["lonely"]);
same(ids(ctx.filterAdminProjects(projects,repositories,"","mine",1)),["agentflow","lonely"]);
same(ids(ctx.filterAdminProjects(projects,repositories,"example-maintainer/agent","all",1)),["agentflow"]);
same(ids(ctx.filterAdminProjects(projects,repositories,"wc","all",1)),["web-console"]);
same(ids(ctx.filterAdminProjects(projects,repositories,"nothing","all",1)),[]);

// Routes.
same({...ctx.parseProjectAdminRoute("#/projects/agentflow/repos")},{id:"agentflow",tab:"repos"});
same({...ctx.parseProjectAdminRoute("#/projects/agentflow/bogus")},{id:"agentflow",tab:"basic"});
same({...ctx.parseProjectAdminRoute("#/projects/new/repos")},{id:"new",tab:"basic"});
same({...ctx.parseProjectAdminRoute("#/projects")},{id:"",tab:"basic"});
assert.equal(ctx.parseProjectAdminRoute("#/issues/x"),null);
assert.equal(ctx.projectAdminHash("agentflow","basic"),"#/projects/agentflow");
assert.equal(ctx.projectAdminHash("agentflow","tapd"),"#/projects/agentflow/tapd");
assert.equal(ctx.projectAdminHash("",""),"#/projects");
assert.equal(ctx.projectAdminHash("a b","members"),"#/projects/a%20b/members");

// Project cards.
const card=ctx.projectCardHTML(projects[0],{repos:repositories,issues,currentId:"agentflow",workflowName:ctx.projectWorkflowName});
assert.match(card,/data-project-open="agentflow"/);
assert.match(card,/tabindex="0"/);
assert.match(card,/AgentFlow/);
assert.match(card,/agentflow · AF/);
assert.match(card,/平台本体/);
assert.match(card,/agent-flow \+1/);
assert.match(card,/通用 · Backend/);
assert.match(card,/TAPD/);
assert.match(card,/class="pa-chip cur">当前/);
assert.equal((card.match(/class="pa-av"/g)||[]).length,3,"at most three avatars");
assert.match(card,/pa-av more">\+2/);
assert.match(card,/<b>3<\/b> 需求/);
assert.match(card,/<b>2<\/b> 推进中/);
const bare=ctx.projectCardHTML(projects[2],{repos:repositories,issues,currentId:"agentflow"});
assert.match(bare,/pa-fact warn[^]*未绑定代码仓库/);
assert.match(bare,/暂无项目说明/);
assert.ok(!bare.includes("推进中"));

// Repository cards and empty state.
const rc=ctx.repoCardHTML(repositories[0],true);
assert.match(rc,/git\.example\.com\/example\/agent-flow/);
assert.match(rc,/preview/);
assert.match(rc,/agentflow\/integration/);
assert.match(rc,/pa-tag def">默认/);
assert.match(rc,/更新于 3 天前/);
assert.match(rc,/href="https:\/\/git\.example\.com\/example\/agent-flow" target="_blank" rel="noopener noreferrer"/);
assert.match(rc,/data-repo-edit="r1"/);
assert.match(rc,/data-repo-delete="r1"/);
const rc2=ctx.repoCardHTML(repositories[1],false);
assert.match(rc2,/未配置集成分支/);
assert.ok(!rc2.includes("默认</span>"));
assert.ok(!rc2.includes("更新于"),"zero time is not shown");
const list=ctx.projectRepoListHTML(repositories.slice(0,2),"r1");
assert.equal((list.match(/class="pa-rc"/g)||[]).length,2);
assert.match(list,/pa-rc-add/);
const empty=ctx.projectRepoListHTML([],"");
assert.match(empty,/还没有绑定代码仓库/);
assert.equal((empty.match(/<li>/g)||[]).length,3);
assert.match(empty,/选择 GitLab 仓库或填写 Git 地址[^]*确认分支[^]*保存即生效/);
assert.match(empty,/添加第一个仓库/);

// Detail panels.
same(vm.runInContext("PROJECT_ADMIN_TABS.map(t=>t.id)",ctx),["basic","repos","members","tapd"]);
const draft=ctx.projectDraftFrom(projects[0]);
same(draft.members.map(m=>m.userId),[2,3,4,5]);
same([...ctx.projectPeopleIds(draft)],[1,2,3,4,5]);
const edit=ctx.projectAdminFormHTML(draft,{creating:false,custom:false,repos:repositories.slice(0,2),saved:projects[0]});
assert.equal((edit.match(/data-pa-panel="/g)||[]).length,4);
assert.match(edit,/name="id" value="agentflow" required readonly/);
assert.match(edit,/修改 Key/);
assert.match(edit,/标识用于接口与链接，创建后不可修改/);
assert.match(edit,/<option value="r1" selected>agent-flow/);
assert.match(edit,/select name="workflowId"/);
assert.match(edit,/name="tapdClear"/);
assert.match(edit,/name="tapdWorkspaceId"/,"projects already using TAPD keep its settings");
{
  const plain={...projects[0],tapdWorkspaceId:"",tapdIterationId:""};
  const html=ctx.projectAdminFormHTML(ctx.projectDraftFrom(plain),{creating:false,custom:false,repos:[],saved:plain});
  assert.doesNotMatch(html,/tapdWorkspaceId|tapdClear|TAPD 需求池/,"TAPD stays hidden without an integration");
  assert.match(html,/危险操作/,"the advanced tab still offers project deletion");
  ctx.state.data.mcpServers=[{id:"tapd",marketId:"70"}];
  assert.match(ctx.projectAdminFormHTML(ctx.projectDraftFrom(plain),{creating:false,custom:false,repos:[],saved:plain}),/name="tapdWorkspaceId"/,"a TAPD MCP enables the settings");
  ctx.state.data.mcpServers=[];
}
assert.match(edit,/data-member-row="3"/);
assert.match(edit,/data-pa-panel="repos"[^>]* hidden/);
const fresh=ctx.projectAdminFormHTML({...ctx.blankProjectDraft(),name:"Web Console",id:"web-console",key:"WC"},{creating:true,custom:false,repos:[],saved:null});
assert.match(fresh,/自定义/);
assert.match(fresh,/恢复自动/);
assert.match(fresh,/根据项目名称自动生成/);
assert.match(fresh,/data-pa-panel="repos"[^>]*><\/section>/,"locked tabs render no content while creating");
assert.match(fresh,/data-pa-panel="members"[^>]*><\/section>/);
assert.ok(!/name="id"[^>]*readonly/.test(fresh));
assert.match(fresh,/保存项目后，在「代码仓库」中添加/);
assert.ok(!fresh.includes('name="ownerId"'),"signed-in users own new projects without a picker");
ctx.state.currentUser={isGuest:true};
const guest=ctx.projectAdminFormHTML(ctx.blankProjectDraft(),{creating:true,custom:false,repos:[],saved:null});
assert.match(guest,/data-pa-panel="basic"[^]*项目 Owner[^]*name="ownerId"[^]*data-pa-panel="repos"/,"guests pick an owner on the basic tab");
ctx.state.currentUser={id:1};
const prj=Array.from({length:27},(_,i)=>({id:"p"+i,key:i?"PRJ"+String.fromCharCode(64+i):"PRJ"}));
const fallback=gen("中文",prj,"zz99").key;
assert.match(fallback,/^PRJ[A-Z]{2}$/);
assert.ok(!prj.some(p=>p.key===fallback),"key stays unique after single-letter suffixes run out");

// Dirty tracking and discard baseline.
const base=ctx.clone(draft);
assert.equal(ctx.projectDraftDirty(draft,base),false);
assert.equal(ctx.projectDraftDirty({...draft,key:"af"},base),false,"key compare is case-insensitive");
assert.equal(ctx.projectDraftDirty({...draft,name:"AgentFlow 2"},base),true);
assert.equal(ctx.projectDraftDirty({...draft,members:[...draft.members,{userId:0}]},base),true);
assert.equal(ctx.projectDraftDirty({...draft,ownerId:"1"},base),false);
assert.equal(ctx.projectDraftDirty({...draft,tapdClear:true},base),true);

(async()=>{
  // New project: occupied identifiers never reach POST /api/projects.
  const f=(v)=>({elements:Object.fromEntries(Object.entries(v).map(([k,value])=>[k,{value}])),querySelector:()=>null});
  ctx.state.projectAdminDetailId="new";
  ctx.state.projectDraft={...ctx.blankProjectDraft(),name:"Dup",id:"web-console",key:"DUP"};
  await ctx.saveProjectAdmin({preventDefault(){},currentTarget:f({id:"web-console",key:"DUP",name:"Dup",workflowId:"wf",color:"#536dfe"})});
  assert.equal(requests.length,0,"occupied id must not be posted");
  assert.equal(ctx.__fail.name,"id");
  assert.match(ctx.__fail.message,/已被占用/);
  await ctx.saveProjectAdmin({preventDefault(){},currentTarget:f({id:"Web-Console",key:"DUP",name:"Dup",workflowId:"wf"})});
  assert.equal(requests.length,0,"occupancy check ignores case");

  // Only the name is needed: id/key come from the draft, owner falls back to the current user.
  const g=ctx.generateProjectIdentity("Fresh Project",projects,"");
  ctx.state.projectDraft={...ctx.blankProjectDraft(),name:"Fresh Project",...g};
  await ctx.saveProjectAdmin({preventDefault(){},currentTarget:f({id:g.id,key:g.key,name:"Fresh Project",workflowId:"wf",color:"#536dfe",description:""})});
  assert.equal(requests.length,1,notices.join("; "));
  assert.equal(requests[0].method,"POST");
  assert.equal(requests[0].body.id,"fresh-project");
  assert.equal(requests[0].body.key,"FP");
  assert.equal(requests[0].body.ownerId,1);
  assert.equal(ctx.state.projectAdminDetailId,"fresh-project");
  assert.equal(ctx.state.projectAdminTab,"repos");
  assert.equal(ctx.location.hash,"#/projects/fresh-project/repos");
  assert.equal(ctx.state.projectFilter,"agentflow","creating a project does not switch the workbench project");
  assert.ok(!("repoRows" in requests[0].body));

  // Repository drawer: saving with "default" on writes the repo, then the project, and keeps the open draft intact.
  requests.length=0;
  ctx.state.projectAdminDetailId="web-console";ctx.state.projectDraftFor="web-console";
  ctx.state.projectDraft={...ctx.projectDraftFrom(projects[1]),name:"Web Console 改名",description:"未保存说明"};
  ctx.state.projectDraftBase=ctx.projectDraftFrom(projects[1]);
  vm.runInContext('Object.assign(repoDrawerState,{projectId:"web-console",repoId:"",nameTouched:false,saving:false})',ctx);
  dom["#repoDrawerDefault"]=el({classes:["on"]});
  dom["#repoDrawerSave"]=el();
  dom["#repoDrawer"]=el({classes:["open"]});
  dom["#repoDrawerScrim"]=el({classes:["open"]});
  dom["#repoDrawerUrlError"]=el();
  const form={elements:{repoUrl:el({value:"git@git.example.com:group/new-repo.git"}),name:el({value:""}),defaultBranch:el({value:""}),integrateBranch:el({value:"agentflow/integration"}),localPath:el({value:""})}};
  await ctx.saveRepoDrawer({preventDefault(){},currentTarget:form});
  assert.equal(requests[0].url,"/api/repositories");
  assert.equal(requests[0].method,"POST");
  assert.equal(requests[0].body.gongfengProjectId,"group/new-repo");
  assert.equal(requests[0].body.name,"new-repo");
  assert.equal(requests[0].body.defaultBranch,"main");
  assert.equal(requests[0].body.cloneUrl,"git@git.example.com:group/new-repo.git");
  assert.equal(requests[1].url,"/api/projects");
  assert.equal(requests[1].body.defaultRepositoryId,"r9");
  assert.equal(requests[1].body.name,"Web Console","default repo update uses the saved project, not the draft");
  assert.equal(ctx.state.projectDraft.defaultRepositoryId,"r9");
  assert.equal(ctx.state.projectDraftBase.defaultRepositoryId,"r9");
  assert.equal(ctx.state.projectDraft.name,"Web Console 改名");
  assert.equal(ctx.state.projectDraft.description,"未保存说明");
  assert.ok(ctx.state.data.repositories.some(r=>r.id==="r9"));
  assert.equal(dom["#repoDrawer"].classList.contains("open"),false,"drawer closes after saving");

  // Focus returns into the detail form after the drawer closes; the repo cards must still be rebuilt.
  requests.length=0;dom["#repoDrawer"].classList.add("open");
  const focused=[],editBtn=el({dataset:{repoEdit:"r3"},focus(){focused.push("r3")}});
  const detailForm=el({innerHTML:"stale",dataset:{projectFor:"web-console",mode:"edit",paBound:"true"},contains:()=>true,
    elements:{name:{value:"Web Console 改名"},description:{value:"抽屉打开前输入"}},
    querySelectorAll:sel=>sel==="[data-repo-edit]"?[editBtn]:[]});
  dom["#projectAdminForm"]=detailForm;
  ctx.document.activeElement=el();ctx.document.body={dataset:{userComboboxBound:"true"}};
  vm.runInContext('Object.assign(repoDrawerState,{projectId:"web-console",repoId:"r3",nameTouched:true,saving:false})',ctx);
  dom["#repoDrawerDefault"].classList.add("on");
  Object.assign(form.elements,{repoUrl:el({value:"https://git.example.com/web/web"}),name:el({value:"web"}),defaultBranch:el({value:"main"}),integrateBranch:el({value:"agentflow/int2"})});
  await ctx.saveRepoDrawer({preventDefault(){},currentTarget:form});
  assert.equal(requests[0].method,"PUT");
  assert.equal(requests[0].body.integrateBranch,"agentflow/int2");
  assert.match(detailForm.innerHTML,/data-repo-card="r3"[^]*agentflow\/int2/,"edited card re-rendered while focus was inside the form");
  assert.match(detailForm.innerHTML,/data-repo-card="r9"/);
  assert.match(detailForm.innerHTML,/<option value="r3" selected>/,"default repository select follows the drawer");
  assert.equal(ctx.state.projectDraft.description,"抽屉打开前输入","unsaved form edits are kept");
  assert.equal(ctx.state.projectDraft.defaultRepositoryId,"r3");
  same(focused,["r3"],"focus moves to the rebuilt edit button");
  delete dom["#projectAdminForm"];ctx.document.activeElement=null;

  // Invalid URL keeps the drawer open and writes nothing.
  requests.length=0;dom["#repoDrawer"].classList.add("open");
  form.elements.repoUrl.value="https://bitbucket.org/foo/bar";
  await ctx.saveRepoDrawer({preventDefault(){},currentTarget:form});
  assert.equal(requests.length,0);
  assert.equal(dom["#repoDrawerUrlError"].hidden,false);
  assert.equal(dom["#repoDrawer"].classList.contains("open"),true);

  // Removing the default repository clears it from the draft and baseline only when it matched.
  ctx.state.projectDraft.defaultRepositoryId="r9";
  ctx.syncProjectDraftDefaultRepo("web-console","","r9");
  assert.equal(ctx.state.projectDraft.defaultRepositoryId,"");
  ctx.state.projectDraft.defaultRepositoryId="r3";
  ctx.syncProjectDraftDefaultRepo("web-console","","r9");
  assert.equal(ctx.state.projectDraft.defaultRepositoryId,"r3");
  console.log("project admin layout passed");
})().catch(err=>{console.error(err);process.exitCode=1;});
