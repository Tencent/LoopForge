package web

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRunQueueInteraction(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "run_queue_test.js", appBundlePath(t)).CombinedOutput(); err != nil {
		t.Fatalf("run queue interaction: %v\n%s", err, out)
	}
}

func TestRunQueueAssets(t *testing.T) {
	for name, want := range map[string]string{
		"index.html":     `data-run-filter="queued">排队中`,
		"product-v4.css": ".info-tip:hover .info-tip-panel",
		"styles.css":     ".status.queued",
		"app.js":         "if(name!=='runs')stopQueuedRunRefresh()",
	} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want) {
			t.Fatalf("%s missing %q", name, want)
		}
	}
	page, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<h1>运行记录<span class="info-tip" tabindex="0"`, "平台所有项目共用执行名额", "默认 10 条顶层 LOOP", "平台管理员可在「执行机器」调整", "空闲 1 小时后自动归档工作区并释放名额"} {
		if !strings.Contains(string(page), want) {
			t.Fatalf("run concurrency hint missing %q", want)
		}
	}
	data, err := os.ReadFile("app/runs.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"取消排队", "host.dataset.controlSignature=runControlSignature(r)", "r.status!=='queued'", "if(r.status==='queued')", "自动分配 · 等待可用机器", "空闲 1 小时后归档并释放名额"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("queued run controls missing %q", want)
		}
	}
}
