// Package dockerproxy gives a conversation workspace Docker without handing it
// the daemon: a per-workspace unix socket that forwards the Docker Engine API
// to the real daemon, labels everything the workspace creates, only lets it
// touch objects carrying its own label, and refuses settings that escape to
// the host (privileged, host namespaces, devices, bind mounts outside the
// workspace).
package dockerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Label marks every container, network and volume a workspace creates.
const Label = "loopforge.workspace"

// PathMapping maps a path prefix the workspace sees (inside its container or
// bwrap view) onto the path the daemon resolves.
type PathMapping struct {
	From string
	To   string
}

type Config struct {
	// Upstream is the real daemon's unix socket.
	Upstream string
	// Socket is where the workspace connects.
	Socket string
	// Workspace is the label value identifying this workspace's objects.
	Workspace string
	// Paths rewrites bind sources before they reach the daemon.
	Paths []PathMapping
	// BindRoots are the daemon-side directories bind sources must stay in.
	BindRoots []string
}

type Proxy struct {
	cfg      Config
	listener net.Listener
	server   *http.Server
	client   *http.Client
	reverse  *httputil.ReverseProxy
	once     sync.Once
}

// DefaultUpstream is the daemon socket named by DOCKER_HOST, else the
// standard path.
func DefaultUpstream() string {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); strings.HasPrefix(host, "unix://") {
		return strings.TrimPrefix(host, "unix://")
	}
	return "/var/run/docker.sock"
}

// Start listens on cfg.Socket (replacing a stale socket file) and serves until
// Close.
func Start(cfg Config) (*Proxy, error) {
	if strings.TrimSpace(cfg.Workspace) == "" || strings.ContainsAny(cfg.Workspace, "=,\"\n") {
		return nil, fmt.Errorf("docker proxy needs a plain workspace id")
	}
	if cfg.Upstream == "" {
		cfg.Upstream = DefaultUpstream()
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Socket), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(cfg.Socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("docker proxy socket path %s is not a socket", cfg.Socket)
		}
		_ = os.Remove(cfg.Socket)
	}
	listener, err := net.Listen("unix", cfg.Socket)
	if err != nil {
		return nil, err
	}
	// Workspaces run as the platform user; nobody else gets in.
	if err := os.Chmod(cfg.Socket, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	upstream := cfg.Upstream
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", upstream)
		},
		MaxIdleConns:    8,
		IdleConnTimeout: 30 * time.Second,
	}
	target := &url.URL{Scheme: "http", Host: "docker"}
	p := &Proxy{cfg: cfg, listener: listener, client: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
	p.reverse = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.Host = "docker"
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			deny(w, http.StatusBadGateway, "Docker 守护进程不可用："+err.Error())
		},
	}
	p.server = &http.Server{Handler: http.HandlerFunc(p.serve), ReadHeaderTimeout: 30 * time.Second}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *Proxy) Socket() string { return p.cfg.Socket }

func (p *Proxy) Close() error {
	var err error
	p.once.Do(func() {
		err = p.server.Close()
		_ = os.Remove(p.cfg.Socket)
	})
	return err
}

// forbidden explains why the request is refused; the Docker CLI prints the
// message field.
type forbidden string

func (f forbidden) Error() string { return string(f) }

