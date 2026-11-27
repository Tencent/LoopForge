package runtime

import (
	"agentflow/internal/domain"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
)

type openSandboxBackend struct {
	connection opensandbox.ConnectionConfig
	config     domain.OpenSandboxConnection
}

func NormalizeOpenSandboxConnection(config domain.OpenSandboxConnection) (domain.OpenSandboxConnection, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.URL))
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || len(config.URL) > 2048 {
		return config, errors.New("请输入 OpenSandbox 的 HTTP 或 HTTPS 服务地址，地址中请勿包含凭据、查询参数或片段")
	}
	endpoint.Path = strings.TrimSuffix(strings.TrimRight(endpoint.Path, "/"), "/v1")
	endpoint.RawPath = ""
	endpoint.Host = strings.ToLower(endpoint.Host)
	config.URL = endpoint.String()
	config.APIKey = strings.TrimSpace(config.APIKey)
	if len(config.APIKey) > 4096 || strings.ContainsAny(config.APIKey, "\r\n\x00") {
		return config, errors.New("请输入有效的 OpenSandbox API Key")
	}
	return config, nil
}

// Prevent a saved unauthenticated connection from inheriting the SDK's
// process-wide OPEN_SANDBOX_API_KEY, which may belong to a different endpoint.
type openSandboxAnonymousTransport struct{}

func (openSandboxAnonymousTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("OPEN-SANDBOX-API-KEY") != "" {
		req = req.Clone(req.Context())
		req.Header.Del("OPEN-SANDBOX-API-KEY")
	}
	return http.DefaultTransport.RoundTrip(req)
}

func newOpenSandboxBackend(config domain.OpenSandboxConnection) *openSandboxBackend {
	retry := opensandbox.DefaultRetryConfig()
	protocol, _, _ := strings.Cut(config.URL, "://")
	connection := opensandbox.ConnectionConfig{
		Domain: config.URL, Protocol: protocol, APIKey: config.APIKey, DisableMetrics: true, Retry: &retry,
	}
	if config.APIKey == "" {
		connection.HTTPClient = &http.Client{Transport: openSandboxAnonymousTransport{}}
	}
	return &openSandboxBackend{config: config, connection: connection}
}

func ProbeOpenSandboxConnection(ctx context.Context, config domain.OpenSandboxConnection) error {
	_, err := listOpenSandboxes(ctx, config, opensandbox.ListOptions{PageSize: 1})
	return err
}

func OpenSandboxLoad(ctx context.Context, config domain.OpenSandboxConnection) (int, error) {
	result, err := listOpenSandboxes(ctx, config, opensandbox.ListOptions{
		PageSize: 1,
		States:   []opensandbox.SandboxState{opensandbox.StatePending, opensandbox.StateRunning, opensandbox.StatePausing, opensandbox.StatePaused, opensandbox.StateStopping},
	})
	if err != nil {
		return 0, err
	}
	return max(result.Pagination.TotalItems, len(result.Items)), nil
}

func listOpenSandboxes(ctx context.Context, config domain.OpenSandboxConnection, opts opensandbox.ListOptions) (*opensandbox.ListSandboxesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	client := opensandbox.NewLifecycleClient(config.URL+"/v1", config.APIKey, opensandbox.WithHTTPClient(&http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}))
	result, err := client.ListSandboxes(ctx, opts)
	if err != nil {
		var remote *opensandbox.APIError
		if errors.As(err, &remote) {
			return nil, fmt.Errorf("OpenSandbox 返回 HTTP %d，请检查服务地址和 API Key", remote.StatusCode)
		}
		return nil, errors.New("无法连接 OpenSandbox，请检查服务地址和 API 网络")
	}
	if result.Items == nil {
		return nil, errors.New("服务未返回 OpenSandbox 沙箱列表，请检查服务地址")
	}
	return result, nil
}

func (m *Manager) SetOpenSandboxSource(source func(context.Context, string, string) (domain.OpenSandboxConnection, error)) {
	m.openSandboxSource = source
}

func (m *Manager) openSandboxCluster(ctx context.Context, resources SandboxResources) (*openSandboxBackend, error) {
	if resources.ClusterID == "" {
		return nil, nil
	}
	if m.openSandboxSource == nil {
		return nil, errors.New("执行集群连接不可用")
	}
	config, err := m.openSandboxSource(ctx, resources.ClusterID, resources.ProjectID)
	if err != nil {
		return nil, err
	}
	if config.URL == "" {
		return nil, errors.New("执行集群服务地址为空")
	}
	return newOpenSandboxBackend(config), nil
}

