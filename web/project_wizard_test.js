"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const [projectsPath, drawerPath, wizardPath, issuesPath] = process.argv.slice(2);

function extract(source, name) {
  const start = source.search(new RegExp("^(?:async )?function " + name + "\\(", "m"));
  assert.ok(start >= 0, name);
  const rest = source.slice(start), next = /\n(?:async )?function \w+\(|\n\/\//.exec(rest.slice(1));
  return next ? rest.slice(0, next.index + 1) : rest;
}

const requests = [], notices = [], confirms = [], views = [], switcherEvents = [];
const users = [{id:1,loginName:"alice"},{id:2,loginName:"bob"},{id:3,loginName:"carol"}];
const projects = [
  {id:"agentflow",key:"AF",name:"AgentFlow",ownerId:1,members:[{userId:2}],workflowId:"wf",defaultRepositoryId:""},
];
const repositories = [];
let confirmAnswer = true, failNext = "", pending = null;

// A host whose innerHTML setter mounts a fake dialog shell and whose getter reassembles it, so the
// tests see the same markup whether a render remounted the shell or only swapped its parts.
let mounts = 0;
function part(re, html) { return (html.match(re) || ["", ""])[1]; }
function host() {
  let dialog = null;
  const h = {id:"",className:"",dataset:{},addEventListener(){},querySelectorAll:()=>[],remove(){delete dom["#projectWizard"]},
    querySelector:sel=>sel===".pw-dialog"?dialog:null,
    get innerHTML(){
      if(!dialog)return "";
      const d=dialog;
      return `<div class="pw-backdrop" data-pw-backdrop>${d.head.replace(/ data-pw-step="[^"]*"( aria-busy="true")?>$/,` data-pw-step="${d.dataset.pwStep}"${d.attrs["aria-busy"]?' aria-busy="true"':""}>`)}${d.rail.outerHTML}<div class="pw-main">${d.close.html.replace(/ disabled>/,">").replace(/aria-label="关闭">/,`aria-label="关闭"${d.close.disabled?" disabled":""}>`)}<div class="pw-body">${d.body.innerHTML}</div>${d.footer.outerHTML}</div></div></div>`;
    },
    set innerHTML(html){
      mounts++;
      const close={html:part(/(<button type="button" class="pw-close"[^]*?<\/button>)/,html),disabled:/class="pw-close"[^>]* disabled>/.test(html)};
      const parts={rail:{outerHTML:part(/(<aside class="pw-rail">[^]*?<\/aside>)/,html)},body:{innerHTML:part(/<div class="pw-body">([^]*)<\/div><footer/,html),scrollTop:0},footer:{outerHTML:part(/(<footer class="pw-footer">[^]*<\/footer>)/,html)},close};
      dialog={...parts,head:part(/(<div class="pw-dialog"[^>]*>)/,html),dataset:{pwStep:part(/data-pw-step="([^"]*)"/,html)},attrs:{"aria-busy":/aria-busy="true"/.test(html)},
        setAttribute(k,v){this.attrs[k]=v},removeAttribute(k){delete this.attrs[k]},
        querySelector:sel=>({".pw-body":parts.body,".pw-rail":parts.rail,".pw-footer":parts.footer,".pw-close":close}[sel]||null)};
    },
  };
  return h;
}
const dom = {};
let hashListener = null;
const ctx = {
  URL,
  console, Event,
  state:{projectFilter:"agentflow",currentUser:{id:1},projectAdminDetailId:null,projectAdminTab:"basic",projectDraft:null,projectDraftFor:"",projectDraftBase:null,
    data:{projects,repositories,issues:[],users,workflows:[{id:"generic-backend-delivery",name:"后端交付",generic:true},{id:"wf",name:"Backend"}],businessDomains:[],mcpServers:[{id:"tapd",marketId:"70"}]}},
  escapeHTML:s=>String(s??"").replace(/[&<>'"]/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;","'":"&#39;",'"':"&quot;"}[c])),
  safeColor:v=>/^#[0-9a-f]{3,8}$/i.test(String(v||""))?v:"#74829a",
  clone:v=>v==null?v:JSON.parse(JSON.stringify(v)),
  userById:id=>users.find(u=>u.id===Number(id)),
  userDisplay:u=>u.loginName,
  userAvatarHTML:u=>`<i>${(u&&u.loginName||"?")[0]}</i>`,
  issueRunHealth:()=>"",
  defaultRunEval:()=>({enabled:true}),
  isPlatformAdmin:()=>true,
  toast:m=>notices.push(m),
  afConfirm:async opt=>{confirms.push(opt.message);return confirmAnswer},
  renderAll:()=>{},
  renderProjectSwitcher:()=>{},
  switchView:name=>views.push(name),
  localStorage:{setItem(){}},
  activeViewName:()=>"projects",
  location:{hash:"#/projects"},
  history:{state:null,replaceState(s,t,h){ctx.location.hash=h},pushState(s,t,h){ctx.location.hash=h},back(){}},
  businessState:{report:null,seq:0},
  requestAnimationFrame:()=>{},
  window:{addEventListener(type,fn){if(type==="hashchange")hashListener=fn}},
  setTimeout:()=>0,
  api:async(url,init)=>{
    const body=init&&init.body?JSON.parse(init.body):null;
    requests.push({url,method:(init&&init.method)||"GET",body});
    if(pending)await pending;
    if(failNext){const m=failNext;failNext="";throw new Error(m)}
    if(url==="/api/repositories")return {...body,id:"r"+(repositories.length+9)};
    if(url.startsWith("/api/repositories?"))return {};
    return body;
  },
  $:sel=>dom[sel]||null,
  $$:()=>[],
};
ctx.document={activeElement:null,contains:()=>false,addEventListener(){},body:{dataset:{},classList:{add(){},remove(){}},appendChild(el){dom["#"+el.id]=el}},createElement:()=>host()};
dom["#projectSwitcher"]={value:"",dispatchEvent(e){switcherEvents.push({type:e.type,value:this.value})}};
vm.createContext(ctx);
for (const name of ["parseGongfengRepoURL","repoDisplayPath","repoProviderLabel"]) vm.runInContext(extract(fs.readFileSync(issuesPath,"utf8"),name),ctx);
vm.runInContext(fs.readFileSync(projectsPath,"utf8"),ctx);
vm.runInContext(fs.readFileSync(drawerPath,"utf8"),ctx);
vm.runInContext(fs.readFileSync(wizardPath,"utf8"),ctx);

const W=()=>vm.runInContext("projectWizard",ctx);
const html=()=>dom["#projectWizard"]?.innerHTML||"";
const footer=()=>(html().match(/<footer class="pw-footer">[^]*<\/footer>/)||[""])[0];
const nav=()=>(footer().match(/<div class="pw-footer-nav">([^]*)<\/div><\/footer>/)||["",""])[1];
const actions=s=>[...s.matchAll(/data-pw-action="([\w-]+)"/g)].map(m=>m[1]);
const sameList=(a,b,m)=>assert.deepEqual([...a],b,m);
function assertFooterLayout(step){
  const f=footer(),left=f.split("pw-footer-spacer")[0];
  assert.ok(!f.includes("稍后再说"),step+": no later button");
  if(step==="basic"){sameList(actions(left),["cancel"]);sameList(actions(nav()),["next"]);assert.match(nav(),/创建并继续/);return}
  sameList(actions(left),["skip"],step+": skip sits alone on the left");
  assert.match(left,/跳过此步/);
  const expected=step==="repos"?["next"]:["back","next"];
  sameList(actions(nav()),expected,step+": back is next to the primary button");
  assert.match(nav(),step==="tapd"?/完成/:/下一步/);
}

(async()=>{
  // Opening starts at step 1 with only the name required and no identity inputs.
  ctx.openProjectWizard();
  assert.equal(W().step,"basic");
  const basic=html();
  assert.match(basic,/role="dialog" aria-modal="true"/);
  assert.match(basic,/第 1 步，共 4 步/);
  assert.match(basic,/name="name"/);
  assert.match(basic,/name="workflowId"[^]*value="generic-backend-delivery" selected/);
  for(const bad of ['name="id"','name="key"',"自定义","标识与 Key","data-pa-ident"])assert.ok(!basic.includes(bad),"step 1 has no "+bad);
  assert.ok(!basic.includes('name="ownerId"'),"signed-in users own the project without a picker");
  assert.equal((basic.match(/class="pw-step[ "]/g)||[]).length,5);
  assert.ok(!/<span class="pw-step-mark">5</.test(basic),"the closing 完成 item is not numbered as a fifth step");
  assert.match(basic,/pw-step is-active" aria-current="step"><span class="pw-step-mark">1/);
  assert.match(basic,/所有配置之后都能在项目设置里修改/);
  assertFooterLayout("basic");

  // Cancel with nothing typed closes without asking or requesting.
  await ctx.closeProjectWizard();
  assert.equal(W(),null);assert.equal(confirms.length,0);assert.equal(requests.length,0);

  // Closing after typing asks first; "继续编辑" keeps the dialog.
  ctx.openProjectWizard();
  mounts=0;ctx.renderProjectWizard();
  assert.equal(mounts,0,"re-rendering keeps the mounted dialog shell");
  mounts=1;
  W().draft.name="Web Console";
  confirmAnswer=false;
  assert.equal(await ctx.closeProjectWizard(),false);
  assert.ok(W(),"still open");
  assert.match(confirms[0],/新项目还没有创建/);
  confirmAnswer=true;

  // Empty name stays on step 1 with an error and no request.
  W().draft.name="  ";
  await ctx.projectWizardNext();
  assert.equal(W().step,"basic");assert.equal(requests.length,0);
  assert.match(html(),/role="alert">项目名称不能为空/);

  // Server errors keep step 1.
  W().draft.name="Web Console";failNext="项目标识已存在";
  await ctx.projectWizardNext();
  assert.equal(W().step,"basic");assert.equal(requests.length,1);
  assert.match(html(),/创建失败[^<]*项目标识已存在/);
  requests.length=0;

  // Create: id/key are generated from the name; one POST, then step 2.
  await ctx.projectWizardNext();
  assert.equal(requests.length,1);
  assert.equal(requests[0].method,"POST");assert.equal(requests[0].url,"/api/projects");
  assert.equal(requests[0].body.id,"web-console");
  assert.equal(requests[0].body.key,"WC");
  assert.equal(requests[0].body.ownerId,1);
  assert.equal(requests[0].body.workflowId,"generic-backend-delivery");
  assert.ok(!("businessDomainId" in requests[0].body));
  assert.equal(W().step,"repos");assert.equal(W().projectId,"web-console");
  assert.ok(projects.some(p=>p.id==="web-console"),"project list includes the new project");
  assert.match(html(),/第 2 步，共 4 步/);
  assert.match(html(),/项目「Web Console」已创建/);
  assert.match(html(),/pw-step is-done"><span class="pw-step-mark"><svg/);
  assertFooterLayout("repos");

  // Repositories: invalid URL is rejected locally; the first saved repo becomes the default.
  requests.length=0;
  W().repo.url="https://bitbucket.org/foo/bar";
  assert.equal(await ctx.projectWizardAddRepo(),false);
  assert.equal(requests.length,0);
  assert.match(html(),/无法识别为 GitHub 仓库地址/);
  Object.assign(W().repo,{url:"https://github.com/example/web-console",branch:"",integrate:"agentflow/integration"});
  assert.equal(await ctx.projectWizardAddRepo(),true);
  assert.equal(requests[0].url,"/api/repositories");assert.equal(requests[0].method,"POST");
  assert.equal(requests[0].body.projectId,"web-console");
  assert.equal(requests[0].body.gongfengProjectId,"github:example/web-console");
  assert.equal(requests[0].body.provider,"github");
  assert.equal(requests[0].body.name,"web-console");
  assert.equal(requests[0].body.defaultBranch,"main");
  assert.equal(requests[0].body.integrateBranch,"agentflow/integration");
  assert.equal(requests[1].url,"/api/projects");assert.equal(requests[1].method,"PUT");
  assert.equal(requests[1].body.defaultRepositoryId,"r9");
  assert.equal(W().repo.url,"","inputs reset after adding");
  assert.match(html(),/data-pw-repo="r9"[^]*is-def">默认/);
  requests.length=0;
  W().repo.url="git@git.example.com:example/api-service.git";
  await ctx.projectWizardAddRepo();
  assert.equal(requests.length,1,"second repository does not touch the default");
  W().repo.url="https://git.example.com/example/api-service";
  assert.equal(await ctx.projectWizardAddRepo(),false,"duplicate repository");
  W().repo.url="";
  requests.length=0;
  await ctx.projectWizardNext();
  assert.equal(requests.length,0);
  assert.equal(W().step,"members");assert.ok(W().done.repos);
  assertFooterLayout("members");
  assert.match(html(),/第 3 步，共 4 步/);

  // Members: no change → no request; back to repos is allowed, never to step 1.
  await ctx.projectWizardNext();
  assert.equal(requests.length,0);assert.equal(W().step,"tapd");assert.ok(W().skipped.members);
  assert.match(html(),/<i>已跳过<\/i>/);
  assertFooterLayout("tapd");
  await ctx.projectWizardBack();
  assert.equal(W().step,"members");
  await ctx.projectWizardBack();
  assert.equal(W().step,"repos");
  await ctx.projectWizardBack();
  assert.equal(W().step,"repos","step 1 is not reachable after creation");
  await ctx.projectWizardNext();
  assert.equal(W().step,"members");

  // Duplicate or empty members are rejected without a request.
  W().draft.members=[{userId:2,role:"member"},{userId:2,role:"member"}];
  await ctx.projectWizardNext();
  assert.equal(requests.length,0);assert.equal(W().step,"members");
  assert.match(html(),/项目成员不能重复/);
  W().draft.members=[{userId:0,role:"member"}];
  await ctx.projectWizardNext();
  assert.equal(requests.length,0);
  assert.match(html(),/请选择有效的项目成员/);

  // Skip with unsaved changes asks; declining stays.
  confirmAnswer=false;confirms.length=0;
  await ctx.projectWizardSkip();
  assert.equal(confirms.length,1);assert.equal(W().step,"members");
  confirmAnswer=true;

  // Closing mid-way with unsaved changes asks too.
  confirmAnswer=false;confirms.length=0;
  assert.equal(await ctx.closeProjectWizard(),false);
  assert.match(confirms[0],/项目已创建并保留/);
  confirmAnswer=true;

  // A real change PUTs the members.
  W().draft.members=[{userId:2,role:"member"},{userId:3,role:"member"}];
  await ctx.projectWizardNext();
  assert.equal(requests.length,1);
  assert.equal(requests[0].method,"PUT");
  assert.deepEqual(requests[0].body.members.map(m=>m.userId),[2,3]);
  assert.equal(requests[0].body.defaultRepositoryId,"r9","default repository survives the member save");
  assert.equal(requests[0].body.key,"WC");
  assert.equal(W().step,"tapd");assert.ok(W().done.members);
  requests.length=0;

  // TAPD: PUT failure stays; success opens the summary.
  W().draft.tapdWorkspaceId=" 20375341 ";failNext="boom";
  await ctx.projectWizardNext();
  assert.equal(W().step,"tapd");assert.match(html(),/role="alert">boom/);
  requests.length=0;
  await ctx.projectWizardNext();
  assert.equal(requests.length,1);
  assert.equal(requests[0].body.tapdWorkspaceId,"20375341");
  assert.equal(W().step,"done");
  const done=html();
  assert.match(done,/项目「Web Console」已创建/);
  assert.match(done,/data-pw-summary="project">[^]*Web Console · <code>web-console<\/code> · <code>WC<\/code>/);
  assert.match(done,/is-ok" data-pw-summary="repos">[^]*2 个，默认 web-console/);
  assert.match(done,/is-ok" data-pw-summary="members">[^]*3 位（含 Owner）/);
  assert.match(done,/is-ok" data-pw-summary="tapd">[^]*20375341/);
  assert.match(done,/开始第一条需求[^]*进入项目设置/);
  sameList(actions(footer()),["close"]);
  assert.ok(!done.includes("第 5 步"));
  assert.equal(mounts,1,"errors, saves and step changes never remount the dialog (no replayed entrance animation)");

  // Start first requirement: closes, switches the workbench project and opens issues.
  await ctx.projectWizardStartIssues();
  assert.equal(W(),null);
  assert.deepEqual(switcherEvents,[{type:"change",value:"web-console"}]);
  assert.deepEqual(views,["issues"]);

  // A second "Web Console" dedupes against the first; skipping everything sends nothing after the POST.
  ctx.openProjectWizard();W().draft.name="Web Console";requests.length=0;
  await ctx.projectWizardNext();
  assert.equal(requests[0].body.id,"web-console-2");
  assert.equal(requests[0].body.key,"WCA");

  // Removing a repository asks first; removing the default promotes the next one and says so.
  W().repo.url="https://git.example.com/example/a";await ctx.projectWizardAddRepo();
  W().repo.url="https://git.example.com/example/b";await ctx.projectWizardAddRepo();
  const defaultRepo=ctx.state.data.projects.find(p=>p.id==="web-console-2").defaultRepositoryId;
  assert.equal(defaultRepo,"r11");
  requests.length=0;confirms.length=0;confirmAnswer=false;
  assert.equal(await ctx.projectWizardRemoveRepo("r11"),false);
  assert.equal(confirms.length,1);assert.match(confirms[0],/当前的默认仓库/);
  assert.equal(requests.length,0,"declining keeps the repository");
  confirmAnswer=true;notices.length=0;
  assert.equal(await ctx.projectWizardRemoveRepo("r11"),true);
  assert.deepEqual(requests.map(r=>r.method+" "+r.url),["DELETE /api/repositories?id=r11","PUT /api/projects"]);
  assert.equal(requests[1].body.defaultRepositoryId,"r12");
  assert.match(notices[0],/已将「b」设为默认仓库/);
  await ctx.projectWizardRemoveRepo("r12");
  assert.equal(ctx.projectWizardRepos().length,0);
  requests.length=0;confirms.length=0;
  await ctx.projectWizardSkip();await ctx.projectWizardSkip();
  W().draft.tapdWorkspaceId="1";
  confirms.length=0;
  await ctx.projectWizardSkip();
  assert.equal(confirms.length,1,"skip with typed TAPD asks first");
  assert.equal(requests.length,0,"skipping never writes");
  assert.equal(W().step,"done");
  assert.ok(W().skipped.repos&&W().skipped.members&&W().skipped.tapd);
  assert.match(html(),/is-warn" data-pw-summary="repos">[^]*未配置，可稍后在项目设置补充/);
  assert.match(html(),/is-warn" data-pw-summary="members">[^]*仅 Owner/);
  assert.match(html(),/is-warn" data-pw-summary="tapd">/);

  // "进入项目设置" opens the tabbed editor for the new project.
  await ctx.projectWizardOpenSettings();
  assert.equal(W(),null);
  assert.equal(ctx.state.projectAdminDetailId,"web-console-2");
  assert.equal(ctx.location.hash,"#/projects/web-console-2");

  // Saving locks the dialog.
  ctx.resetProjectAdminDetail();ctx.location.hash="#/projects";
  ctx.openProjectWizard();W().draft.name="Locked";
  let release;pending=new Promise(r=>{release=r});
  const creating=ctx.projectWizardNext();
  assert.equal(W().saving,true);
  assert.match(footer(),/正在保存…/);
  assert.match(html(),/class="pw-close" data-pw-action="close" aria-label="关闭" disabled/);
  assert.equal(await ctx.closeProjectWizard({force:true}),false,"cannot close while saving");
  release();pending=null;await creating;
  assert.equal(W().step,"repos");
  await ctx.closeProjectWizard();
  assert.equal(W(),null);

  // Routes: #/projects/new opens the wizard over the list; closing returns to #/projects.
  ctx.location.hash="#/projects/new";
  ctx.syncProjectAdminRouteState();
  assert.equal(W().step,"basic");
  assert.equal(ctx.state.projectAdminDetailId,null,"no full-page create form");
  await ctx.closeProjectWizard();
  assert.equal(ctx.location.hash,"#/projects");
  ctx.location.hash="#/projects/new";
  await ctx.applyProjectAdminRoute();
  assert.ok(W(),"hashchange to #/projects/new opens the wizard");
  await ctx.closeProjectWizard();
  ctx.openProjectAdminDetail("new");
  assert.ok(W(),"legacy callers of the new route get the wizard");
  await ctx.closeProjectWizard();

  // Editing an existing project still uses the tabbed page.
  ctx.openProjectAdminDetail("agentflow","members");
  assert.equal(W(),null);
  assert.equal(ctx.state.projectAdminDetailId,"agentflow");
  assert.equal(ctx.state.projectAdminTab,"members");
  assert.equal(ctx.location.hash,"#/projects/agentflow/members");

  // Leaving the hash the wizard was opened on (back button, another view) closes it.
  ctx.resetProjectAdminDetail();ctx.location.hash="#/projects";
  ctx.openProjectWizard();W().draft.name="Unsaved";
  ctx.location.hash="#/projects/new";hashListener();
  assert.ok(W(),"moving to #/projects/new keeps the wizard");
  ctx.location.hash="#/issues";hashListener();
  assert.equal(W(),null,"navigating to another view closes the wizard");

  // Guests choose an owner on step 1.
  ctx.resetProjectAdminDetail();
  ctx.state.currentUser={isGuest:true};
  ctx.openProjectWizard();
  assert.match(html(),/项目 Owner[^]*name="ownerId"/);
  W().draft.name="Guest Project";requests.length=0;
  await ctx.projectWizardNext();
  assert.equal(requests.length,0);assert.match(html(),/请选择项目 Owner/);
  // Without a TAPD integration the wizard goes from members straight to the summary.
  await ctx.closeProjectWizard({force:true});
  ctx.state.currentUser={id:1};ctx.state.data.mcpServers=[];
  ctx.resetProjectAdminDetail();ctx.location.hash="#/projects";
  ctx.openProjectWizard();
  assert.doesNotMatch(html(),/TAPD/,"no TAPD step is listed");
  assert.match(html(),/第 1 步，共 3 步/);
  W().draft.name="No Tracker";requests.length=0;
  await ctx.projectWizardNext();
  await ctx.projectWizardSkip();
  assert.equal(W().step,"members");
  await ctx.projectWizardNext();
  assert.equal(W().step,"done");
  assert.doesNotMatch(html(),/data-pw-summary="tapd"/);
  await ctx.closeProjectWizard({force:true});
  console.log("project wizard passed");
})().catch(err=>{console.error(err);process.exitCode=1;});
