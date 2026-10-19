const fs = require('fs');
const vm = require('vm');
const assert = require('assert');
const bundle = fs.readFileSync(process.argv[2], 'utf8');
const start = bundle.indexOf('async function deleteIssueById(');
assert(start >= 0);
const end = bundle.indexOf('\n}', start) + 2;
const code = bundle.slice(start, end);
const visibilityStart = bundle.indexOf('function issueCanDelete(');
const visibilityEnd = bundle.indexOf('\n}', visibilityStart) + 2;
const visibility = {issueClarifying: id => id === 'busy'};
vm.createContext(visibility);
vm.runInContext(bundle.slice(visibilityStart, visibilityEnd), visibility);
assert.equal(visibility.issueCanDelete({id:'idle',deletionAllowed:true}),true);
assert.equal(visibility.issueCanDelete({id:'success',deletionAllowed:false}),false);
assert.equal(visibility.issueCanDelete({id:'busy',deletionAllowed:true}),false);
assert.equal(visibility.issueCanDelete({id:'unknown'}),false);
const closeStart = bundle.indexOf('function issueCanClose(');
const closeEnd = bundle.indexOf('\n}', closeStart) + 2;
vm.runInContext(bundle.slice(closeStart, closeEnd), visibility);
assert.equal(visibility.issueCanClose({id:'settled',closureAllowed:true}),true);
assert.equal(visibility.issueCanClose({id:'active',closureAllowed:false}),false);
assert.equal(visibility.issueCanClose({id:'busy',closureAllowed:true}),false);
async function run({confirm=true, busy=false, fail=false}={}) {
 const issue={id:'issue/1',title:'测试需求',source:'tapd'};
 const calls=[];
 const state={data:{issues:[issue,{id:'other'}]},issueDetailData:{issue},selectedIssue:issue.id};
 const context={state,issueClarifying:()=>busy,toast:()=>{},refreshRuns:async()=>{},renderIssues:()=>calls.push('render'),closeIssueDrawer:()=>calls.push('close'),afConfirm:async opts=>{assert(opts.message.includes('后续同步可能重新导入'));return confirm;},api:async(path,opts)=>{calls.push([path,opts.method]);if(fail)throw new Error('conflict');}};
 vm.createContext(context);vm.runInContext(code,context);
 await context.deleteIssueById(issue.id);
 return {state,calls};
}
(async()=>{
 let out=await run();assert.deepStrictEqual(out.calls,[['/api/issues/issue%2F1','DELETE'],'close','render']);assert.equal(out.state.data.issues.length,1);assert.equal(out.state.issueDetailData,null);
 for(const opts of [{confirm:false},{busy:true},{fail:true}]){out=await run(opts);assert.equal(out.state.data.issues.length,2);assert(out.state.issueDetailData);assert(!out.calls.includes('close'));}
})().catch(err=>{console.error(err);process.exit(1);});
