package runtime

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"agentflow/internal/domain"
)

// Workspace file access: pages the Agent opens as file:// are served to the
// user's own browser so they render natively, like dev servers. Paths are
// the ones the run browser sees (/workspace/... in a docker sidecar, the host
// workspace for a host browser) and must resolve inside that workspace.

// dockerWorkspaceRoot is where the browser sidecar mounts the workspace.
const dockerWorkspaceRoot = "/workspace"

// Outcomes shared by the sidecar script (exit codes) and the host reader.
const (
	workspaceFileOK          = 0
	workspaceFileMissing     = 44
	workspaceFileOutside     = 45
	workspaceFileDirectory   = 46 // a directory asked for without a trailing slash
	workspaceFileTooLarge    = 47
	workspaceFileNoIndexHTML = 48
)

// workspaceFileScript prints a workspace file inside the sidecar:
// node -e script ROOT PATH LIMIT. Symlinks are resolved before the
// containment check, and a directory serves its index.html.
const workspaceFileScript = `const fs=require("fs"),path=require("path");const[root0,p,limit]=process.argv.slice(1);` +
	`const real=f=>{try{return fs.realpathSync(f)}catch(e){process.exit(44)}};const root=real(root0);` +
	`const inside=r=>r===root||r.startsWith(root.endsWith("/")?root:root+"/");` +
	`let r=real(p);if(!inside(r))process.exit(45);let st=fs.statSync(r);` +
	`if(st.isDirectory()){if(!p.endsWith("/"))process.exit(46);const i=path.join(r,"index.html");if(!fs.existsSync(i))process.exit(48);` +
	`r=real(i);if(!inside(r))process.exit(45);st=fs.statSync(r);}` +
	`if(!st.isFile())process.exit(44);if(st.size>+limit)process.exit(47);process.stdout.write(fs.readFileSync(r),()=>process.exit(0));`

// workspaceFileExec runs the script in a sidecar; swapped in tests.
var workspaceFileExec = func(ctx context.Context, container, file string) ([]byte, int, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", container, "node", "-e", workspaceFileScript,
		dockerWorkspaceRoot, file, strconv.Itoa(DevServerBodyLimit)).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= workspaceFileMissing && exit.ExitCode() <= workspaceFileNoIndexHTML {
		return nil, exit.ExitCode(), nil
	}
	if err != nil {
		return nil, 0, err
	}
	return out, workspaceFileOK, nil
}

func handleWorkspaceFile(ctx context.Context, runID, file string) (domain.BrowserState, error) {
	if !strings.HasPrefix(file, "/") || strings.ContainsRune(file, 0) {
		return domain.BrowserState{}, errors.New("无效的工作区文件路径")
	}
	endpoint, ok := devServerLookup(ctx, runID)
	if !ok {
		return domain.BrowserState{Available: false, Reason: "Agent 浏览器尚未启动：工作区文件预览需要它所在的执行环境"}, nil
	}
	var (
		body []byte
		code int
		err  error
	)
	if endpoint.Kind == "docker" {
		body, code, err = workspaceFileExec(ctx, endpoint.Container, file)
	} else if endpoint.Workspace == "" {
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: "无法确定 Agent 浏览器的工作区目录"}, nil
	} else {
		body, code = serveWorkspaceFile(endpoint.Workspace, file, DevServerBodyLimit)
	}
	if err != nil {
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: "读取工作区文件失败：" + err.Error()}, nil
	}
	text := func(status int, message string) (domain.BrowserState, error) {
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, HTTP: &domain.BrowserHTTPResponse{
			Status: status, Header: map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}}, Body: []byte(message),
		}}, nil
	}
	switch code {
	case workspaceFileOK:
	case workspaceFileMissing:
		return text(http.StatusNotFound, "文件不存在："+file)
	case workspaceFileOutside:
		return text(http.StatusForbidden, "只能预览工作区内的文件")
	case workspaceFileDirectory:
		// The gateway adds the slash, so relative links resolve in the directory.
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, HTTP: &domain.BrowserHTTPResponse{Status: http.StatusMovedPermanently}}, nil
	case workspaceFileNoIndexHTML:
		return text(http.StatusNotFound, "目录下没有 index.html："+file)
	case workspaceFileTooLarge:
		return domain.BrowserState{Available: true, Kind: endpoint.Kind, Reason: fmt.Sprintf("文件超过 %d MB，预览暂不支持", DevServerBodyLimit>>20)}, nil
	}
	name := file
	if strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	return domain.BrowserState{Available: true, Kind: endpoint.Kind, HTTP: &domain.BrowserHTTPResponse{
		Status: http.StatusOK, Header: map[string][]string{"Content-Type": {contentType}}, Body: body,
	}}, nil
}

// serveWorkspaceFile is the host browser's counterpart of workspaceFileScript.
func serveWorkspaceFile(root, file string, limit int64) ([]byte, int) {
	nativePath, ok := hostWorkspaceFilePath(file)
	if !ok {
		return nil, workspaceFileOutside
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, workspaceFileMissing
	}
	inside := func(p string) bool {
		rel, err := filepath.Rel(realRoot, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	real, err := filepath.EvalSymlinks(nativePath)
	if err != nil {
		return nil, workspaceFileMissing
	}
	if !inside(real) {
		return nil, workspaceFileOutside
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, workspaceFileMissing
	}
	if info.IsDir() {
		if !strings.HasSuffix(file, "/") {
			return nil, workspaceFileDirectory
		}
		index, err := filepath.EvalSymlinks(filepath.Join(real, "index.html"))
		if err != nil {
			return nil, workspaceFileNoIndexHTML
		}
		if !inside(index) {
			return nil, workspaceFileOutside
		}
		if info, err = os.Stat(index); err != nil {
			return nil, workspaceFileNoIndexHTML
		}
		real = index
	}
	if !info.Mode().IsRegular() {
		return nil, workspaceFileMissing
	}
	if info.Size() > limit {
		return nil, workspaceFileTooLarge
	}
	body, err := os.ReadFile(real)
	if err != nil {
		return nil, workspaceFileMissing
	}
	return body, workspaceFileOK
}
