package web

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The OAuth tab and the settings page must agree on the channel and message,
// and the settings listener must be registered once. The quick-access wizard
// asks for a GitHub token and still refreshes the user when focus returns.
func TestGongfengOAuthCompletionChannelIsShared(t *testing.T) {
	read := func(name string) string {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for name, wants := range map[string][]string{
		"gongfeng-oauth-done.js":               {"'agentflow-gongfeng-oauth'", "type:'gongfeng-oauth-completed'"},
		"app/workbench.js":                     {"'agentflow-gongfeng-oauth'", "'gongfeng-oauth-completed'", "function listenForGongfengOAuth()"},
		"../chatbot/src/QuickAccessWizard.jsx": {`window.addEventListener("focus", refreshUser)`},
	} {
		code := read(name)
		for _, want := range wants {
			if !strings.Contains(code, want) {
				t.Errorf("%s misses %q", name, want)
			}
		}
	}
	if wizard := read("../chatbot/src/QuickAccessWizard.jsx"); strings.Contains(wizard, "agentflow-gongfeng-oauth") || strings.Contains(wizard, "BroadcastChannel") {
		t.Error("the quick-access wizard must not offer the legacy OAuth flow")
	}
	if n := strings.Count(read("app.js"), "listenForGongfengOAuth()"); n != 1 {
		t.Fatalf("settings listener must be registered exactly once at startup, found %d calls", n)
	}
}

func TestGongfengOAuthCompletionRefreshesSettingsCardInPlace(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert=require('node:assert/strict');
const code=require('node:fs').readFileSync('` + appBundlePath(t) + `','utf8');
const state={currentUser:{id:1},data:{users:[{id:1,gitTokenConfigured:false}]}};
const userById=id=>state.data.users.find(u=>u.id===id);
const escapeHTML=v=>String(v),userAvatarHTML=()=>'',userDisplay=()=>'Alice',loadPersonalBotBindings=()=>{};
const toasts=[];const toast=(message,kind)=>toasts.push({message,kind});
let view='runs';const activeViewName=()=>view;
const calls=[];
const api=async(path)=>{calls.push(path);if(path==='/api/users')return [{id:1,gitTokenConfigured:true,gitTokenOAuth:true}];if(path.endsWith('/git-token/verify'))return {valid:true,scopes:['read_repository','write_repository']};return {}};
const channels=[];
class BroadcastChannel{constructor(name){this.name=name;channels.push(this)}close(){}}
let panelRenders=0,replacedRow='',progress='',filter='';
const unsaved={value:'typed-but-unsaved'};
const main={replaceWith(){}};
const row={classList:{contains:name=>name==='is-configured'&&!!replacedRow},replaceWith(next){replacedRow=next.html},querySelector(s){return s==='.credential-row-main'?main:null}};
const panel={set innerHTML(v){panelRenders++},querySelector(s){return {'[data-credential-row="gitToken"]':row,'.credential-progress':{set innerHTML(v){progress=v}},'.credential-filter':{set innerHTML(v){filter=v}}}[s]??null},querySelectorAll:()=>[]};
const $=s=>({'#credentialPanel':panel,'[data-credential-row="gitToken"]':row,'#credentialPanel [data-credential-row="gitToken"]':row}[s]??null);
const document={createElement(){const holder={set innerHTML(v){holder.firstElementChild={html:v}}};return holder},querySelectorAll:()=>[]};
const window={};
eval(code.slice(code.indexOf('const CREDENTIAL_GROUPS='),code.indexOf('// 谁能改通用模板')));
(async()=>{
 const channel=listenForGongfengOAuth();
 assert.equal(channels.length,1);assert.equal(channel.name,'agentflow-gongfeng-oauth');
 const flush=()=>new Promise(r=>setTimeout(r,10));
 channel.onmessage({data:{type:'gongfeng-oauth-completed'}});await flush();
 assert.deepEqual(calls,[],'other views must ignore the signal');
 view='settings';
 channel.onmessage({data:{type:'something-else'}});await flush();
 assert.deepEqual(calls,[],'only the completion message counts');
 channel.onmessage({data:{type:'gongfeng-oauth-completed'}});await flush();
 assert.deepEqual(calls,['/api/users','/api/users/1/git-token/verify']);
 assert.equal(panelRenders,0,'the panel must not be re-rendered');
 assert.equal(unsaved.value,'typed-but-unsaved');
 assert.equal(view,'settings');
 assert.ok(replacedRow.includes('is-configured')&&replacedRow.includes('工蜂授权'),'the Gongfeng row switches to the configured grant');
 assert.ok(progress.includes('<b>1</b>'));
 assert.ok(filter.includes('已配置<span>1</span>'));
 assert.equal(state.data.users[0].gitTokenOAuth,true);
 assert.equal(state.gitTokenVerify.result.valid,true);
 assert.ok(toasts.some(t=>t.message.includes('工蜂授权已保存')));
 assert.equal(toasts.at(-1).message,'Git 令牌校验通过');
 // Guests have no panel to patch, so a late signal touches nothing.
 state.currentUser={isGuest:true};calls.length=0;
 channel.onmessage({data:{type:'gongfeng-oauth-completed'}});await flush();
 assert.deepEqual(calls,[]);
})().catch(error=>{console.error(error);process.exit(1)});
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("gongfeng OAuth settings refresh failed: %v\n%s", err, out)
	}
}
