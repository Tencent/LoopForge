package dockerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type seen struct {
	Method, Path, Query string
	Body                map[string]any
}

// fakeDaemon answers inspect calls from a label table and records everything
// else.
type fakeDaemon struct {
	mu       sync.Mutex
	requests []seen
	labels   map[string]map[string]string // "containers/x" -> labels
	execs    map[string]string            // exec id -> container id
	api      string                       // ApiVersion reported by /version
	options  map[string]map[string]string // "volumes/x" -> driver options
}

func (d *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path, _ := route(r.URL.Path)
	if r.Method == http.MethodGet && path == "/version" {
		_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": d.api})
		return
	}
	if strings.HasSuffix(path, "/attach") && r.Header.Get("Upgrade") == "tcp" {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
		_ = rw.Flush()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(rw, buf); err == nil {
			_, _ = conn.Write(bytes.ToUpper(buf))
		}
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if r.Method == http.MethodGet && parts[0] == "images" && len(parts) >= 3 && parts[len(parts)-1] == "json" {
		ref := strings.Join(parts[1:len(parts)-1], "/")
		if labels, ok := d.labels["images/"+ref]; ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Labels": labels}})
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && len(parts) >= 2 {
		key := parts[0] + "/" + parts[1]
		switch {
		case parts[0] == "exec" && len(parts) == 3:
			if c, ok := d.execs[parts[1]]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"ContainerID": c})
				return
			}
			http.NotFound(w, r)
			return
		case parts[0] == "containers" && len(parts) == 3 && parts[2] == "json":
			if labels, ok := d.labels[key]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"Config": map[string]any{"Labels": labels}})
				return
			}
			http.NotFound(w, r)
			return
		case (parts[0] == "volumes" || parts[0] == "networks") && len(parts) == 2:
			if labels, ok := d.labels[key]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"Labels": labels, "Options": d.options[key]})
				return
			}
			http.NotFound(w, r)
			return
		}
	}
	var body map[string]any
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	d.mu.Lock()
	d.requests = append(d.requests, seen{r.Method, path, r.URL.RawQuery, body})
	if path == "/volumes/create" {
		name, _ := body["Name"].(string)
		labels := map[string]string{}
		for k, v := range body["Labels"].(map[string]any) {
			labels[k] = v.(string)
		}
		d.labels["volumes/"+name] = labels
		if opts, ok := body["DriverOpts"].(map[string]any); ok {
			if d.options == nil {
				d.options = map[string]map[string]string{}
			}
			d.options["volumes/"+name] = map[string]string{}
			for k, v := range opts {
				d.options["volumes/"+name][k] = v.(string)
			}
		}
	}
	d.mu.Unlock()
	_, _ = w.Write([]byte(`{}`))
}

func (d *fakeDaemon) last(t *testing.T) seen {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		t.Fatal("daemon saw no request")
	}
	return d.requests[len(d.requests)-1]
}

func (d *fakeDaemon) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

type harness struct {
	daemon    *fakeDaemon
	client    *http.Client
	workspace string
	root      string
}

func start(t *testing.T) *harness { return startAPI(t, "") }

func startAPI(t *testing.T, api string) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dproxy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	daemon := &fakeDaemon{labels: map[string]map[string]string{
		"containers/mine":            {Label: "ws1"},
		"containers/theirs":          {Label: "ws2"},
		"containers/bare":            {},
		"networks/mynet":             {Label: "ws1"},
		"networks/othernet":          {Label: "ws2"},
		"volumes/myvol":              {Label: "ws1"},
		"volumes/othervol":           {Label: "ws2"},
		"images/myapp:dev":           {Label: "ws1"},
		"images/team/other:1":        {Label: "ws2"},
		"images/alpine:3":            {},
		"images/registry.io/x/y:2.0": {},
	}, execs: map[string]string{"e-mine": "mine", "e-theirs": "theirs"}, api: api}
	upstream := filepath.Join(dir, "daemon.sock")
	listener, err := net.Listen("unix", upstream)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: daemon}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	workspace := filepath.Join(dir, "workspace")
	root := filepath.Join(dir, "conv")
	for _, d := range []string{workspace, filepath.Join(root, "home"), filepath.Join(dir, "outside")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	proxy, err := Start(Config{
		Upstream:  upstream,
		Socket:    filepath.Join(root, "home", ".loopforge", "docker.sock"),
		Workspace: "ws1",
		Paths:     []PathMapping{{From: filepath.Join(root, "ws"), To: workspace}},
		BindRoots: []string{workspace, filepath.Join(root, "home")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", proxy.Socket())
	}}}
	return &harness{daemon: daemon, client: client, workspace: workspace, root: root}
}

