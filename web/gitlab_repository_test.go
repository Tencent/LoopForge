package web

import (
	"os/exec"
	"testing"
)

func TestGitLabRepositoryProjectOwner(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const state = {currentUser:{id:1},projectFilter:'unrelated',data:{users:[
 {id:1}, {id:2,gitLabBaseUrl:'https://owner.example.com'}, {id:3,gitLabBaseUrl:'https://other.example.com'}
],projects:[{id:'edited',ownerId:2},{id:'unrelated',ownerId:3}],repositories:[]}};
const code=fs.readFileSync('app/issues.js','utf8');
eval(code.slice(code.indexOf('function parseGongfengRepoURL('),code.indexOf('function fillRepositoryFromLink(')));
const url='https://owner.example.com/group/subgroup/repo';
assert.equal(parseGongfengRepoURL(url,'edited').provider,'gitlab','an admin without GitLab credentials uses the project owner');
assert.equal(parseGongfengRepoURL('git@owner.example.com:group/subgroup/repo.git','edited').webUrl,url);
assert.equal(parseGongfengRepoURL(url,{ownerId:2}).project,'gitlab:'+url);
assert.equal(parseGongfengRepoURL(url,'unrelated'),null,'another owner cannot authorize this instance');
assert.equal(parseGongfengRepoURL(url),null,'missing context does not borrow the editor credentials');
state.currentUser={id:2};
assert.equal(parseGongfengRepoURL(url,'edited').provider,'gitlab','owner and administrator get the same result');
assert.equal(parseGongfengRepoURL(url,'unrelated'),null,'even an owner editing another project uses that project owner');
assert.equal(parseGongfengRepoURL('https://github.com/example/repo','edited').provider,'github');
assert.equal(parseGongfengRepoURL('https://git.example.com/example/repo','edited').project,'example/repo');
assert.equal(parseGongfengRepoURL('https://owner.example.com.evil.test/group/repo','edited'),null);
assert.equal(parseGongfengRepoURL('https://user:pass@owner.example.com/group/repo','edited'),null);
// The new drawer must pass its own project, not the globally selected project.
const el=value=>({value,classList:{toggle(){}},focus(){}});
const form={elements:{repoUrl:el(url),name:el(''),id:el(''),defaultBranch:el('main'),integrateBranch:el(''),localPath:el('')}};
const elements={'#repoDrawerForm':form,'#repoDrawerDetect':{},'#repoDrawerUrlError':{},'#repoDrawerDetectName':{},'#repoDrawerDetectPath':{},'#repoDrawerDefault':{classList:{contains(){return false}}},'#repoDrawerSave':{}};
const $=id=>elements[id];
const calls=[];
const api=async(path,options)=>{calls.push({path,payload:JSON.parse(options.body)});return {...JSON.parse(options.body),id:'saved'}};
const captureProjectAdminForm=()=>{},renderAll=()=>{},refreshProjectAdminRepos=()=>{},toast=()=>{};
eval(fs.readFileSync('app/repo_drawer.js','utf8')+';globalThis.drawer=repoDrawerState;');
drawer.projectId='edited';
syncRepoDrawerDetect();
assert.equal(form.elements.name.value,'repo');
(async()=>{
 await saveRepoDrawer({preventDefault(){},currentTarget:form});
 assert.equal(calls.length,1);
 assert.equal(calls[0].payload.provider,'gitlab');
 assert.equal(calls[0].payload.projectId,'edited');
 assert.equal(calls[0].payload.webUrl,url);
 assert.equal(calls[0].payload.gongfengProjectId,'gitlab:'+url);
})().catch(error=>{console.error(error);process.exitCode=1});
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("GitLab owner regression: %v\n%s", err, out)
	}
}
