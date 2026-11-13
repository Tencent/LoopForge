package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// InstallConversationApproval installs only provider-owned hooks. Generated
// scripts contain no credentials; their private gateway capability is supplied
// in the process environment and forwarded through the runner's control link.
func InstallConversationApproval(root, provider string, command CommandSpec) (CommandSpec, error) {
	hook := filepath.Join(root, "native-approval.py")
	if err := os.WriteFile(hook, []byte(conversationApprovalPython), 0600); err != nil {
		return command, err
	}
	launch := "python3 " + shellSingleQuote(hook)
	hooks := map[string]any{"PreToolUse": []any{map[string]any{"matcher": ".*", "hooks": []any{map[string]any{"type": "command", "command": launch, "timeout": 60}}}}}
	home := AgentCLIHome{Base: root, Rel: "home"}
	switch provider {
	case "codebuddy", "claude-code":
		settings := map[string]any{"hooks": hooks}
		data, _ := json.Marshal(settings)
		path := filepath.Join(root, "native-settings.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			return command, err
		}
		command.Argv = conversationReplaceFlag(command.Argv, "--settings", path)
	case AgentCLIZCode:
		if err := os.WriteFile(filepath.Join(root, "zcode-resume-model.py"), conversationZCodeResumeScript(), 0600); err != nil {
			return command, err
		}
		// ZCode loads user hooks from this config; retain MCP and the
		// SessionStart hook that removes the short-lived credential file.
		root, err := openAgentCLIHome(home)
		if err != nil {
			return command, err
		}
		data, err := root.ReadFile(zcodeConfigPath)
		root.Close()
		document := map[string]any{}
		if err != nil && !os.IsNotExist(err) {
			return command, err
		}
		if err == nil {
			if err = json.Unmarshal(data, &document); err != nil {
				return command, err
			}
		}
		configured, _ := document["hooks"].(map[string]any)
		if configured == nil {
			configured = map[string]any{}
		}
		events, _ := configured["events"].(map[string]any)
		if events == nil {
			events = map[string]any{}
		}
		events["PreToolUse"] = []any{map[string]any{"matcher": ".*", "hooks": []any{map[string]any{"type": "command", "command": launch, "timeout": 60, "onError": "deny"}}}}
		configured["enabled"], configured["events"] = true, events
		document["hooks"] = configured
		data, _ = json.Marshal(document)
		if err := writeAgentCLIFile(home, zcodeConfigPath, data); err != nil {
			return command, err
		}
	case "codex":
		entry := "hooks.PreToolUse=[{hooks=[{type=\"command\",command=" + strconv.Quote(launch) + ",timeout=60}]}]"
		command.Argv = append(command.Argv[:len(command.Argv)-1], "-c", "features.hooks=true", "-c", entry, command.Argv[len(command.Argv)-1])
	case AgentCLIKimi:
		command.AgentCLI.ApprovalCommand = launch
	case AgentCLIPi:
		if err := writeAgentCLIFile(home, ".pi/agent/extensions/loopforge-approval.ts", []byte(conversationPiApproval)); err != nil {
			return command, err
		}
	case AgentCLIOpenCode:
		if err := writeAgentCLIFile(home, ".config/opencode/plugins/loopforge-approval.js", []byte(conversationOpenCodeApproval)); err != nil {
			return command, err
		}
	case AgentCLICursor:
		// Cursor's preToolUse hook covers native reads, edits and shell alike.
		data, _ := json.Marshal(map[string]any{"version": 1, "hooks": map[string]any{"preToolUse": []any{map[string]any{"command": launch + " cursor", "timeout": 60, "failClosed": true}}}})
		if err := writeAgentCLIFile(home, ".cursor/hooks.json", data); err != nil {
			return command, err
		}
	case AgentCLIAntigravity:
		agyHooks := map[string]any{"loopforge-approval": map[string]any{"PreToolUse": []any{map[string]any{"matcher": ".*", "hooks": []any{map[string]any{"type": "command", "command": launch + " agy", "timeout": 60}}}}}}
		data, _ := json.Marshal(agyHooks)
		if err := writeAgentCLIFile(home, ".gemini/config/hooks.json", data); err != nil {
			return command, err
		}

	case AgentCLIDeepSeek:
		path := filepath.Join(root, "dsh-approval.mjs")
		if err := os.WriteFile(path, []byte(conversationDSHApproval), 0600); err != nil {
			return command, err
		}
		patch, _ := json.Marshal([]any{map[string]any{"insert": []any{map[string]any{"id": "loopforge-native-approval", "name": path}}}})
		patchFile := filepath.Join(root, "dsh-approval.json")
		if err := os.WriteFile(patchFile, patch, 0600); err != nil {
			return command, err
		}
		command.Argv = append(append(append([]string{}, command.Argv[:3]...), "--patch", patchFile), command.Argv[3:]...)
	default:
		return command, fmt.Errorf("CLI %s 缺少原生审批适配", provider)
	}
	return command, nil
}

