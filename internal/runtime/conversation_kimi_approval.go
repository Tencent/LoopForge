package runtime

// Kimi Code's command hook API caps timeouts at 600s and fails open when a
// command cannot start or times out. Its ACP API does not cover every native
// operation. Load this narrow adapter through Node's supported module loader;
// leave the installed CLI and all other hooks untouched. An unknown entry
// implementation fails before the CLI starts instead of losing the guard.
const conversationKimiApprovalLoader = `import {register} from "node:module";
import {isMainThread} from "node:worker_threads";
import {realpathSync} from "node:fs";
import {fileURLToPath,pathToFileURL} from "node:url";

let entryURL,approvalCommand;
if(isMainThread) {
  const command=process.env.AGENTFLOW_KIMI_APPROVAL_COMMAND;
  if(!command) throw new Error("Kimi 原生审批缺少受控 Hook");
  register(import.meta.url,{parentURL:import.meta.url,data:{entryURL:pathToFileURL(realpathSync(process.argv[1])).href,approvalCommand:command}});
}
export function initialize(data) {entryURL=data.entryURL;approvalCommand=data.approvalCommand;}
export async function load(url,context,nextLoad) {
  const loaded=await nextLoad(url,context);
  if(!url.startsWith("file:")||pathToFileURL(realpathSync(fileURLToPath(url))).href!==entryURL) return loaded;
  const source=Buffer.from(loaded.source).toString("utf8");
  const signature="async function runHook(hostProcess, command, input, options) {";
  if(source.split(signature).length!==2||!source.includes("src/features/externalHooks/internal/runHook.ts"))
    throw new Error("当前 Kimi 版本不兼容原生审批适配，请升级平台后重试");
  const branch="\nif(command==="+JSON.stringify(approvalCommand)+") return loopforgeNativeApprovalHook(hostProcess,command,input,options);\n";
  return {...loaded,source:source.replace(signature,signature+branch)+"\n"+loopforgeNativeApprovalHook.toString()};
}

async function loopforgeNativeApprovalHook(hostProcess,command,input,options) {
  const block=()=>({action:"block",reason:"本次原生操作未获批准或审批进程已结束",exitCode:2});
  if(options.signal?.aborted) return block();
  let proc;
  try {proc=await hostProcess.spawn(command,[],{shell:true,cwd:options.cwd,env:options.env});}
  catch {return block();}
  return await new Promise(resolve=>{
    let stdout="",settled=false;
    const finish=result=>{
      if(settled) return;
      settled=true;
      options.signal?.removeEventListener("abort",abort);
      resolve(result);
    };
    const abort=()=>{try{proc.kill("SIGKILL");}catch{}finish(block());};
    proc.stdout.setEncoding("utf8");
    proc.stderr.setEncoding("utf8");
    proc.stdout.on("data",chunk=>{stdout+=chunk;if(stdout.length>65536)abort();});
    // The Python hook only returns a small JSON decision. Do not surface
    // raw subprocess errors (they can contain credentials or tool input).
    proc.stderr.on("data",()=>{});
    const stdoutDone=new Promise(done=>proc.stdout.once("end",done));
    const stderrDone=new Promise(done=>proc.stderr.once("end",done));
    options.signal?.addEventListener("abort",abort,{once:true});
    if(options.signal?.aborted) {abort();return;}
    Promise.all([proc.wait(),stdoutDone,stderrDone]).then(([code])=>{
      let allowed=false;
      try {allowed=code===0&&JSON.parse(stdout).hookSpecificOutput?.permissionDecision==="allow";}catch{}
      finish(allowed?{action:"allow",exitCode:0}:block());
      proc.dispose();
    },()=>{finish(block());try{proc.dispose();}catch{}});
    proc.stdin.on("error",abort);
    try {proc.stdin.end(JSON.stringify(input));}catch{abort();}
  });
}
`
