package runtime

func dockerConversationProcessArgv(file string, argv []string) []string {
	return withToolchainPath(append([]string{"python3", "-c", dockerConversationProcessSupervisor, file}, argv...))
}

func dockerConversationLeasedProcessArgv(file, lease string, argv []string) []string {
	return dockerWindowsPythonArgv(append([]string{dockerConversationProcessSupervisor, file, "--owner-lease", lease}, argv...))
}

func dockerConversationLeasedStopArgv(file string) []string {
	return dockerWindowsPythonArgv([]string{dockerConversationProcessStop, file})
}

func dockerConversationStopArgv(file string) []string {
	return withToolchainPath([]string{"python3", "-c", dockerConversationProcessStop, file})
}

const dockerWindowsPythonRequired = "Windows Docker execution requires Python 3 in the container image at /usr/bin/python3 or /usr/local/bin/python3; install python3 and retry."

// Supervisors run in the canonical capability-restricted container. Scripts
// reach this only after scriptview drops its bootstrap capabilities. Isolated
// Python and absolute image interpreters reject workspace module/PATH shims.
func dockerWindowsPythonArgv(arguments []string) []string {
	launch := `for python in /usr/bin/python3 /usr/local/bin/python3; do
  if [ -x "$python" ]; then exec "$python" -I -c "$@"; fi
done
printf '%s\n' '` + dockerWindowsPythonRequired + `' >&2
exit 127`
	return append([]string{"/bin/sh", "-c", launch, "loopforge-process-supervisor"}, arguments...)
}

func dockerScriptProcessArgv(file, lease string, argv []string) []string {
	return dockerWindowsPythonArgv(append([]string{dockerConversationProcessSupervisor, file, "--retain-successful-background-services", "--owner-lease", lease}, argv...))
}

func dockerScriptStopArgv(file string) []string {
	return dockerWindowsPythonArgv([]string{dockerConversationProcessStop, file})
}

// A CLI may create new sessions for its native shell tools. Killing only its
// process group leaves those tools running. A Linux subreaper owns even orphaned
// and double-forked descendants without affecting other commands in the same
// workspace container. It preserves stdin and waits for cleanup before exit.
const dockerConversationProcessSupervisor = `import ctypes,json,os,signal,subprocess,sys,time
path=sys.argv[1]
keep_success=len(sys.argv)>2 and sys.argv[2]=='--retain-successful-background-services'
argv=sys.argv[3:] if keep_success else sys.argv[2:]
owner_lease=argv[1] if len(argv)>1 and argv[0]=='--owner-lease' else ''
if owner_lease: argv=argv[2:]
stopped=path+'.stop'
cancelled=False
def cancel(signum,frame):
    global cancelled
    cancelled=True
for sig in (signal.SIGTERM,signal.SIGINT,signal.SIGHUP):
    signal.signal(sig,cancel)
def stat(pid):
    try:
        with open('/proc/'+str(pid)+'/stat') as f:
            fields=f.read().rsplit(')',1)[1].split()
        return int(fields[1]),fields[19]
    except (OSError,ValueError,IndexError):
        return None
me=os.getpid()
identity=stat(me)
if identity is None or ctypes.CDLL(None,use_errno=True).prctl(36,1,0,0,0)!=0:
    sys.exit('native CLI requires Linux child subreaper support')

def lease_alive():
    if not owner_lease: return True
    try:
        with open(owner_lease) as f: lease=int(f.read().strip())
        with open('/proc/uptime') as f: now=int(f.read().split('.')[0])
        return 0<=now-lease<=6
    except (OSError,ValueError):
        return False
def state(status):
    temp=path+'.tmp'
    fd=os.open(temp,os.O_WRONLY|os.O_CREAT|os.O_TRUNC|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'w') as f:
        json.dump({'pid':me,'start':identity[1],'status':status},f)
    os.replace(temp,path)
def descendants():
    rows={}
    for name in os.listdir('/proc'):
        if name.isdigit():
            entry=stat(int(name))
            if entry is not None:
                rows[int(name)]=entry
    owned={me}
    while True:
        more={pid for pid,(parent,start) in rows.items() if parent in owned}
        if more.issubset(owned):
            break
        owned.update(more)
    return {pid:rows[pid][1] for pid in owned if pid!=me}
def send(pid,start,sig):
    entry=stat(pid)
    if entry is not None and entry[1]==start:
        try:
            os.kill(pid,sig)
        except ProcessLookupError:
            pass
def cleanup(child):
    # Freeze parents before killing them, then rescan for children created
    # during that snapshot. Subreaper adoption retains every owned process.
    frozen={}
    while True:
        current=descendants()
        for pid,start in current.items():
            send(pid,start,signal.SIGSTOP)
        if current.items()<=frozen.items():
            break
        frozen.update(current)
    for pid,start in frozen.items():
        send(pid,start,signal.SIGKILL)
    code=child.wait(timeout=5) if child is not None else 0
    deadline=time.monotonic()+5
    while True:
        try:
            pid,_=os.waitpid(-1,os.WNOHANG)
        except ChildProcessError:
            break
        if pid==0:
            if time.monotonic()>deadline:
                raise RuntimeError('native CLI descendants did not exit')
            time.sleep(0.01)
    return code
child=None
code=1
try:
    state('running')
    # The stop marker also fences cancellation before docker exec reaches
    # this wrapper. A late launch cannot begin a new CLI after stop returned.
    if not lease_alive(): cancelled=True
    if not cancelled and not os.path.exists(stopped):
        child=subprocess.Popen(argv,start_new_session=True)
        while child.poll() is None and not cancelled and not os.path.exists(stopped):
            if not lease_alive(): cancelled=True; break
            time.sleep(0.05)
finally:
    # Trusted scripts intentionally keep successful background services warm.
    # Cancellation and every failure still revoke the complete owned tree.
    if not lease_alive(): cancelled=True
    if keep_success and child is not None and child.poll()==0 and not cancelled and not os.path.exists(stopped):
        code=0
    else:
        code=cleanup(child)
    state('done')
sys.exit(143 if cancelled or os.path.exists(stopped) else (code if code>=0 else 128-code))
`

// A PID's Linux start time prevents a stale control file from signalling a
// reused PID. A stop marker is retained if launch has not reached the container.
const dockerConversationProcessStop = `import json,os,signal,sys,time
path=sys.argv[1]
stopped=path+'.stop'
try:
    fd=os.open(stopped,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    os.close(fd)
except FileExistsError:
    pass
deadline=time.monotonic()+7
while True:
    try:
        with open(path) as f:
            state=json.load(f)
    except FileNotFoundError:
        break
    if state['status']=='done':
        os.unlink(path)
        os.unlink(stopped)
        break
    try:
        with open('/proc/'+str(state['pid'])+'/stat') as f:
            start=f.read().rsplit(')',1)[1].split()[19]
    except FileNotFoundError:
        sys.exit('native CLI supervisor exited without cleanup')
    if start!=state['start']:
        sys.exit('native CLI supervisor identity changed')
    try:
        os.kill(state['pid'],signal.SIGTERM)
    except ProcessLookupError:
        pass
    if time.monotonic()>deadline:
        sys.exit('native CLI supervisor did not stop')
    time.sleep(0.05)
`
