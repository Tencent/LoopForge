const fs = require('fs');
const vm = require('vm');
const assert = require('assert');
const bundle = fs.readFileSync(process.argv[2], 'utf8');
const start = bundle.indexOf('async function deleteIssueById(');
assert(start >= 0);
const end = bundle.indexOf('\n}', start) + 2;
const code = bundle.slice(start, end);
const helpers = bundle.slice(bundle.indexOf('function issueDeletePath('), bundle.indexOf('function issueDeletionContextHTML('));
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
async function run({confirm=true, busy=false, fail=false, batch=false, force=false, historyChanged=false}={}) {
 const issue={id:'issue/1',title:'测试需求',source:'tapd',deletionAllowed:true,deletionRequiresForce:force};
 const calls=[];
 const state={data:{issues:[issue,{id:'other'}]},issueDetailData:{issue},selectedIssue:issue.id,issueBulkDeleting:batch,issueSelection:new Set([issue.id,'other'])};
 const context={state,issueCanDelete:()=>!busy,issueDeletionBlock:()=>({reason:'正在澄清'}),toast:()=>{},refreshRuns:async()=>{},refreshIssues:async()=>calls.push('refresh issues'),renderIssues:()=>calls.push('render'),closeIssueDrawer:()=>calls.push('close'),
  afConfirm:async opts=>{
   assert(opts.message.includes('后续同步可能重新导入'));
   assert.equal(opts.message.includes('任务、运行记录、日志、产物和执行工作区'),force);
   assert.equal(opts.confirmText,force?'确认强制删除':'确认删除');
   if(historyChanged)issue.deletionRequiresForce=true;
   return confirm;
  },
  api:async(path,opts)=>{calls.push([path,opts.method]);if(fail)throw new Error('conflict');}};
 vm.createContext(context);vm.runInContext(helpers+'\n'+code,context);
 await context.deleteIssueById(issue.id);
 return {state,calls};
}
(async()=>{
 let out=await run();assert.deepStrictEqual(out.calls,[['/api/issues/issue%2F1','DELETE'],'close','render']);assert.equal(out.state.data.issues.length,1);assert.equal(out.state.issueDetailData,null);assert.equal(out.state.issueSelection.has('issue/1'),false);assert.equal(out.state.issueSelection.has('other'),true);
 out=await run({force:true});assert.deepStrictEqual(out.calls,[['/api/issues/issue%2F1?force=true','DELETE'],'close','render']);
 out=await run({historyChanged:true,fail:true});assert.deepStrictEqual(out.calls,[['/api/issues/issue%2F1','DELETE'],'refresh issues']);
 for(const opts of [{confirm:false,force:true},{busy:true},{fail:true},{batch:true}]){out=await run(opts);assert.equal(out.state.data.issues.length,2);assert(out.state.issueDetailData);assert(!out.calls.includes('close'));}
})().catch(err=>{console.error(err);process.exit(1);});