// SupportsPlatformSandbox reports capabilities for the platform machine.
func (m *Manager) SupportsPlatformSandbox(profile domain.SandboxProfile, local bool) bool {
	if profile.Driver == domain.SandboxDriverDocker {
		return !local || DockerReady()
	}
	return local && NativeSandboxAvailable(profile.Driver)
}

func ValidateExecutionClusterProfile(profile domain.SandboxProfile) error {
	return validateOpenSandboxProfile(profile, domain.DockerfileDefinition{})
}

func validateOpenSandboxProfile(profile domain.SandboxProfile, dockerfile domain.DockerfileDefinition) error {
	if profile.Driver != domain.SandboxDriverDocker && profile.Driver != domain.SandboxDriverNative {
		return errors.New("执行集群支持平台默认 Docker 沙箱或 native 沙箱")
	}
	if profile.Privileged || profile.ReadOnlyRootFS {
		return errors.New("OpenSandbox 当前支持普通沙箱，特权与只读根文件系统设置暂不可用")
	}
	if profile.Driver == domain.SandboxDriverDocker {
		if profile.Dockerfile != nil {
			dockerfile.Content, dockerfile.Files = profile.Dockerfile.Content, profile.Dockerfile.Files
		}
		if !isBuiltInIsolatedDockerfile(dockerfile) {
			return errors.New("OpenSandbox 使用预构建镜像，请选择平台默认 Docker 沙箱或 native 沙箱")
		}
	}
	return nil
}

type openSandbox struct {
	backend   *openSandboxBackend
	manager   *Manager
	id        string
	workspace string
	image     string
	syncLock  string
	profile   domain.SandboxProfile
	gate      sync.RWMutex
	mu        sync.Mutex
	instance  *opensandbox.Sandbox
	expires   time.Time
	closed    bool
}

// Official installs provide the same built-in execution image via
// AGENTFLOW_ISOLATED_IMAGE. The fallback matches the published Compose pin.
const defaultOpenSandboxImage = "ghcr.io/tencent/loopforge:0.4.2"

func (m *Manager) newOpenSandbox(backend *openSandboxBackend, id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements, dockerfile domain.DockerfileDefinition) (Sandbox, error) {
	if err := validateOpenSandboxProfile(profile, dockerfile); err != nil {
		return nil, err
	}
	if requirements.RunnerLoginRelay {
		return nil, errors.New("OpenSandbox CLI 使用 API Key")
	}
	root, err := filepath.Abs(m.root)
	if err != nil || m.root == "" || root == "/" {
		return nil, errors.New("OpenSandbox 需要专用的工作区根目录")
	}
	rel, err := filepath.Rel(root, workspace)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return nil, errors.New("OpenSandbox 工作区必须位于平台工作区根目录")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	actual, err := filepath.EvalSymlinks(workspace)
	if err != nil || actual != filepath.Join(root, rel) {
		return nil, errors.New("OpenSandbox 工作区目录不能是符号链接")
	}
	image := strings.TrimSpace(os.Getenv("AGENTFLOW_ISOLATED_IMAGE"))
	if image == "" {
		image = defaultOpenSandboxImage
	}
	return &openSandbox{
		backend: backend, manager: m, id: id, workspace: workspace, image: image,
		syncLock: "opensandbox-workspace-" + safeID(rel), profile: profile,
	}, nil
}

func (s *openSandbox) ID() string                      { return s.id }
func (s *openSandbox) Workspace() string               { return s.workspace }
func (s *openSandbox) Driver() string                  { return "opensandbox" }
func (s *openSandbox) IsolationEnv() map[string]string { return s.profile.Environment }

// A finite lease also bounds orphaned instances after an unclean worker exit.
const openSandboxLease = 2 * time.Hour