func (h *harness) do(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, "http://docker/v1.45"+path, reader)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func TestContainerCreateLabelsAndMapsWorkspaceBinds(t *testing.T) {
	h := start(t)
	code, out := h.do(t, "POST", "/containers/create?name=app", map[string]any{
		"Image":  "alpine",
		"Labels": map[string]any{"app": "x", Label: "ws2"},
		"HostConfig": map[string]any{
			"Binds":  []any{filepath.Join(h.root, "ws", "src") + ":/src:ro", "myvol:/data", "newvol:/cache"},
			"Mounts": []any{map[string]any{"Type": "bind", "Source": filepath.Join(h.root, "home", "cache"), "Target": "/c"}},
		},
	})
	if code != 200 {
		t.Fatalf("create: %d %s", code, out)
	}
	got := h.daemon.last(t)
	if got.Path != "/containers/create" || !strings.Contains(got.Query, "name=app") {
		t.Fatalf("forwarded %+v", got)
	}
	labels := got.Body["Labels"].(map[string]any)
	if labels[Label] != "ws1" || labels["app"] != "x" {
		t.Fatalf("labels %v", labels)
	}
	binds := got.Body["HostConfig"].(map[string]any)["Binds"].([]any)
	if binds[0] != filepath.Join(h.workspace, "src")+":/src:ro" {
		t.Fatalf("bind not mapped to the daemon path: %v", binds)
	}
	if h.daemon.labels["volumes/newvol"][Label] != "ws1" {
		t.Fatalf("missing named volume should be created with the workspace label: %v", h.daemon.labels)
	}
}

