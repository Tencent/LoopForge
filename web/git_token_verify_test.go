package web

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCredentialTipStaysInsideRow(t *testing.T) {
	css, err := os.ReadFile("platform.css")
	if err != nil {
		t.Fatal(err)
	}
	const selector = ".credential-tip::after{"
	start := strings.Index(string(css), selector)
	if start < 0 {
		t.Fatalf("platform.css is missing %q", selector)
	}
	rule := string(css[start:])
	rule = rule[:strings.Index(rule, "}")]
	for _, want := range []string{"right:calc(100% + ", "top:50%", "translate(3px,-50%)"} {
		if !strings.Contains(rule, want) {
			t.Fatalf("credential tooltip should sit left of the button, missing %q in %s", want, rule)
		}
	}
	if strings.Contains(rule, "bottom:calc(100%") {
		t.Fatalf("credential tooltip must not render above the button (clipped by .credential-row-main overflow): %s", rule)
	}
	if !strings.Contains(string(css), ".credential-tip:focus-within::after{opacity:1;transform:translate(0,-50%)}") {
		t.Fatal("credential tooltip visible state should keep vertical centering")
	}
}

func TestGitTokenVerificationBadge(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict');
const code=require('node:fs').readFileSync('` + appBundlePath(t) + `','utf8');
const state={currentUser:{id:1},data:{users:[{id:1,gitTokenConfigured:true}]}};
const userById=id=>state.data.users.find(u=>u.id===id);
const escapeHTML=v=>String(v),userAvatarHTML=()=>'',userDisplay=()=>'Alice';
const toasts=[];const toast=(message,kind)=>toasts.push({message,kind});
let result={valid:false,error:'当前配置的是工蜂「私人令牌」，只能用于 API，不能克隆或推送仓库。请创建「个人访问令牌」'};
const calls=[];
const api=async(path,options={})=>{
  calls.push({path,method:options.method||'GET',body:options.body});
  if(path.endsWith('/credentials'))return {id:1,gitTokenConfigured:true};
  if(path==='/api/auth/gongfeng')return {enabled:false};
  return result;
};
const verifyCalls=()=>calls.filter(c=>c.path.endsWith('/git-token/verify'));
const putCalls=()=>calls.filter(c=>c.method==='PUT');
const panel={innerHTML:'',classList:{remove(){}},querySelectorAll:()=>[]};
const host={innerHTML:'',querySelector(selector){return {'#credentialPanel':panel}[selector]??null},querySelectorAll:()=>[]};
const posted=[];
const $=selector=>({'#personalSettingsContent':host,'#credentialPanel':panel,'#chatNextFrame':{contentWindow:{postMessage:m=>posted.push(m)}}}[selector]??null);
const document={querySelectorAll:()=>[],activeElement:null};
const window={};
const location={search:'',pathname:'/',hash:'',origin:'http://localhost'};
let replaced=0;const history={replaceState(){replaced++}};
eval(code.slice(code.indexOf('const CREDENTIAL_GROUPS='),code.indexOf('// 谁能改通用模板'))+';globalThis.cred={kind:credentialKind};');
const gitToken=cred.kind('gitToken');
(async()=>{
 // Entering the page verifies a configured token once and caches the answer.
 await renderPersonalSettings();await new Promise(setImmediate);
 assert.equal(verifyCalls().length,1);
 assert.equal(verifyCalls()[0].method,'GET');
 // The page shows only the account strip and the credential panel; WeCom bindings are not listed here.
 assert.ok(!host.innerHTML.includes('data-personal-bot-bindings')&&!host.innerHTML.includes('企业微信'));
 assert.ok(!calls.some(c=>c.path.startsWith('/api/bot-bindings')));
 assert.equal(credentialStatus(gitToken,state.data.users[0]),'err');
 assert.ok(credentialSubline(gitToken,state.data.users[0]).includes('私人令牌'));
 await renderPersonalSettings();await new Promise(setImmediate);
 assert.equal(verifyCalls().length,1,'cached result must preserve error on rerender');
 assert.ok(panel.innerHTML.includes('有错误'),'the row badge shows the cached failure');
 assert.ok(panel.innerHTML.includes('git-token-pat-warning'));
 assert.ok(!panel.innerHTML.includes('credential-grid')&&!panel.innerHTML.includes('保存个人凭证'));
 // The row's 重新校验 button refreshes the status in place.
 result={valid:true,scopes:['read_repository','write_repository']};
 await refreshGitTokenStatus({announce:true});
 assert.equal(credentialStatus(gitToken,state.data.users[0]),'ok');
 assert.ok(credentialSubline(gitToken,state.data.users[0]).includes('令牌有效'));
 assert.ok(credentialSubline(gitToken,state.data.users[0]).includes('read_repository · write_repository'));
 assert.equal(toasts.at(-1).message,'Git 令牌校验通过');
 result={valid:false,missing:['write_repository']};
 await refreshGitTokenStatus();
 assert.equal(credentialStatus(gitToken,state.data.users[0]),'err');
 assert.ok(credentialSubline(gitToken,state.data.users[0]).includes('缺少权限 write_repository'));
 assert.ok(credentialBadgeHTML('empty').includes('未配置'));
 // The expanded 代码仓库 row opens on a read-only detail; the token input sits in a hidden replace pane.
 assert.ok(state.gitTokenVerify.checkedAt>0,'a finished verification records when it ran');
 const editor=credentialEditorHTML(gitToken,state.data.users[0]);
 assert.ok(editor.includes('credential-editor-form is-detail')&&editor.includes('data-git-detail'));
 assert.ok(editor.includes('<code class="credential-scope is-miss" title="缺少此权限">write_repository</code>'));
 assert.ok(editor.includes('令牌有效但缺少权限 write_repository</span> · 刚刚'));
 assert.ok(editor.includes('class="credential-btn primary credential-ink" data-credential-act="replace"'),'a failing token promotes 手动更换令牌');
 assert.ok(editor.includes('class="credential-replace is-off" data-credential-pane="form" inert'));
 assert.ok(editor.includes('data-git-verify-line')&&editor.includes('data-credential-act="cancel"')&&editor.includes('data-credential-act="confirm-delete"'));
 assert.ok(editor.includes('个人访问令牌</dd>'));
 assert.ok(credentialEditorHTML(gitToken,{...state.data.users[0],gitTokenOAuth:true}).includes('工蜂授权 · 到期自动续期'));
 assert.ok(!credentialEditorHTML(cred.kind('codexApiKey'),{codexApiKeyConfigured:true}).includes('is-detail'),'other kinds keep the original editor');
 state.gitTokenVerify={userId:1};
 assert.ok(gitTokenDetailHTML(gitToken,state.data.users[0]).includes('尚未校验')&&gitTokenDetailHTML(gitToken,state.data.users[0]).includes('校验后显示'));
 state.gitTokenVerify={userId:1,busy:true,result:{valid:true,scopes:['api']}};
 assert.ok(gitTokenDetailHTML(gitToken,state.data.users[0]).includes('校验中')&&gitTokenDetailHTML(gitToken,state.data.users[0]).includes('>api</code>'),'re-checking keeps the previous scopes');
 // A typed token is verified first; nothing is saved if it fails.
 result={valid:false,error:'令牌无效或已过期'};
 const failed=await verifyAndSaveGitToken('typed-pat');
 assert.equal(failed.saved,false);
 const typed=verifyCalls().at(-1);
 assert.equal(typed.method,'POST');
 assert.deepEqual(JSON.parse(typed.body),{token:'typed-pat'});
 assert.equal(putCalls().length,0,'an invalid token must not be saved');
 // A verified token is saved alone and the verification is kept as the row status.
 result={valid:true,scopes:['api','read_repository','write_repository']};
 const saved=await verifyAndSaveGitToken('typed-pat');
 assert.equal(saved.saved,true);
 assert.equal(putCalls().length,1);
 assert.deepEqual(JSON.parse(putCalls()[0].body),{gitToken:'typed-pat'});
 assert.equal(state.gitTokenVerify.result.valid,true);
 assert.deepEqual(posted,[{type:'agentflow-credentials-changed'}]);
 // Returning from Gongfeng OAuth clears the cache, announces and re-verifies.
 location.search='?gongfeng=ok';
 result={valid:false,missing:['api']};
 const before=verifyCalls().length;
 await renderPersonalSettings();await new Promise(setImmediate);
 assert.equal(replaced,1);
 assert.equal(verifyCalls().length,before+1);
 assert.ok(toasts.some(t=>t.message.includes('工蜂授权已保存')));
 assert.ok(toasts.at(-1).message.includes('api')&&toasts.at(-1).kind==='err');
 // The chat frame may clean ?gongfeng=ok first; the flag it leaves still announces the return.
 location.search='';
 state.gongfengReturned=true;
 const announced=toasts.length,beforeFlag=verifyCalls().length;
 await renderPersonalSettings();await new Promise(setImmediate);
 assert.equal(state.gongfengReturned,false);
 assert.equal(replaced,1,'an already clean URL is left alone');
 assert.equal(verifyCalls().length,beforeFlag+1);
 assert.ok(toasts.slice(announced).some(t=>t.message.includes('工蜂授权已保存')));
})().catch(error=>{console.error(error);process.exit(1)});
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("git token UI check failed: %v\n%s", err, out)
	}
}
