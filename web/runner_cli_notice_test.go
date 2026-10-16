package web

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestRunnerCLIUpdateNotice(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]*html.Node{}
	for node := range doc.Descendants() {
		for _, attr := range node.Attr {
			if attr.Key == "id" {
				nodes[attr.Val] = node
			}
		}
	}
	notice, list, view := nodes["runnerCLIUpdateNotice"], nodes["runnersList"], nodes["runnersView"]
	if notice == nil || list == nil || view == nil || notice.Parent != view || list.Parent != view || bytes.Index(data, []byte(`id="runnerCLIUpdateNotice"`)) > bytes.Index(data, []byte(`id="runnersList"`)) {
		t.Fatal("notice must be a static sibling above the machine list")
	}
	var markup bytes.Buffer
	if err := html.Render(&markup, notice); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"由您自行安装和更新", "平台不会代为升级", "部分模型或功能不可用", "所选模型的访问权限", "Runner 自身的自动更新不包括 Codex / CodeBuddy / Claude Code"} {
		if !strings.Contains(markup.String(), want) {
			t.Errorf("missing maintenance guidance: %s", want)
		}
	}
	for _, hidden := range []string{"<button", "<details", " hidden", "aria-hidden", " style="} {
		if strings.Contains(markup.String(), hidden) {
			t.Errorf("notice must be persistent and non-interactive: %s", hidden)
		}
	}
	app, err := os.ReadFile("app/runners.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"runnerCLIUpdateNotice", "data-update-cli", "data-upgrade-cli", "/cli/update", "/cli/upgrade", "一键升级", "远程升级"} {
		if bytes.Contains(app, []byte(forbidden)) {
			t.Errorf("runner rendering must not hide the notice or upgrade user CLIs: %s", forbidden)
		}
	}
	if !bytes.Contains(app, []byte("rt.version||rt.binaryPath")) {
		t.Fatal("keep discovered CLI version display")
	}
}
