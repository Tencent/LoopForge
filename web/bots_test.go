package web

import (
	"os/exec"
	"testing"
)

func TestBotSessionSearchAndTypes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := `const assert=require('node:assert/strict'),vm=require('node:vm'),fs=require('node:fs');const ctx={};vm.createContext(ctx);vm.runInContext(fs.readFileSync('app/bots.js','utf8'),ctx);const sessions=[{name:'研发群',type:'group'},{name:'Alice',type:'single'}];assert.equal(ctx.filteredBotSessions(sessions,'研发')[0].type,'group');assert.equal(ctx.filteredBotSessions(sessions,'ALICE')[0].type,'single');assert.equal(ctx.filteredBotSessions(sessions,'不存在').length,0);assert.equal(ctx.botTypeLabel('group'),'群聊');assert.equal(ctx.botTypeLabel('single'),'单聊');assert.equal(ctx.filteredBotSessions(sessions,'').length,2);ctx.escapeHTML=x=>String(x);assert.match(ctx.botModelPickerHTML({model:'gpt-6.1-sol'}),/name="model" value="gpt-6.1-sol"/);assert.match(fs.readFileSync('app/bots.js','utf8'),/name="model"/);`
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("search: %v %s", err, out)
	}
}
