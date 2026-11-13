package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeKimiHookHasNoWatchdogAndFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	dir := t.TempDir()
	loader := filepath.Join(dir, "loader.mjs")
	if err := os.WriteFile(loader, []byte(conversationKimiApprovalLoader), 0600); err != nil {
		t.Fatal(err)
	}
	// Exercise a real Node module loader and child process. The unadapted
	// vendor executor deliberately has a 1ms watchdog; only the owned hook
	// may outlive it. Preserve stdin and deny every unrecognised outcome.
	fixture := `// src/features/externalHooks/internal/runHook.ts
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
async function runHook(hostProcess, command, input, options) {
  return {action:'vendor',timeout:options.timeout};
}
const host={async spawn(command,args,options){
  if(process.env.LF_SPAWN_ERROR)throw new Error('private error');
  const child=spawn(command,args,{...options,env:process.env,stdio:'pipe'});
  return {...child,stdin:child.stdin,stdout:child.stdout,stderr:child.stderr,
    wait:()=>new Promise((resolve,reject)=>{child.once('exit',resolve);child.once('error',reject)}),
    kill:signal=>child.kill(signal),dispose(){}};
}};
const command=process.env.AGENTFLOW_KIMI_APPROVAL_COMMAND;
const controller=new AbortController();
if(process.env.LF_CANCEL)setTimeout(()=>controller.abort(),30);
const actual=await runHook(host,command,{toolCallId:'exact-id',toolName:'Write',toolInput:{content:'exact-content'}},{timeout:0.001,signal:controller.signal});
assert.equal(actual.action,process.env.LF_EXPECT);
assert.equal((await runHook(host,'other-hook',{}, {timeout:7})).action,'vendor');
`
	entry := filepath.Join(dir, "kimi.mjs")
	if err := os.WriteFile(entry, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "kimi-link.mjs")
	if err := os.Symlink(entry, linked); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "hook.mjs")
	hook := `let input='';for await(const part of process.stdin)input+=part;
const event=JSON.parse(input);if(event.toolCallId!=='exact-id'||event.toolInput.content!=='exact-content')process.exit(1);
await new Promise(resolve=>setTimeout(resolve,80));
const mode=process.env.LF_MODE;
if(mode==='crash')process.exit(1);
if(mode==='invalid')console.log('not-json');
else console.log(JSON.stringify({hookSpecificOutput:{permissionDecision:mode==='deny'?'deny':'allow'}}));
`
	if err := os.WriteFile(child, []byte(hook), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"allow", "symlink-allow", "deny", "invalid", "crash", "spawn-error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			expected := "block"
			if mode == "allow" || mode == "symlink-allow" {
				expected = "allow"
			}
			cmd := exec.Command("node", "--import", loader, entry)
			if mode == "symlink-allow" {
				cmd = exec.Command("node", "--import", loader, linked)
			}
			cmd.Env = append(os.Environ(), "AGENTFLOW_KIMI_APPROVAL_COMMAND=node "+shellSingleQuote(child), "LF_MODE="+mode, "LF_EXPECT="+expected)
			if mode == "symlink-allow" {
				cmd.Env = append(cmd.Env, "NODE_OPTIONS=--preserve-symlinks-main")
			}
			if mode == "spawn-error" {
				cmd.Env = append(cmd.Env, "LF_SPAWN_ERROR=1")
			}
			if mode == "cancel" {
				cmd.Env = append(cmd.Env, "LF_CANCEL=1")
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("owned hook %s: %v: %s", mode, err, out)
			}
		})
	}
	if err := os.WriteFile(entry, []byte("console.log('UNGUARDED CLI STARTED')"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "--import", loader, entry)
	cmd.Env = append(os.Environ(), "AGENTFLOW_KIMI_APPROVAL_COMMAND=owned-hook")
	out, err := cmd.CombinedOutput()
	if err == nil || strings.Contains(string(out), "UNGUARDED CLI STARTED\n") || !strings.Contains(string(out), "不兼容") {
		t.Fatalf("unknown Kimi entry must fail closed: %v: %s", err, out)
	}
}
