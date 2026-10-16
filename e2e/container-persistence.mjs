// Called by test-container.sh before and after force-recreating the app.
import fs from 'node:fs';
const [phase,stateFile]=process.argv.slice(2);
const base=process.env.LOOPFORGE_TEST_URL;
if(!['prepare','verify'].includes(phase)||!base||!stateFile)throw new Error('Expected phase, protected state path and test URL');
let state;
async function request(method,path,body){
  const headers={Cookie:state.cookie,Origin:base};
  if(body!==undefined && !(body instanceof FormData))headers['Content-Type']='application/json';
  const res=await fetch(base+path,{method,headers,body:body===undefined?undefined:body instanceof FormData?body:JSON.stringify(body)});
  if(!res.ok)throw new Error(`${method} ${path}: HTTP ${res.status}`);
  return res;
}
if(phase==='prepare'){
  const login=await fetch(base+'/api/auth/password',{method:'POST',headers:{Origin:base,'Content-Type':'application/json'},body:JSON.stringify({login:process.env.LOOPFORGE_TEST_USER||'admin',password:process.env.LOOPFORGE_TEST_PASSWORD})});
  if(!login.ok)throw new Error('Persistence test login failed');
  state={cookie:login.headers.get('set-cookie').split(';')[0],sessionId:'e2e-container-persistence'};
  const form=new FormData();form.set('projectId','welcome');form.set('file',new Blob(['persistent attachment\n']),'persistence.txt');
  state.attachment=(await (await request('POST','/api/assistant/attachments',form)).json()).id;
  await request('PUT','/api/assistant/sessions/'+state.sessionId,{projectId:'welcome',title:'E2E container recreation',messages:[{role:'user',content:'persistent conversation'}]});
  fs.writeFileSync(stateFile,JSON.stringify(state),{mode:0o600});fs.chmodSync(stateFile,0o600);
}else{
  state=JSON.parse(fs.readFileSync(stateFile,'utf8'));
  const text=await (await request('GET','/api/assistant/attachments/'+state.attachment)).text();
  if(text!=='persistent attachment\n')throw new Error('Attachment lost after recreation');
  const session=await (await request('GET','/api/assistant/sessions/'+state.sessionId)).json();
  if(session.messages[0].content!=='persistent conversation')throw new Error('Conversation lost after recreation');
  await request('DELETE','/api/assistant/sessions/'+state.sessionId);
  console.log('Container recreation preserved login, conversation and attachment.');
}