func deny(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "LoopForge 工作区 Docker 代理：" + message})
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+(\.[0-9]+)*(/.*)$`)

// route is the API path without its version prefix, split into segments.
func route(raw string) (string, []string) {
	clean := path.Clean("/" + raw)
	if m := versionPrefix.FindStringSubmatch(clean); m != nil {
		clean = m[2]
	}
	parts := strings.Split(strings.Trim(clean, "/"), "/")
	return clean, parts
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if err := p.authorize(r); err != nil {
		var f forbidden
		if errors.As(err, &f) {
			deny(w, http.StatusForbidden, f.Error())
			return
		}
		deny(w, http.StatusBadGateway, err.Error())
		return
	}
	p.reverse.ServeHTTP(w, r)
}

// authorize checks r and rewrites it in place (labels, filters, paths).
func (p *Proxy) authorize(r *http.Request) error {
	clean, parts := route(r.URL.Path)
	method := r.Method
	read := method == http.MethodGet || method == http.MethodHead
	switch parts[0] {
	case "_ping", "version":
		return nil
	case "info":
		if read {
			return nil
		}
	case "events":
		p.scopeList(r)
		return nil
	case "session", "grpc":
		// BuildKit's session and gRPC channels for docker build.
		return nil
	case "build":
		if len(parts) == 1 && method == http.MethodPost {
			q := r.URL.Query()
			switch mode := strings.ToLower(q.Get("networkmode")); mode {
			case "", "default", "bridge", "none":
			default:
				return forbidden("构建只能用默认网络或 none（--network " + mode + "）")
			}
			if q.Get("cgroupparent") != "" {
				return forbidden("构建不能指定 --cgroup-parent")
			}
			return nil
		}
		return forbidden("不支持 " + clean)
	case "images":
		return p.authorizeImage(r, parts)
	case "distribution":
		if read {
			return nil
		}
	case "auth":
		return nil
	case "containers":
		return p.authorizeContainer(r, parts)
	case "exec":
		if len(parts) >= 2 {
			return p.ownedExec(r.Context(), parts[1])
		}
	case "commit":
		if method == http.MethodPost {
			return p.owned(r.Context(), "containers", r.URL.Query().Get("container"))
		}
	case "networks":
		return p.authorizeNetwork(r, parts)
	case "volumes":
		return p.authorizeVolume(r, parts)
	}
	return forbidden(fmt.Sprintf("工作区不能调用 %s %s（只开放本工作区的容器、网络、卷，以及镜像拉取和构建）", method, clean))
}

func (p *Proxy) authorizeImage(r *http.Request, parts []string) error {
	switch {
	case len(parts) == 2 && parts[1] == "prune":
		return forbidden("不能清理宿主机上的全部镜像，请按名字删除自己构建或拉取的镜像")
	case len(parts) == 2 && (parts[1] == "create" || parts[1] == "load" || parts[1] == "json" || parts[1] == "search" || parts[1] == "get"):
		return nil
	case len(parts) >= 2:
		// inspect, history, tag, push, delete of a named image.
		return nil
	}
	return forbidden("不支持的镜像操作")
}

func (p *Proxy) authorizeContainer(r *http.Request, parts []string) error {
	method := r.Method
	switch {
	case len(parts) == 2 && parts[1] == "json":
		p.scopeList(r)
		return nil
	case len(parts) == 2 && parts[1] == "create" && method == http.MethodPost:
		return p.rewriteJSON(r, p.checkContainerCreate)
	case len(parts) == 2 && parts[1] == "prune":
		p.scopeList(r)
		return nil
	case len(parts) >= 2:
		if err := p.owned(r.Context(), "containers", parts[1]); err != nil {
			return err
		}
		if len(parts) == 3 && parts[2] == "exec" && method == http.MethodPost {
			return p.rewriteJSON(r, func(body map[string]any) error {
				if truthy(body["Privileged"]) {
					return forbidden("exec 不能使用 --privileged")
				}
				return nil
			})
		}
		if len(parts) == 3 && parts[2] == "update" {
			return p.rewriteJSON(r, func(body map[string]any) error {
				return checkResources(body)
			})
		}
		return nil
	}
	return forbidden("不支持的容器操作")
}

func (p *Proxy) authorizeNetwork(r *http.Request, parts []string) error {
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		p.scopeList(r)
		return nil
	case len(parts) == 2 && parts[1] == "create":
		return p.rewriteJSON(r, func(body map[string]any) error {
			if driver, _ := body["Driver"].(string); driver == "host" || driver == "macvlan" || driver == "ipvlan" {
				return forbidden("不能创建 " + driver + " 网络")
			}
			setLabel(body, "Labels", p.cfg.Workspace)
			return nil
		})
	case len(parts) == 2 && parts[1] == "prune":
		p.scopeList(r)
		return nil
	case len(parts) >= 2:
		if err := p.owned(r.Context(), "networks", parts[1]); err != nil {
			return err
		}
		if len(parts) == 3 && (parts[2] == "connect" || parts[2] == "disconnect") {
			return p.rewriteJSON(r, func(body map[string]any) error {
				id, _ := body["Container"].(string)
				return p.owned(r.Context(), "containers", id)
			})
		}
		return nil
	}
	return forbidden("不支持的网络操作")
}

func (p *Proxy) authorizeVolume(r *http.Request, parts []string) error {
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		p.scopeList(r)
		return nil
	case len(parts) == 2 && parts[1] == "create":
		return p.rewriteJSON(r, func(body map[string]any) error {
			if driver, _ := body["Driver"].(string); driver != "" && driver != "local" {
				return forbidden("只支持 local 卷")
			}
			if opts, _ := body["DriverOpts"].(map[string]any); len(opts) > 0 {
				return forbidden("卷不能带 driver 选项（可能把宿主目录挂进来）")
			}
			setLabel(body, "Labels", p.cfg.Workspace)
			return nil
		})
	case len(parts) == 2 && parts[1] == "prune":
		p.scopeList(r)
		return nil
	case len(parts) >= 2:
		return p.owned(r.Context(), "volumes", parts[1])
	}
	return forbidden("不支持的卷操作")
}

// scopeList narrows a list, prune or event stream to this workspace's label.
func (p *Proxy) scopeList(r *http.Request) {
	q := r.URL.Query()
	filters := map[string]any{}
	if raw := q.Get("filters"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &filters)
	}
	want := Label + "=" + p.cfg.Workspace
	switch labels := filters["label"].(type) {
	case map[string]any:
		for k := range labels {
			if k != want {
				delete(labels, k)
			}
		}
		labels[want] = true
	default:
		filters["label"] = map[string]any{want: true}
	}
	raw, _ := json.Marshal(filters)
	q.Set("filters", string(raw))
	r.URL.RawQuery = q.Encode()
}

// rewriteJSON decodes the request body, lets check validate and modify it,
// and puts the re-encoded body back.
func (p *Proxy) rewriteJSON(r *http.Request, check func(map[string]any) error) error {
	body := map[string]any{}
	if r.Body != nil {
		data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		_ = r.Body.Close()
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(data)) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				return forbidden("请求体不是 JSON 对象")
			}
		}
	}
	if err := check(body); err != nil {
		return err
	}
	data, _ := json.Marshal(body)
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	r.Header.Set("Content-Length", fmt.Sprint(len(data)))
	r.Header.Set("Content-Type", "application/json")
	return nil
}

func setLabel(body map[string]any, key, workspace string) {
	labels, _ := body[key].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
	}
	labels[Label] = workspace
	body[key] = labels
}

func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}

func nonEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	case bool:
		return t
	}
	return true
}

func checkResources(host map[string]any) error {
	for _, key := range []string{"Devices", "DeviceCgroupRules", "DeviceRequests"} {
		if nonEmpty(host[key]) {
			return forbidden("不能把宿主设备交给容器（" + key + "）")
		}
	}
	return nil
}

func (p *Proxy) checkContainerCreate(body map[string]any) error {
	setLabel(body, "Labels", p.cfg.Workspace)
	if networking, _ := body["NetworkingConfig"].(map[string]any); networking != nil {
		endpoints, _ := networking["EndpointsConfig"].(map[string]any)
		for name := range endpoints {
			if name == "bridge" || name == "none" || name == "default" {
				continue
			}
			if name == "host" {
				return forbidden("不能使用宿主网络（--network host）")
			}
			if err := p.owned(context.Background(), "networks", name); err != nil {
				return err
			}
		}
	}
	host, _ := body["HostConfig"].(map[string]any)
	if host == nil {
		return nil
	}
	if truthy(host["Privileged"]) {
		return forbidden("不能使用 --privileged")
	}
	if nonEmpty(host["CapAdd"]) {
		return forbidden("不能增加 capabilities（--cap-add）")
	}
	if nonEmpty(host["CgroupParent"]) {
		return forbidden("不能指定 --cgroup-parent")
	}
	// Non-null path lists replace Docker's masking of /proc and /sys
	// (--security-opt systempaths=unconfined).
	for _, key := range []string{"MaskedPaths", "ReadonlyPaths"} {
		if v, ok := host[key]; ok && v != nil {
			return forbidden("不能修改容器的 /proc、/sys 屏蔽路径（" + key + "）")
		}
	}
	if driver, _ := host["VolumeDriver"].(string); driver != "" && driver != "local" {
		return forbidden("只支持 local 卷驱动")
	}
	if runtime, _ := host["Runtime"].(string); runtime != "" && runtime != "runc" {
		return forbidden("不能指定容器运行时（--runtime " + runtime + "）")
	}
	if err := checkResources(host); err != nil {
		return err
	}
	if opts, _ := host["SecurityOpt"].([]any); len(opts) > 0 {
		for _, item := range opts {
			opt, _ := item.(string)
			if !strings.HasPrefix(opt, "no-new-privileges") {
				return forbidden("不能修改安全选项（--security-opt " + opt + "）")
			}
		}
	}
	for _, key := range []string{"PidMode", "IpcMode", "UTSMode", "UsernsMode", "CgroupnsMode", "NetworkMode"} {
		mode, _ := host[key].(string)
		switch {
		case mode == "host":
			return forbidden("不能使用宿主命名空间（" + key + "=host）")
		case strings.HasPrefix(mode, "container:"):
			if err := p.owned(context.Background(), "containers", strings.TrimPrefix(mode, "container:")); err != nil {
				return err
			}
		case key == "NetworkMode" && mode != "" && mode != "default" && mode != "bridge" && mode != "none":
			if err := p.owned(context.Background(), "networks", mode); err != nil {
				return err
			}
		}
	}
	if from, _ := host["VolumesFrom"].([]any); len(from) > 0 {
		for _, item := range from {
			name, _, _ := strings.Cut(fmt.Sprint(item), ":")
			if err := p.owned(context.Background(), "containers", name); err != nil {
				return err
			}
		}
	}
	if binds, _ := host["Binds"].([]any); len(binds) > 0 {
		out := make([]any, 0, len(binds))
		for _, item := range binds {
			spec, _ := item.(string)
			rewritten, err := p.checkBind(spec)
			if err != nil {
				return err
			}
			out = append(out, rewritten)
		}
		host["Binds"] = out
	}
	if mounts, _ := host["Mounts"].([]any); len(mounts) > 0 {
		for _, item := range mounts {
			mount, _ := item.(map[string]any)
			if err := p.checkMount(mount); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkBind validates "source:target[:options]"; an absolute source is a host
// path, anything else a named volume.
func (p *Proxy) checkBind(spec string) (string, error) {
	source, rest, ok := strings.Cut(spec, ":")
	if !ok {
		return spec, nil // anonymous volume
	}
	if strings.HasPrefix(source, "/") {
		mapped, err := p.hostPath(source)
		if err != nil {
			return "", err
		}
		return mapped + ":" + rest, nil
	}
	return spec, p.ensureVolume(source)
}

func (p *Proxy) checkMount(mount map[string]any) error {
	kind, _ := mount["Type"].(string)
	source, _ := mount["Source"].(string)
	switch kind {
	case "bind":
		mapped, err := p.hostPath(source)
		if err != nil {
			return err
		}
		mount["Source"] = mapped
	case "volume":
		if opts, _ := mount["VolumeOptions"].(map[string]any); opts != nil {
			if driver, _ := opts["DriverConfig"].(map[string]any); nonEmpty(driver) {
				return forbidden("卷挂载不能带 driver 配置")
			}
		}
		if source != "" {
			return p.ensureVolume(source)
		}
	case "tmpfs", "":
	default:
		return forbidden("不支持 " + kind + " 挂载")
	}
	return nil
}

// hostPath maps a bind source the workspace sees onto the daemon path and
// keeps it inside the workspace.
func (p *Proxy) hostPath(source string) (string, error) {
	clean := path.Clean(source)
	for _, m := range p.cfg.Paths {
		from := path.Clean(m.From)
		if clean == from || strings.HasPrefix(clean, from+"/") {
			clean = path.Join(m.To, strings.TrimPrefix(clean, from))
			break
		}
	}
	for _, root := range p.cfg.BindRoots {
		root = path.Clean(root)
		if !within(clean, root) {
			continue
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", forbidden("工作区目录不可用：" + root)
		}
		if !within(resolveExisting(clean), realRoot) {
			return "", forbidden("挂载路径经软链指到了工作区之外：" + source)
		}
		return clean, nil
	}
	return "", forbidden("只能把工作区里的目录挂进容器，" + source + " 不在工作区内")
}

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// resolveExisting resolves symlinks in the longest existing prefix of p, so a
// not-yet-created directory under a symlink is judged by where it would land.
func resolveExisting(p string) string {
	rest := ""
	for cur := p; ; {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// ensureVolume allows a named volume this workspace owns, creating it with
// the workspace label when it does not exist yet.
func (p *Proxy) ensureVolume(name string) error {
	labels, found, err := p.labels(context.Background(), "volumes", name)
	if err != nil {
		return err
	}
	if found {
		if labels[Label] != p.cfg.Workspace {
			return forbidden("卷 " + name + " 不属于本工作区")
		}
		return nil
	}
	body, _ := json.Marshal(map[string]any{"Name": name, "Labels": map[string]string{Label: p.cfg.Workspace}})
	resp, err := p.client.Post("http://docker/volumes/create", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("创建卷 %s 失败：%s", name, strings.TrimSpace(string(data)))
	}
	return nil
}

// owned refuses objects without this workspace's label.
func (p *Proxy) owned(ctx context.Context, kind, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return forbidden("缺少对象 id")
	}
	labels, found, err := p.labels(ctx, kind, id)
	if err != nil {
		return err
	}
	if !found {
		// Let the daemon answer "no such object" itself.
		return nil
	}
	if labels[Label] != p.cfg.Workspace {
		return forbidden(fmt.Sprintf("%s 不属于本工作区，只能操作本工作区创建的对象", id))
	}
	return nil
}

func (p *Proxy) ownedExec(ctx context.Context, id string) error {
	var exec struct{ ContainerID string }
	found, err := p.inspect(ctx, "/exec/"+url.PathEscape(id)+"/json", &exec)
	if err != nil || !found {
		return err
	}
	return p.owned(ctx, "containers", exec.ContainerID)
}

func (p *Proxy) labels(ctx context.Context, kind, id string) (map[string]string, bool, error) {
	var obj struct {
		Labels map[string]string
		Config struct{ Labels map[string]string }
	}
	found, err := p.inspect(ctx, "/"+kind+"/"+url.PathEscape(id)+map[string]string{"containers": "/json"}[kind], &obj)
	if err != nil || !found {
		return nil, found, err
	}
	if kind == "containers" {
		return obj.Config.Labels, true, nil
	}
	return obj.Labels, true, nil
}

func (p *Proxy) inspect(ctx context.Context, apiPath string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+apiPath, nil)
	if err != nil {
		return false, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("docker inspect %s: HTTP %d %s", apiPath, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return true, json.NewDecoder(resp.Body).Decode(out)
}

// StopContainers stops this workspace's running containers (they stay, with
// their data, for "docker start" in a later turn). It talks to the daemon
// directly, so it works after Close.
func (p *Proxy) StopContainers(ctx context.Context) error {
	filters, _ := json.Marshal(map[string]any{"label": map[string]bool{Label + "=" + p.cfg.Workspace: true}})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?filters="+url.QueryEscape(string(filters)), nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	var list []struct {
		ID string `json:"Id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if err != nil {
		return err
	}
	var first error
	for _, c := range list {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/containers/"+url.PathEscape(c.ID)+"/stop?t=5", nil)
		resp, err := p.client.Do(req)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		resp.Body.Close()
	}
	return first
}