func (s *openSandbox) ensure(ctx context.Context) (*opensandbox.Sandbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("OpenSandbox 工作区已关闭")
	}
	if s.instance != nil && time.Now().Before(s.expires) {
		if time.Until(s.expires) > openSandboxLease/2 {
			return s.instance, nil
		}
		renewed, err := s.instance.Renew(ctx, openSandboxLease)
		if err == nil {
			s.expires = renewed.ExpiresAt
			return s.instance, nil
		}
		if !openSandboxNotFound(err) {
			return nil, fmt.Errorf("续期 OpenSandbox：%w", err)
		}
	}
	limits := maps.Clone(opensandbox.DefaultResourceLimits)
	if s.profile.CPUMillicores > 0 {
		limits["cpu"] = fmt.Sprintf("%dm", s.profile.CPUMillicores)
	}
	if s.profile.MemoryMiB > 0 {
		limits["memory"] = fmt.Sprintf("%dMi", s.profile.MemoryMiB)
	}
	ttl := int(openSandboxLease.Seconds())
	expires := time.Now().Add(openSandboxLease)
	// Use a sandbox-scoped token so a task cannot use its own execd endpoint
	// to start another process as the root controller.
	token := rand.Text() + rand.Text()
	connection := s.backend.connection
	connection.Headers = map[string]string{"X-EXECD-ACCESS-TOKEN": token}
	instance, err := opensandbox.CreateSandbox(ctx, connection, opensandbox.SandboxCreateOptions{
		Image: s.image, TimeoutSeconds: &ttl, ResourceLimits: limits, SecureAccess: s.backend.config.SecureAccess,
		Env:          map[string]string{"EXECD_ACCESS_TOKEN": token},
		Metadata:     map[string]string{"managed-by": "loopforge", "workspace-id": s.id},
		ReadyTimeout: 2 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 OpenSandbox：%w", err)
	}
	s.instance, s.expires = instance, expires
	return instance, nil
}

func openSandboxNotFound(err error) bool {
	var apiError *opensandbox.APIError
	return errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound
}

func (s *openSandbox) Close(ctx context.Context) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.kill(ctx); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func (s *openSandbox) Recreate(ctx context.Context) error {
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kill(ctx)
}

func (s *openSandbox) kill(ctx context.Context) error {
	if s.instance == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.instance.Kill(ctx); err != nil && !openSandboxNotFound(err) {
		return fmt.Errorf("回收 OpenSandbox：%w", err)
	}
	s.instance = nil
	return nil
}

func (s *openSandbox) environmentIdentity() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.instance == nil || time.Now().After(s.expires) {
		return "", errors.New("OpenSandbox 工作区尚未就绪")
	}
	return "opensandbox:" + s.instance.ID(), nil
}

func (s *openSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if len(command.Argv) == 0 {
		return "", fmt.Errorf("%w: OpenSandbox command is empty", errProcessNotStarted)
	}
	if command.runnerLogin != nil {
		return "", fmt.Errorf("%w: OpenSandbox CLI 使用 API Key", errProcessNotStarted)
	}
	lock, err := s.manager.acquireLock(ctx, s.syncLock)
	if err != nil {
		return "", fmt.Errorf("%w: 等待 OpenSandbox 工作区：%w", errProcessNotStarted, err)
	}
	defer unlockFile(lock)
	command, err = projectCommandEnvironment(ctx, command, s.profile.Environment)
	if err != nil {
		return "", err
	}
	if command.openSandboxConversation != nil {
		return s.execConversation(ctx, command, sink)
	}
	argv, cleanup, err := s.commandInput(command)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errProcessNotStarted, err)
	}
	defer cleanup()
	instance, err := s.ensure(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errProcessNotStarted, err)
	}
	wd := command.WorkingDir
	if wd == "" {
		wd = s.workspace
	}
	env := mergeEnvironmentMaps(s.profile.Environment, command.environment, command.projectCredentialEnv)
	uid, gid := int32(os.Geteuid()), int32(os.Getegid())
	before, err := s.uploadWorkspace(ctx, instance, s.workspace, workspaceTransferSkips, uid, gid)
	if err != nil {
		return "", fmt.Errorf("%w: 上传 OpenSandbox 工作区：%w", errProcessNotStarted, err)
	}
	request := opensandbox.RunCommandRequest{Argv: argv, Cwd: wd, Envs: env, UID: &uid, GID: &gid}
	if deadline, ok := ctx.Deadline(); ok {
		request.Timeout = max(1, time.Until(deadline).Milliseconds())
	}
	output, runErr := s.runCommand(ctx, instance, request, command, sink)
	if err := ctx.Err(); err != nil {
		return output, err
	}
	if err := s.downloadWorkspace(ctx, instance, s.workspace, workspaceTransferSkips, before); err != nil {
		return output, errors.Join(runErr, fmt.Errorf("取回 OpenSandbox 工作区：%w", err))
	}
	return output, runErr
}

