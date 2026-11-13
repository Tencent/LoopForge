package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestZCodeConversationReselectsModelWithoutReplacingJournal(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected", true: "rejected"}[rejected], func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "fake-zcode")
			fake := `#!/usr/bin/env python3
import json,os,sys
home=os.environ["HOME"]
assert "ZCODE_BASE_URL" not in os.environ
if sys.argv[1:]==["app-server","--stdio"]:
    source=os.environ["ZCODE_PERSONAL_PROVIDER_CONFIG_FILE"]
    with open(source) as f: assert json.load(f)["config"]["defaultModelSelection"]["modelId"]=="new/model$literal"
    os.unlink(source) # The real SessionStart hook removes its credential file.
    for line in sys.stdin:
        req=json.loads(line)
        assert req["params"]["sessionId"]=="sess_original"
        if req["method"]=="session/resume":
            assert req["params"]["toolDenylist"]==["Bash","Write"]
            result={"settings":{"model":{"current":{"providerId":"agentflow","modelId":"old-model"},"available":[{"ref":{"providerId":"agentflow","modelId":"new/model$literal"},"reasoning":{"defaultLevel":"none","levels":[{"value":"none"}]}}]}}}
        elif req["method"]=="session/setModel":
            if os.environ.get("TEST_REJECT"):
                print(json.dumps({"id":req["id"],"error":{"message":"secret-from-protocol-error"}}),flush=True)
                continue
            assert req["params"]["model"]=={"providerId":"agentflow","modelId":"new/model$literal","options":{"reasoningLevel":"none"}}
            assert req["params"]["persistAsWorkspaceLastUsed"] is False
            with open(home+"/selected.json","w") as f: json.dump(req["params"]["model"],f)
            result={"settings":{"model":{"current":req["params"]["model"]}}}
        else: raise AssertionError("unexpected protocol method")
        print(json.dumps({"id":req["id"],"result":result}),flush=True)
else:
    assert sys.argv[1:]==["--resume","sess_original","--disallowed-tools","Bash Write","--prompt","literal $HOME "+chr(96)+"command"+chr(96)+" prompt"]
    with open(os.environ["ZCODE_PERSONAL_PROVIDER_CONFIG_FILE"]) as f: assert json.load(f)["config"]["defaultModelSelection"]["modelId"]=="new/model$literal"
    with open(home+"/selected.json") as f: assert json.load(f)["modelId"]=="new/model$literal"
    with open(home+"/prompt-ran","w") as f: f.write("resumed original session")
`
			if err := os.WriteFile(binary, []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(root, "home")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(home, "native-journal")
			if err := os.WriteFile(journal, []byte("original native history"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "zcode-resume-model.py"), conversationZCodeResumeScript(), 0600); err != nil {
				t.Fatal(err)
			}
			native := []string{binary, "--resume", "sess_original", "--disallowed-tools", "Bash Write", "--prompt", "literal $HOME `command` prompt"}
			bound, env, err := ApplyAgentCLICredential(AgentCLIZCode, ProviderCredential{APIKey: "fixture-key"}, AgentCLIOptions{Model: "new/model$literal"}, HostAgentCLIHome(home), home, native)
			if err != nil {
				t.Fatal(err)
			}
			argv := conversationZCodeResumeArgv(root, native, bound)
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Env = append(os.Environ(), "HOME="+home, "ZCODE_BASE_URL=host-setting")
			for key, value := range env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			if rejected {
				cmd.Env = append(cmd.Env, "TEST_REJECT=1")
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != rejected {
				t.Fatalf("rejected=%v: %s: %v", rejected, output, err)
			}
			_, effectErr := os.Stat(filepath.Join(home, "prompt-ran"))
			if (effectErr != nil) != rejected || strings.Contains(string(output), "secret-from-protocol-error") {
				t.Fatalf("configuration failure ran a prompt or exposed its error: %s", output)
			}
			data, err := os.ReadFile(journal)
			if err != nil || string(data) != "original native history" {
				t.Fatal("native journal was replaced")
			}
			files := zcodeKeyFiles(t, home)
			for _, file := range files {
				if strings.Contains(file, "preflight-") {
					t.Fatal("preparatory credential retained")
				}
			}
			if !rejected && len(files) != 0 {
				t.Fatal("prompt credential retained")
			}
		})
	}
}

func TestZCodeConversationNewSessionDoesNotRunModelPreparation(t *testing.T) {
	native := []string{"zcode", "--prompt", "first turn"}
	bound := []string{"sh", "-c", "bound command"}
	if got := conversationZCodeResumeArgv(t.TempDir(), native, bound); !slices.Equal(got, bound) {
		t.Fatalf("new session unexpectedly ran a resume: %q", got)
	}
}