func conversationReplaceFlag(argv []string, name, value string) []string {
	for i := 1; i < len(argv)-1; i++ {
		if argv[i] == name {
			argv[i+1] = value
			return argv
		}
	}
	return append(argv[:1], append([]string{name, value}, argv[1:]...)...)
}

const conversationApprovalPython = `import json,os,sys,urllib.request
allowed=False
reason="原生工具审批服务不可用，操作未执行"
try:
    event=json.load(sys.stdin)
    name=event.get("tool_name") or event.get("toolName") or event.get("tool",{}).get("name") or event.get("toolCall",{}).get("name") or event.get("hook_event_name","")
    arguments=event.get("tool_input",event.get("toolInput",event.get("input",event.get("arguments",event.get("toolCall",{}).get("args",{})))))
    payload={"jsonrpc":"2.0","id":1,"method":"agentflow/nativeApproval","params":{"name":name,"input":arguments}}
    url=os.environ["AGENTFLOW_NATIVE_APPROVAL_URL"]
    request=urllib.request.Request(url,json.dumps(payload).encode(),{"Content-Type":"application/json"})
    with urllib.request.urlopen(request,timeout=50) as response: result=json.load(response).get("result",{})
    allowed=result.get("allow") is True
    reason=result.get("reason",reason)
except Exception:
    pass
if len(sys.argv)>1 and sys.argv[1]=="agy":
    print(json.dumps({"decision":"allow" if allowed else "deny","reason":reason}))
elif len(sys.argv)>1 and sys.argv[1]=="cursor":
    print(json.dumps({"permission":"allow" if allowed else "deny","user_message":reason,"agent_message":reason}))
else:
    print(json.dumps({"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow" if allowed else "deny","permissionDecisionReason":reason}}))
if not allowed:
    print(reason,file=sys.stderr)
    sys.exit(2)
`

const conversationApprovalJS = `async function approve(name,input,signal) {
  const response=await fetch(process.env.AGENTFLOW_NATIVE_APPROVAL_URL,{method:"POST",headers:{"content-type":"application/json"},body:JSON.stringify({jsonrpc:"2.0",id:1,method:"agentflow/nativeApproval",params:{name,input}}),signal:signal??AbortSignal.timeout(50000)});
  if(!response.ok) throw new Error("原生工具审批服务不可用");
  const rpc=await response.json();
  if(rpc.result?.allow!==true) throw new Error(rpc.result?.reason||"操作未批准");
}
`
const conversationPiApproval = conversationApprovalJS + `
export default function(pi) {
  // Codemode runs in QuickJS without filesystem/network access. Its nested
  // calls enter this same tool_call pipeline, where each concrete tool is
  // checked separately. Checking the wrapper would block platform MCP reads.
  pi.on("tool_call",async(event,ctx)=>{if(event.toolName==="codemode")return;try{await approve(event.toolName,event.input,ctx.signal)}catch(error){return {block:true,reason:String(error.message)}}});
}
`
const conversationOpenCodeApproval = conversationApprovalJS + `
export const LoopForgeApproval=async()=>({"tool.execute.before":async(input,output)=>{await approve(input.tool,output.args)}});
`
const conversationDSHApproval = conversationApprovalJS + `
export const inject=["tools"];
export function apply(ctx) {
  ctx.on("tools/pre-execute",async(exec,next)=>{
    try { await approve(exec.name,exec.arguments,exec.signal); return next(); }
    catch(error) { return {kind:"deny",reason:String(error.message)}; }
  });
}
`
