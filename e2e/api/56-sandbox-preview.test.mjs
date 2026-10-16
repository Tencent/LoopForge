import {test,before,after} from 'node:test';
import assert from 'node:assert/strict';
import {api,get,post,put,del,track,cleanup,ensureProject,waitRun,waitFor} from '../lib/api.mjs';
import {cfg,uid} from '../lib/env.mjs';
import {createWorkflow,script,startRun} from '../lib/fixtures.mjs';

before(ensureProject);
after(cleanup);

test('Docker sandbox: real container, shared state, environment and artifact handoff', {skip:process.env.AGENTFLOW_E2E_DOCKER!=='1',timeout:600_000},async()=>{
  const dockerfile=uid('docker-file'),sandbox=uid('docker-sandbox');
  track('Dockerfile',()=>del(`/api/dockerfiles?id=${dockerfile}`));
  track('Docker sandbox',()=>del(`/api/sandboxes?id=${sandbox}`));
  await put('/api/dockerfiles',{id:dockerfile,projectId:cfg.projectId,name:'E2E Docker execution',content:'FROM node:22-bookworm-slim\nWORKDIR /workspace\n'});
  await put('/api/sandboxes',{id:sandbox,projectId:cfg.projectId,name:'E2E container',driver:'docker',dockerfileId:dockerfile,environment:{E2E_SANDBOX_VALUE:'actual-container'}});
  const wf=await createWorkflow('docker-run',[
    script('build',`test -f /.dockerenv && test "$E2E_SANDBOX_VALUE" = actual-container
node -e "require('fs').writeFileSync('report.txt','docker-runtime-ok\\n')"
printf 'shared-container' > /tmp/loopforge-e2e-marker`,{artifactOutputs:[{name:'report',path:'report.txt',mediaType:'text/plain',required:true}]}),
    script('verify',`test "$(cat /tmp/loopforge-e2e-marker)" = shared-container && grep -q docker-runtime-ok .agentflow/inputs/build/report.txt && echo DOCKER_VERIFIED`),
  ],{sandboxProfileId:sandbox});
  const {id}=await startRun(wf);
  const run=await waitRun(id,r=>r.status==='succeeded',{timeoutMs:540_000});
  assert.equal(run.workflowSnapshot.sandboxSnapshot.profile.driver,'docker');
  assert.match((await api('GET',`/api/runs/${id}/logs.txt`,undefined,{raw:true})).text,/DOCKER_VERIFIED/);
});

test('static preview: real builder, archived HTML/assets, session gateway and revocation',{skip:!cfg.model,timeout:600_000},async()=>{
  const wf=await createWorkflow('static-preview',[
    script('seed',`mkdir -p src/assets
printf '<!doctype html><html><head><title>E2E Preview</title></head><body>LOOPFORGE_PREVIEW_OK<button id="spa">SPA route</button><button id="navigate">Page route</button><script src="/assets/app.js"></script></body></html>' > src/index.html
cat > src/assets/app.js <<'JS'
window.loopforgePreview="real-asset";
history.replaceState(null,"",location.pathname+location.search);
window.addEventListener("popstate",function(){});
document.getElementById("spa").addEventListener("click",function(){history.pushState(null,"","/about?mode=spa");});
document.getElementById("navigate").addEventListener("click",function(){location.assign("/contact?mode=page");});
JS
cat > package.json <<'JSON'
{"name":"loopforge-preview-fixture","private":true,"scripts":{"build":"node build.cjs"}}
JSON
cat > build.cjs <<'JS'
const fs=require('node:fs');fs.rmSync('dist',{recursive:true,force:true});fs.cpSync('src','dist',{recursive:true});
JS`),
  ],{preview:{enabled:true,kind:'static',sourcePath:'dist',model:cfg.modelID,spaFallback:true}});
  const {id}=await startRun(wf,'自动化预览验收：当前工作区是完整的零依赖静态前端工程。package.json 的 build 命令通过 Node 将 src 复制为 dist。请运行 npm run build，保留 LOOPFORGE_PREVIEW_OK 标记，交付生成的预览。无需安装第三方包。');
  await waitRun(id,r=>r.status==='succeeded',{timeoutMs:540_000});
  const preview=await waitFor('preview archived',async()=>{
    const p=await get(`/api/runs/${id}/preview`,{expect:[200,404]});
    if(p.status==='failed')throw new Error(p.error||'preview finalization failed');
    return p.status==='ready'?p:null;
  },{timeoutMs:60_000,intervalMs:1000});
  assert.ok(preview.id);
  const session=await post(`/api/runs/${id}/preview/session`,{});
  const address=new URL(session.url,cfg.base);
  const html=await api('GET',address.pathname+address.search,undefined,{auth:false,raw:true});
  assert.match(html.text,/LOOPFORGE_PREVIEW_OK/);
  assert.match(html.headers.get('content-security-policy')||'',/sandbox/);
  const asset=await api('GET',new URL('assets/app.js',address).pathname,undefined,{auth:false,raw:true});
  assert.match(asset.text,/real-asset/);
  assert.equal(html.headers.get('referrer-policy'),'same-origin');
  assert.match(html.text,/data-af-preview-history/);
  const root=address.pathname.replace(/\/$/,'');
  const target=new URL('/about?from=preview',cfg.base);
  const {get:request}=await import(target.protocol==='https:'?'node:https':'node:http');
  const stray=await new Promise((resolve,reject)=>{
    const req=request(target,{
      headers:{Referer:address.href,Accept:'text/html','Sec-Fetch-Mode':'navigate','Sec-Fetch-Site':'same-origin'},signal:AbortSignal.timeout(15_000),
    },response=>{response.resume();resolve(response);});
    req.on('error',reject);
  });
  assert.equal(stray.statusCode,303);
  assert.equal(stray.headers.location,`${root}/about?from=preview`);
  if(process.env.AGENTFLOW_E2E_PREVIEW_BROWSER==='1'){
    const {chromium}=await import('@playwright/test');
    const browser=await chromium.launch({args:['--no-sandbox']});
    try{
      const page=await browser.newPage();
      await page.goto(address.href);
      await page.waitForFunction(prefix=>location.pathname.startsWith(prefix),root);
      await page.locator('#spa').click();
      await page.waitForURL(url=>url.pathname===`${root}/about`&&url.search==='?mode=spa');
      await page.reload();
      await page.waitForFunction(prefix=>location.pathname===prefix+'/about',root);
      assert.match(await page.locator('body').innerText(),/LOOPFORGE_PREVIEW_OK/);
      await page.locator('#navigate').click();
      await page.waitForURL(url=>url.pathname===`${root}/contact`&&url.search==='?mode=page');
      await page.goBack();
      await page.waitForFunction(prefix=>location.pathname===prefix+'/about',root);
      assert.match(await page.locator('body').innerText(),/LOOPFORGE_PREVIEW_OK/);
    }finally{await browser.close();}
  }
  await del(`/api/runs/${id}/preview`,{expect:[200,202,204]});
  await api('GET',address.pathname+address.search,undefined,{auth:false,expect:[403,404,410]});
});
