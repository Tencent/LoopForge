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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
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

	versionMu    sync.Mutex
	versionKnown bool
	subpaths     bool
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
	if _, parts := route(r.URL.Path); parts[0] == "grpc" {
		p.serveGRPC(w, r)
		return
	}
	p.reverse.ServeHTTP(w, r)
}

// authorize checks r and rewrites it in place (labels, filters, paths).
func (p *Proxy) authorize(r *http.Request) error {
	clean, parts := route(r.URL.Path)
	method := r.Method
	read := method == http.MethodGet || method == http.MethodHead
	// The daemon reads parameters with r.FormValue, where a form-encoded body
	// overrides the query the proxy checked. Docker clients never send one.
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && (media == "application/x-www-form-urlencoded" || media == "multipart/form-data") {
		return forbidden("请求体不能是表单（" + media + "），参数请放在查询串里")
	}
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
	case "session":
		// File sync for a /build the proxy has already checked.
		return nil
	case "grpc":
		// Upgraded and checked call by call in serveGRPC.
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
			for _, ref := range q["t"] {
				if err := p.mayWriteTag(r.Context(), ref); err != nil {
					return err
				}
			}
			// Built images carry the workspace label, which is what lets the
			// workspace delete or retag them later.
			labels := map[string]string{}
			if raw := q.Get("labels"); raw != "" {
				_ = json.Unmarshal([]byte(raw), &labels)
			}
			labels[Label] = p.cfg.Workspace
			encoded, _ := json.Marshal(labels)
			q.Set("labels", string(encoded))
			r.URL.RawQuery = q.Encode()
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
			q := r.URL.Query()
			if err := p.owned(r.Context(), "containers", q.Get("container")); err != nil {
				return err
			}
			if repo := q.Get("repo"); repo != "" {
				if err := p.mayWriteTag(r.Context(), imageRef(repo, q.Get("tag"))); err != nil {
					return err
				}
			}
			return p.rewriteJSON(r, func(body map[string]any) error {
				setLabel(body, "Labels", p.cfg.Workspace)
				return nil
			})
		}
	case "networks":
		return p.authorizeNetwork(r, parts)
	case "volumes":
		return p.authorizeVolume(r, parts)
	}
	return forbidden(fmt.Sprintf("工作区不能调用 %s %s（只开放本工作区的容器、网络、卷，以及镜像拉取和构建）", method, clean))
}

// Images are shared by every workspace on the daemon. A workspace may pull
// and read any image, but may only delete or overwrite the name of an image
// it built or committed itself (labelled), so it cannot break or poison
// another workspace's images. Pulling a public tag again is always allowed.
func (p *Proxy) authorizeImage(r *http.Request, parts []string) error {
	ctx := r.Context()
	q := r.URL.Query()
	switch {
	case len(parts) == 2 && parts[1] == "prune":
		return forbidden("不能清理宿主机上的全部镜像，请按名字删除本工作区构建的镜像")
	case len(parts) == 2 && parts[1] == "load":
		return forbidden("不支持 docker load（会覆盖其他工作区在用的镜像名），请用 docker build 或 docker pull")
	case len(parts) == 2 && parts[1] == "create":
		if q.Get("fromSrc") != "" && q.Get("repo") != "" {
			return p.mayWriteTag(ctx, imageRef(q.Get("repo"), q.Get("tag")))
		}
		if from := q.Get("fromImage"); from != "" && q.Get("fromSrc") == "" {
			return p.mayPull(ctx, pullRef(from, q.Get("tag")))
		}
		return nil
	case len(parts) == 2 && (parts[1] == "json" || parts[1] == "search" || parts[1] == "get"):
		return nil
	case len(parts) >= 2 && r.Method == http.MethodDelete:
		return p.ownedImage(ctx, strings.Join(parts[1:], "/"), "删除")
	case len(parts) >= 3 && parts[len(parts)-1] == "tag" && r.Method == http.MethodPost:
		return p.mayWriteTag(ctx, imageRef(q.Get("repo"), q.Get("tag")))
	case len(parts) >= 2:
		// inspect, history, push of a named image.
		return nil
	}
	return forbidden("不支持的镜像操作")
}

func imageRef(repo, tag string) string {
	if tag == "" {
		return repo
	}
	return repo + ":" + tag
}

