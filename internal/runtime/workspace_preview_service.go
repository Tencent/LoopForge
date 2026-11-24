package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// WorkspacePreviewService runs a separate Docker exec in the workspace
// container. The development server is supervised independently of the
// conversation CLI and never receives the CLI's per-turn credentials.
func WorkspacePreviewService(ctx context.Context, sandbox Sandbox, req domain.WorkspacePreviewServiceRequest) (domain.WorkspacePreviewServiceState, error) {
	var state domain.WorkspacePreviewServiceState
	if err := req.Validate(); err != nil {
		return state, err
	}
	sb, ok := unwrapSandbox(sandbox).(*dockerSandbox)
	if !ok {
		return state, errors.New("持续预览仅支持 Docker 工作区")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := sb.Exec(ctx, CommandSpec{Argv: []string{
		"python3", "-c", dockerWorkspacePreviewService, req.Operation, req.Name, req.Command,
		dockerConversationProcessSupervisor, dockerConversationProcessStop,
	}}, nil)
	if err != nil {
		return state, fmt.Errorf("操作预览服务 %s 失败: %w: %s", req.Name, err, strings.TrimSpace(out))
	}
	if err := json.Unmarshal([]byte(out), &state); err != nil {
		return state, fmt.Errorf("读取预览服务 %s 状态失败: %w", req.Name, err)
	}
	return state, nil
}

// The control files live in the container's tmpfs, outside the workspace
// snapshot. File locking serializes retries and concurrent starts or stops.
// An outer subreaper adopts and reaps the service tree if the inner
// supervisor dies before it can clean up.
const dockerWorkspacePreviewService = `import fcntl,json,os,subprocess,sys,time
` + dockerProcessStat + `
operation,name,command,supervise,stop=sys.argv[1:]
directory='/dev/shm/loopforge-preview'
os.makedirs(directory,mode=0o700,exist_ok=True)
path=os.path.join(directory,name+'.json')
guardian=path+'.guard'

def supervisor_status(record_path):
    try:
        with open(record_path) as f:
            record=json.load(f)
    except FileNotFoundError:
        return 'stopped'
    if record['status']=='done':
        return 'exited'
    identity=stat(record['pid'])
    return 'running' if identity is not None and identity[1]==record['start'] else 'stopped'

def status():
    current=supervisor_status(path)
    if current=='stopped':
        guard_status=supervisor_status(guardian)
        if guard_status=='running':
            return 'stopping'
        if guard_status=='exited':
            return 'exited'
    return current

def clear():
    for file in (path,path+'.stop',guardian,guardian+'.stop'):
        try:
            os.unlink(file)
        except FileNotFoundError:
            pass

def stop_service():
    for record_path in (guardian,path):
        if supervisor_status(record_path)=='running':
            result=subprocess.run([sys.executable,'-c',stop,record_path],
                capture_output=True,text=True,timeout=9)
            if result.returncode!=0 and supervisor_status(record_path)=='running':
                raise RuntimeError(result.stderr.strip() or '预览服务未停止')

def start():
    stop_service()
    clear()
    process=subprocess.Popen([sys.executable,'-c',supervise,guardian,
        sys.executable,'-c',supervise,path,'sh','-c',command],
        stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,
        start_new_session=True)
    deadline=time.monotonic()+5
    while not os.path.exists(path):
        if process.poll() is not None:
            raise RuntimeError('预览服务进程未启动')
        if time.monotonic()>=deadline:
            process.terminate()
            process.wait(timeout=7)
            clear()
            raise RuntimeError('预览服务启动超时')
        time.sleep(0.02)

with open(os.path.join(directory,'lock'),'a') as lock:
    fcntl.flock(lock,fcntl.LOCK_EX)
    if operation=='start' and supervisor_status(path)!='running':
        start()
    elif operation=='stop':
        stop_service()
        clear()
    print(json.dumps({'name':name,'status':status()}))
`