func TestContainerCreateRefusesHostEscapes(t *testing.T) {
	h := start(t)
	cases := map[string]map[string]any{
		"privileged":    {"Privileged": true},
		"cap-add":       {"CapAdd": []any{"SYS_ADMIN"}},
		"devices":       {"Devices": []any{map[string]any{"PathOnHost": "/dev/sda"}}},
		"gpus":          {"DeviceRequests": []any{map[string]any{"Count": -1}}},
		"seccomp":       {"SecurityOpt": []any{"seccomp=unconfined"}},
		"host-net":      {"NetworkMode": "host"},
		"host-pid":      {"PidMode": "host"},
		"host-userns":   {"UsernsMode": "host"},
		"other-net":     {"NetworkMode": "othernet"},
		"other-ns":      {"PidMode": "container:theirs"},
		"volumes-from":  {"VolumesFrom": []any{"theirs:ro"}},
		"root-bind":     {"Binds": []any{"/:/host"}},
		"docker-sock":   {"Binds": []any{"/var/run/docker.sock:/var/run/docker.sock"}},
		"dotdot-bind":   {"Binds": []any{filepath.Join(h.root, "ws") + "/../../etc:/etc2"}},
		"symlink-bind":  {"Binds": []any{filepath.Join(h.root, "ws", "escape") + ":/x"}},
		"symlink-new":   {"Binds": []any{filepath.Join(h.root, "ws", "escape", "new") + ":/x"}},
		"other-volume":  {"Binds": []any{"othervol:/data"}},
		"mount-bind":    {"Mounts": []any{map[string]any{"Type": "bind", "Source": "/etc", "Target": "/e"}}},
		"mount-driver":  {"Mounts": []any{map[string]any{"Type": "volume", "Source": "v", "VolumeOptions": map[string]any{"DriverConfig": map[string]any{"Name": "local", "Options": map[string]any{"device": "/"}}}}}},
		"runtime":       {"Runtime": "sysbox"},
		"cgroup-parent": {"CgroupParent": "/"},
		"masked-paths":  {"MaskedPaths": []any{}},
		"ro-paths":      {"ReadonlyPaths": []any{}},
		"volume-driver": {"VolumeDriver": "sshfs"},
	}
	for name, hostConfig := range cases {
		before := h.daemon.count()
		code, out := h.do(t, "POST", "/containers/create", map[string]any{"Image": "alpine", "HostConfig": hostConfig})
		if code != http.StatusForbidden || !strings.Contains(out, "LoopForge") {
			t.Errorf("%s: want 403, got %d %s", name, code, out)
		}
		if h.daemon.count() != before {
			t.Errorf("%s: request reached the daemon", name)
		}
	}
	code, out := h.do(t, "POST", "/containers/create", map[string]any{"Image": "alpine", "NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{"othernet": map[string]any{}}}})
	if code != http.StatusForbidden {
		t.Errorf("foreign network endpoint: %d %s", code, out)
	}
	code, out = h.do(t, "POST", "/containers/create", map[string]any{"Image": "alpine", "HostConfig": map[string]any{"NetworkMode": "mynet", "SecurityOpt": []any{"no-new-privileges"}}})
	if code != 200 {
		t.Errorf("own network should be allowed: %d %s", code, out)
	}
}

func TestOnlyOwnObjectsAreReachable(t *testing.T) {
	h := start(t)
	allowed := [][2]string{
		{"POST", "/containers/mine/start"},
		{"GET", "/containers/mine/logs?stdout=1"},
		{"DELETE", "/containers/mine?force=1"},
		{"POST", "/exec/e-mine/start"},
		{"DELETE", "/volumes/myvol"},
		{"DELETE", "/networks/mynet"},
		{"POST", "/commit?container=mine&repo=x"},
		{"GET", "/images/alpine:3/json"},
		{"POST", "/images/create?fromImage=alpine&tag=3"},
		{"GET", "/_ping"},
		{"GET", "/version"},
	}
	for _, c := range allowed {
		if code, out := h.do(t, c[0], c[1], nil); code != 200 {
			t.Errorf("%s %s: want 200, got %d %s", c[0], c[1], code, out)
		}
	}
	denied := [][2]string{
		{"POST", "/containers/theirs/start"},
		{"POST", "/containers/bare/kill"},
		{"GET", "/containers/theirs/archive?path=/"},
		{"POST", "/containers/theirs/exec"},
		{"POST", "/exec/e-theirs/start"},
		{"DELETE", "/volumes/othervol"},
		{"DELETE", "/networks/othernet"},
		{"POST", "/commit?container=theirs"},
		{"POST", "/images/prune"},
		{"POST", "/build/prune"},
		{"GET", "/system/df"},
		{"POST", "/swarm/init"},
		{"GET", "/plugins"},
		{"POST", "/build?networkmode=host"},
		{"POST", "/build?networkmode=container:theirs"},
		{"POST", "/build?cgroupparent=/"},
		{"POST", "/grpc"},
	}
	for _, c := range denied {
		before := h.daemon.count()
		if code, out := h.do(t, c[0], c[1], nil); code != http.StatusForbidden {
			t.Errorf("%s %s: want 403, got %d %s", c[0], c[1], code, out)
		}
		if h.daemon.count() != before {
			t.Errorf("%s %s reached the daemon", c[0], c[1])
		}
	}
	if code, out := h.do(t, "POST", "/containers/mine/exec", map[string]any{"Cmd": []any{"sh"}, "Privileged": true}); code != http.StatusForbidden {
		t.Errorf("privileged exec: %d %s", code, out)
	}
	if code, out := h.do(t, "POST", "/networks/mynet/connect", map[string]any{"Container": "theirs"}); code != http.StatusForbidden {
		t.Errorf("connect foreign container: %d %s", code, out)
	}
}

func TestListsArePinnedToWorkspaceLabel(t *testing.T) {
	h := start(t)
	for _, p := range []string{
		"/containers/json?all=1",
		`/containers/json?filters={"label":{"loopforge.workspace=ws2":true,"app":true}}`,
		"/networks",
		"/volumes",
		"/events",
	} {
		if code, out := h.do(t, "GET", strings.ReplaceAll(p, `"`, "%22"), nil); code != 200 {
			t.Fatalf("%s: %d %s", p, code, out)
		}
		query := h.daemon.last(t).Query
		if !strings.Contains(query, "loopforge.workspace%3Dws1") || strings.Contains(query, "ws2") {
			t.Errorf("%s forwarded with %s", p, query)
		}
	}
	if code, _ := h.do(t, "POST", "/containers/prune", nil); code != 200 || !strings.Contains(h.daemon.last(t).Query, "ws1") {
		t.Errorf("prune should be scoped: %d %+v", code, h.daemon.last(t))
	}
}

func TestCreatedNetworksAndVolumesAreLabelled(t *testing.T) {
	h := start(t)
	if code, out := h.do(t, "POST", "/networks/create", map[string]any{"Name": "n"}); code != 200 {
		t.Fatalf("network: %d %s", code, out)
	}
	if h.daemon.last(t).Body["Labels"].(map[string]any)[Label] != "ws1" {
		t.Fatalf("network not labelled: %+v", h.daemon.last(t))
	}
	if code, out := h.do(t, "POST", "/volumes/create", map[string]any{"Name": "v"}); code != 200 {
		t.Fatalf("volume: %d %s", code, out)
	}
	if code, _ := h.do(t, "POST", "/volumes/create", map[string]any{"Name": "v2", "DriverOpts": map[string]any{"device": "/", "o": "bind"}}); code != http.StatusForbidden {
		t.Fatalf("bind-backed volume must be refused, got %d", code)
	}
	if code, _ := h.do(t, "POST", "/networks/create", map[string]any{"Name": "m", "Driver": "macvlan"}); code != http.StatusForbidden {
		t.Fatalf("macvlan network must be refused, got %d", code)
	}
}

// docker exec/attach hijack the connection after "101 UPGRADED"; the proxy
// must pass the raw stream both ways.
func TestAttachUpgradeStreams(t *testing.T) {
	h := start(t)
	h.daemon.mu.Lock()
	h.daemon.labels["containers/echo"] = map[string]string{Label: "ws1"}
	h.daemon.mu.Unlock()
	conn, err := net.Dial("unix", filepath.Join(h.root, "home", ".loopforge", "docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("POST /v1.45/containers/echo/attach?stream=1&stdin=1&stdout=1 HTTP/1.1\r\nHost: docker\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Length: 0\r\n\r\n"))
	buf := make([]byte, 4096)
	n, _ := conn.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "HTTP/1.1 101") {
		t.Fatalf("no upgrade: %q", buf[:n])
	}
	_, _ = conn.Write([]byte("ping"))
	n, _ = io.ReadAtLeast(conn, buf, 4)
	if string(buf[:n]) != "PING" {
		t.Fatalf("stream not relayed: %q", buf[:n])
	}
}

func TestImagesAreSharedButOnlyOwnOnesChange(t *testing.T) {
	h := start(t)
	allowed := [][2]string{
		{"DELETE", "/images/myapp:dev"},
		{"POST", "/images/myapp:dev/tag?repo=myapp&tag=v2"},
		{"POST", "/images/alpine:3/tag?repo=mine&tag=1"},
		{"POST", "/images/create?fromImage=alpine&tag=3"},
		{"GET", "/images/team/other:1/json"},
		{"POST", "/commit?container=mine&repo=fresh&tag=1"},
		{"POST", "/build?t=myapp:dev&t=new:1"},
	}
	for _, c := range allowed {
		if code, out := h.do(t, c[0], c[1], nil); code != 200 {
			t.Errorf("%s %s: want 200, got %d %s", c[0], c[1], code, out)
		}
	}
	denied := [][2]string{
		{"DELETE", "/images/alpine:3"},
		{"DELETE", "/images/team/other:1"},
		{"DELETE", "/images/registry.io/x/y:2.0"},
		{"POST", "/images/myapp:dev/tag?repo=alpine&tag=3"},
		{"POST", "/build?t=team/other:1"},
		{"POST", "/commit?container=mine&repo=alpine&tag=3"},
		{"POST", "/images/create?fromSrc=-&repo=alpine&tag=3"},
		{"POST", "/images/load"},
	}
	for _, c := range denied {
		before := h.daemon.count()
		if code, out := h.do(t, c[0], c[1], nil); code != http.StatusForbidden {
			t.Errorf("%s %s: want 403, got %d %s", c[0], c[1], code, out)
		}
		if h.daemon.count() != before {
			t.Errorf("%s %s reached the daemon", c[0], c[1])
		}
	}
	h.do(t, "POST", "/build?t=new:1&labels=%7B%22app%22%3A%22x%22%7D", nil)
	if q := h.daemon.last(t).Query; !strings.Contains(q, "loopforge.workspace") || !strings.Contains(q, "app") {
		t.Errorf("build labels not injected: %s", q)
	}
	h.do(t, "POST", "/commit?container=mine&repo=fresh", map[string]any{"Labels": map[string]any{"k": "v"}})
	if labels := h.daemon.last(t).Body["Labels"].(map[string]any); labels[Label] != "ws1" || labels["k"] != "v" {
		t.Errorf("commit labels: %v", labels)
	}
}

// On Docker 26+ a bind below the workspace root becomes a subpath of a volume
// bound to the root, so swapping a checked directory for a symlink before the
// container starts cannot redirect the mount.
func TestBindsBelowWorkspaceRootBecomeVolumeSubpaths(t *testing.T) {
	h := startAPI(t, "1.45")
	ws := filepath.Join(h.root, "ws")
	code, out := h.do(t, "POST", "/containers/create", map[string]any{
		"Image": "alpine",
		"HostConfig": map[string]any{
			"Binds":  []any{ws + "/src/app:/app:ro,rshared", ws + ":/all", "cache:/cache"},
			"Mounts": []any{map[string]any{"Type": "bind", "Source": ws + "/data", "Target": "/data", "BindOptions": map[string]any{"Propagation": "rshared"}}},
		},
	})
	if code != 200 {
		t.Fatalf("create: %d %s", code, out)
	}
	host := h.daemon.last(t).Body["HostConfig"].(map[string]any)
	binds := host["Binds"].([]any)
	if len(binds) != 2 || binds[0] != h.workspace+":/all" || binds[1] != "cache:/cache" {
		t.Fatalf("only the root and named volumes stay binds: %v", binds)
	}
	mounts, _ := json.Marshal(host["Mounts"])
	for _, want := range []string{`"Subpath":"data"`, `"Subpath":"src/app"`, `"Type":"volume"`, `"ReadOnly":true`} {
		if !strings.Contains(string(mounts), want) {
			t.Errorf("mounts missing %s: %s", want, mounts)
		}
	}
	if strings.Contains(string(mounts), "rshared") || strings.Contains(string(mounts), "BindOptions") {
		t.Errorf("bind propagation must be dropped: %s", mounts)
	}
	if _, err := os.Stat(filepath.Join(h.workspace, "src", "app")); err != nil {
		t.Errorf("missing bind source should be created like a plain bind: %v", err)
	}
	var volume string
	for key, opts := range h.daemon.options {
		if strings.HasPrefix(key, "volumes/lf-bind-ws1-") && opts["device"] == h.workspace && opts["o"] == "bind" {
			volume = key
		}
	}
	if volume == "" || h.daemon.labels[volume][Label] != "ws1" {
		t.Fatalf("root-backed volume not created with the workspace label: %v", h.daemon.options)
	}
	if code, _ := h.do(t, "POST", "/volumes/create", map[string]any{"Name": "lf-bind-ws1-x"}); code != http.StatusForbidden {
		t.Errorf("platform volume prefix must be reserved, got %d", code)
	}
	if code, _ := h.do(t, "POST", "/containers/create", map[string]any{"Image": "alpine", "HostConfig": map[string]any{"Binds": []any{ws + "/x:/x:bogus"}}}); code != http.StatusForbidden {
		t.Errorf("unknown bind option must be refused, got %d", code)
	}
}