// mayWriteTag allows naming an image ref unless the name already belongs to
// an image this workspace did not build.
func (p *Proxy) mayWriteTag(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	return p.ownedImage(ctx, ref, "覆盖镜像名")
}

// mayPull lets a pull refresh a public name or this workspace's own image,
// but not replace an image another workspace built under the same name
// (pulling "myapp:dev" from a registry would swap it under that workspace).
func (p *Proxy) mayPull(ctx context.Context, ref string) error {
	var image struct {
		Config struct{ Labels map[string]string }
	}
	found, err := p.inspect(ctx, "/images/"+escapeImageRef(ref)+"/json", &image)
	if err != nil || !found {
		return err
	}
	if owner := image.Config.Labels[Label]; owner != "" && owner != p.cfg.Workspace {
		return forbidden("镜像 " + ref + " 是其他工作区构建的，拉取会把它换掉；换一个镜像名")
	}
	return nil
}

// pullRef is the local name a pull writes: fromImage with the tag (or
// digest) parameter, defaulting to latest.
func pullRef(from, tag string) string {
	switch {
	case strings.HasPrefix(tag, "sha256:"):
		return from + "@" + tag
	case tag != "":
		return from + ":" + tag
	}
	if i := strings.LastIndex(from, "/"); strings.Contains(from[i+1:], ":") || strings.Contains(from, "@") {
		return from
	}
	return from + ":latest"
}

func (p *Proxy) ownedImage(ctx context.Context, ref, action string) error {
	var image struct {
		Config struct{ Labels map[string]string }
	}
	found, err := p.inspect(ctx, "/images/"+escapeImageRef(ref)+"/json", &image)
	if err != nil || !found {
		return err
	}
	if image.Config.Labels[Label] != p.cfg.Workspace {
		return forbidden(fmt.Sprintf("镜像 %s 不是本工作区构建的，不能%s（拉取的公共镜像由平台回收，换一个镜像名即可）", ref, action))
	}
	return nil
}

func escapeImageRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
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
			if driver, _ := body["Driver"].(string); driver != "" && driver != "bridge" {
				return forbidden("只能创建 bridge 网络（--driver " + driver + "）")
			}
			// Driver options can name or reuse host interfaces; custom subnets
			// can shadow host routes (169.254.169.254, the host's LAN).
			if nonEmpty(body["Options"]) {
				return forbidden("网络不能带 driver 选项（--opt）")
			}
			if ipam, _ := body["IPAM"].(map[string]any); ipam != nil {
				if driver, _ := ipam["Driver"].(string); driver != "" && driver != "default" {
					return forbidden("网络只能用默认 IPAM")
				}
				if nonEmpty(ipam["Options"]) || nonEmpty(ipam["Config"]) {
					return forbidden("网络不能自定义子网、网关或 IPAM 选项，由 Docker 从默认地址池分配")
				}
			}
			if nonEmpty(body["ConfigFrom"]) || truthy(body["ConfigOnly"]) {
				return forbidden("不支持 --config-from / --config-only 网络")
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
			if name, _ := body["Name"].(string); strings.HasPrefix(name, bindVolumePrefix) {
				return forbidden(bindVolumePrefix + " 开头的卷名留给平台使用")
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
	if err := canonicalizeKeys(body); err != nil {
		return err
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
		for _, item := range endpoints {
			endpoint, _ := item.(map[string]any)
			if err := p.checkLinks(endpoint["Links"]); err != nil {
				return err
			}
		}
	}
	host, _ := body["HostConfig"].(map[string]any)
	if host == nil {
		return nil
	}
	if err := p.checkLinks(host["Links"]); err != nil {
		return err
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
	mounts, _ := host["Mounts"].([]any)
	for _, item := range mounts {
		mount, _ := item.(map[string]any)
		if err := p.checkMount(mount); err != nil {
			return err
		}
	}
	if binds, _ := host["Binds"].([]any); len(binds) > 0 {
		out := make([]any, 0, len(binds))
		for _, item := range binds {
			spec, _ := item.(string)
			rewritten, mount, err := p.checkBind(spec)
			if err != nil {
				return err
			}
			if mount != nil {
				mounts = append(mounts, mount)
				continue
			}
			out = append(out, rewritten)
		}
		host["Binds"] = out
	}
	if len(mounts) > 0 {
		host["Mounts"] = mounts
	}
	return nil
}

// checkLinks lets legacy links ("name:alias") reach only this workspace's
// containers: a link injects the target's environment into the new one.
func (p *Proxy) checkLinks(v any) error {
	links, _ := v.([]any)
	for _, item := range links {
		link, _ := item.(string)
		name, _, _ := strings.Cut(link, ":")
		if err := p.owned(context.Background(), "containers", strings.TrimPrefix(name, "/")); err != nil {
			return err
		}
	}
	return nil
}

// checkBind validates "source:target[:options]"; an absolute source is a host
// path, anything else a named volume.
// A path below a workspace root comes back as a volume-subpath mount that
// replaces the bind (see subpathMount).
func (p *Proxy) checkBind(spec string) (string, map[string]any, error) {
	source, rest, ok := strings.Cut(spec, ":")
	if !ok {
		return spec, nil, nil // anonymous volume
	}
	if !strings.HasPrefix(source, "/") {
		return spec, nil, p.ensureVolume(source)
	}
	resolved, root, err := p.hostPath(source)
	if err != nil {
		return "", nil, err
	}
	target, options, _ := strings.Cut(rest, ":")
	readOnly := false
	for _, opt := range strings.Split(options, ",") {
		switch opt {
		case "ro", "readonly":
			readOnly = true
		case "", "rw", "z", "Z", "nocopy", "consistent", "cached", "delegated",
			"private", "rprivate", "shared", "rshared", "slave", "rslave":
			// Propagation and SELinux relabelling are dropped: a workspace
			// mount must not propagate mounts back to the host.
		default:
			return "", nil, forbidden("不支持的挂载选项 " + opt)
		}
	}
	// Plain binds create a missing source directory; keep that behaviour.
	if _, statErr := os.Stat(resolved); os.IsNotExist(statErr) {
		_ = os.MkdirAll(resolved, 0o755)
	}
	mount, err := p.subpathMount(resolved, root, target, readOnly)
	if err != nil || mount != nil {
		return "", mount, err
	}
	kept := resolved + ":" + target
	if readOnly {
		kept += ":ro"
	}
	return kept, nil, nil
}

func (p *Proxy) checkMount(mount map[string]any) error {
	kind, _ := mount["Type"].(string)
	source, _ := mount["Source"].(string)
	switch kind {
	case "bind":
		resolved, root, err := p.hostPath(source)
		if err != nil {
			return err
		}
		target, _ := mount["Target"].(string)
		readOnly, _ := mount["ReadOnly"].(bool)
		converted, err := p.subpathMount(resolved, root, target, readOnly)
		if err != nil {
			return err
		}
		if converted != nil {
			for k := range mount {
				delete(mount, k)
			}
			for k, v := range converted {
				mount[k] = v
			}
			return nil
		}
		mount["Source"] = resolved
		if opts, _ := mount["BindOptions"].(map[string]any); opts != nil {
			delete(opts, "Propagation") // default rprivate: nothing propagates back
		}
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
// keeps it inside the workspace. It returns the resolved path and the
// resolved root it lies in.
func (p *Proxy) hostPath(source string) (string, string, error) {
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
			return "", "", forbidden("工作区目录不可用：" + root)
		}
		resolved := resolveExisting(clean)
		if !within(resolved, realRoot) {
			return "", "", forbidden("挂载路径经软链指到了工作区之外：" + source)
		}
		return resolved, realRoot, nil
	}
	return "", "", forbidden("只能把工作区里的目录挂进容器，" + source + " 不在工作区内")
}

// bindVolumePrefix names the proxy's own volumes backed by a workspace root.
const bindVolumePrefix = "lf-bind-"

// subpathMount closes the race between checking a bind source and the daemon
// mounting it: the agent owns everything below a workspace root and could
// swap a checked directory for a symlink in between. The root itself is a
// mount point the agent cannot replace, so it stays a bind. A path below it
// becomes a subpath of a local volume bound to the root; the daemon opens
// volume subpaths without following symlinks out of the volume. Daemons
// before API 1.45 (Docker 26) have no subpaths and keep the checked bind.
func (p *Proxy) subpathMount(resolved, root, target string, readOnly bool) (map[string]any, error) {
	if resolved == root {
		return nil, nil
	}
	if supported, err := p.supportsSubpath(); err != nil || !supported {
		return nil, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, forbidden("挂载路径不在工作区内：" + resolved)
	}
	sum := sha256.Sum256([]byte(root))
	name := bindVolumePrefix + p.cfg.Workspace + "-" + hex.EncodeToString(sum[:6])
	if err := p.ensureBindVolume(name, root); err != nil {
		return nil, err
	}
	return map[string]any{
		"Type": "volume", "Source": name, "Target": target, "ReadOnly": readOnly,
		"VolumeOptions": map[string]any{"NoCopy": true, "Subpath": filepath.ToSlash(rel)},
	}, nil
}

func (p *Proxy) ensureBindVolume(name, root string) error {
	var volume struct {
		Labels  map[string]string
		Options map[string]string
	}
	found, err := p.inspect(context.Background(), "/volumes/"+url.PathEscape(name), &volume)
	if err != nil {
		return err
	}
	if found {
		if volume.Labels[Label] != p.cfg.Workspace || volume.Options["device"] != root || volume.Options["o"] != "bind" {
			return forbidden("卷 " + name + " 被占用，删除后重试")
		}
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"Name": name, "Driver": "local",
		"DriverOpts": map[string]string{"type": "none", "o": "bind", "device": root},
		"Labels":     map[string]string{Label: p.cfg.Workspace},
	})
	resp, err := p.client.Post("http://docker/volumes/create", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("创建工作区卷 %s 失败：%s", name, strings.TrimSpace(string(data)))
	}
	return nil
}

// supportsSubpath reports whether the daemon speaks API 1.45 or later.
func (p *Proxy) supportsSubpath() (bool, error) {
	p.versionMu.Lock()
	defer p.versionMu.Unlock()
	if p.versionKnown {
		return p.subpaths, nil
	}
	var version struct {
		APIVersion string `json:"ApiVersion"`
	}
	// An unknown version refuses the bind (retried next time) rather than
	// falling back to a plain bind for the proxy's lifetime.
	if found, err := p.inspect(context.Background(), "/version", &version); err != nil || !found {
		return false, fmt.Errorf("无法确认 Docker 版本，稍后重试：%v", err)
	}
	var major, minor int
	if _, err := fmt.Sscanf(version.APIVersion, "%d.%d", &major, &minor); err != nil {
		return false, fmt.Errorf("无法识别 Docker API 版本 %q", version.APIVersion)
	}
	p.subpaths = major > 1 || major == 1 && minor >= 45
	p.versionKnown = true
	return p.subpaths, nil
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

// Cleanup removes every container (with its anonymous volumes) and network
// labelled for workspace on the daemon at upstream, and with volumes its
// named volumes too. It runs when the workspace directory itself is deleted,
// so nothing the workspace started outlives it.
func Cleanup(ctx context.Context, upstream, workspace string, volumes bool) error {
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("docker cleanup needs a workspace id")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", upstream)
	}}}
	filters, _ := json.Marshal(map[string]any{"label": map[string]bool{Label + "=" + workspace: true}})
	query := "filters=" + url.QueryEscape(string(filters))
	call := func(method, apiPath string, out any) error {
		req, err := http.NewRequestWithContext(ctx, method, "http://docker"+apiPath, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil
		}
		if resp.StatusCode/100 != 2 {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return fmt.Errorf("%s %s: HTTP %d %s", method, apiPath, resp.StatusCode, strings.TrimSpace(string(data)))
		}
		if out != nil {
			return json.NewDecoder(resp.Body).Decode(out)
		}
		return nil
	}
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	var containers []struct {
		ID string `json:"Id"`
	}
	keep(call(http.MethodGet, "/containers/json?all=1&"+query, &containers))
	for _, c := range containers {
		keep(call(http.MethodDelete, "/containers/"+url.PathEscape(c.ID)+"?force=1&v=1", nil))
	}
	var networks []struct {
		ID string `json:"Id"`
	}
	keep(call(http.MethodGet, "/networks?"+query, &networks))
	for _, n := range networks {
		keep(call(http.MethodDelete, "/networks/"+url.PathEscape(n.ID), nil))
	}
	if !volumes {
		return first
	}
	var list struct{ Volumes []struct{ Name string } }
	keep(call(http.MethodGet, "/volumes?"+query, &list))
	for _, v := range list.Volumes {
		keep(call(http.MethodDelete, "/volumes/"+url.PathEscape(v.Name)+"?force=1", nil))
	}
	return first
}