func (s *openSandbox) commandInput(command CommandSpec) ([]string, func(), error) {
	noop := func() {}
	input := command.Stdin
	if input == nil && len(command.codeBuddySOPInput) > 0 {
		input = bytes.NewReader(command.codeBuddySOPInput)
	}
	if input == nil {
		return command.Argv, noop, nil
	}
	switch input.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
	default:
		return nil, noop, errors.New("OpenSandbox 当前仅支持一次性标准输入")
	}
	dir, err := EnsureControlDirectory(s.workspace, "opensandbox-input")
	if err != nil {
		return nil, noop, err
	}
	root, err := os.OpenRoot(s.workspace)
	if err != nil {
		return nil, noop, err
	}
	rel, err := filepath.Rel(s.workspace, filepath.Join(dir, rand.Text()))
	if err != nil {
		_ = root.Close()
		return nil, noop, err
	}
	file, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, noop, err
	}
	cleanup := func() {
		_ = root.Remove(rel)
		_ = root.Close()
	}
	_, writeErr := io.Copy(file, input)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		cleanup()
		return nil, noop, err
	}
	argv := append([]string{"/bin/sh", "-ec", `input=$1; shift; exec 3< "$input"; rm -f -- "$input"; exec "$@" <&3 3<&-`, "loopforge-stdin", filepath.Join(s.workspace, rel)}, command.Argv...)
	return argv, cleanup, nil
}

func (s *openSandbox) runCommand(ctx context.Context, instance *opensandbox.Sandbox, request opensandbox.RunCommandRequest, command CommandSpec, sink EventSink) (string, error) {
	var buf bytes.Buffer
	output := &buf
	if command.openSandboxConversation != nil {
		output = nil // The conversation polls its own bounded event queue.
	}
	var mu sync.Mutex
	redact := projectCredentialRedactor(command.projectCredentialEnv)
	stdout := &processWriter{output: output, mu: &mu, sink: sink, name: "process.stdout", level: "info", observe: command.observeOutput, redact: redact}
	stderr := &processWriter{output: output, mu: &mu, sink: sink, name: "process.stderr", level: "error", observe: command.observeOutput, redact: redact}
	emit := func(writer *processWriter, out opensandbox.OutputMessage) error {
		_, err := writer.Write([]byte(out.Text))
		return err
	}
	execution, err := instance.RunCommandWithOpts(ctx, request, &opensandbox.ExecutionHandlers{
		SkipAccumulation: true,
		OnInit: func(init opensandbox.ExecutionInit) error {
			if sink != nil {
				sink(domain.TelemetryEvent{Category: "process", Name: "process.started", Phase: "start", Message: compactCommand(command.Argv, command.credentialEnvKeys...),
					Attributes: map[string]any{"sandboxId": instance.ID(), "executionId": init.ID}})
			}
			return nil
		},
		OnStdout: func(out opensandbox.OutputMessage) error { return emit(stdout, out) },
		OnStderr: func(out opensandbox.OutputMessage) error { return emit(stderr, out) },
	})
	if execution != nil && execution.ID != "" && execution.Complete == nil && execution.Error == nil {
		s.interrupt(instance, execution.ID)
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if err == nil {
		switch {
		case execution == nil:
			err = errors.New("OpenSandbox 未返回命令结果")
		case execution.Error != nil:
			err = fmt.Errorf("OpenSandbox 命令失败：%s", execution.Error.Value)
			if execution.ExitCode == nil {
				err = fmt.Errorf("%w: %w", errProcessNotStarted, err)
			}
		case execution.Complete == nil:
			err = errors.New("OpenSandbox 命令流在结束前断开")
		}
	} else {
		// An API validation response can contain input values. Keep the status
		// code and prevent request-scoped environment values from reaching logs.
		var apiError *opensandbox.APIError
		if errors.As(err, &apiError) {
			err = fmt.Errorf("OpenSandbox 执行接口返回 HTTP %d", apiError.StatusCode)
		}
	}
	if err != nil && redact != nil && redact(err.Error()) != err.Error() {
		err = errors.New(redact(err.Error()))
	}
	stdout.flush()
	stderr.flush()
	if sink != nil {
		level := "info"
		if err != nil {
			level = "error"
		}
		attrs := map[string]any{"sandboxId": instance.ID()}
		if execution != nil && execution.ExitCode != nil {
			attrs["exitCode"] = *execution.ExitCode
		}
		sink(domain.TelemetryEvent{Category: "process", Name: "process.finished", Phase: "finish", Level: level, Message: commandResult(err), Attributes: attrs})
	}
	return buf.String(), err
}

func (s *openSandbox) interrupt(instance *opensandbox.Sandbox, executionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint, err := instance.GetEndpoint(ctx, opensandbox.DefaultExecdPort)
	if err != nil {
		return
	}
	address := endpoint.Endpoint
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		address = s.backend.connection.GetProtocol() + "://" + address
	}
	client := opensandbox.NewExecdClient(address, "", opensandbox.WithHeaders(endpoint.Headers))
	_ = client.InterruptCommand(ctx, executionID)
}
