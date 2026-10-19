package runtime

import (
	"agentflow/internal/domain"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Manager struct {
	root  string
	slots chan struct{}
}

func NewManager(root string) *Manager { return NewManagerWithLimit(root, 8) }

func NewManagerWithLimit(root string, concurrency int) *Manager {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Manager{root: root, slots: make(chan struct{}, concurrency)}
}

func (m *Manager) Root() string {
	if m == nil {
		return ""
	}
	return m.root
}

// RunWorkspace returns the host path reserved for a run. It does not create
// the directory; callers must still check whether the run has materialized it.
func (m *Manager) RunWorkspace(runID string) string {
	if m == nil || strings.TrimSpace(m.root) == "" {
		return ""
	}
	return filepath.Join(m.root, safeID(runID))
}

func (m *Manager) Create(ctx context.Context, runID, nodeID string, profile domain.SandboxProfile) (Sandbox, error) {
	return m.open(ctx, safeID(runID+"-"+nodeID), profile, true, true, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
}

// AcquirePersistent reuses a run/issue workspace across SOP nodes. An existing
// git checkout is kept so later nodes continue the same branch.
func (m *Manager) AcquirePersistent(ctx context.Context, persistID string, profile domain.SandboxProfile) (Sandbox, error) {
	return m.open(ctx, "persist-"+safeID(persistID), profile, false, false, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
}

// AcquireRun opens or reconstructs the one sandbox/workspace owned by a
// workflow run. Requirements must be aggregated across all workflow providers
// before this call so Docker mounts cannot depend on whichever node runs first.
func (m *Manager) AcquireRun(ctx context.Context, runID string, profile domain.SandboxProfile, requirements SandboxRequirements, credentials RunCredentials, resources SandboxResources) (Sandbox, error) {
	return m.open(ctx, safeID(runID), profile, false, false, requirements, credentials, resources)
}

func (m *Manager) open(ctx context.Context, id string, profile domain.SandboxProfile, wipe, ephemeral bool, requirements SandboxRequirements, credentials RunCredentials, resources SandboxResources) (Sandbox, error) {
	lock, err := m.acquireLock(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("sandbox lock: %w", err)
	}
	fail := func(err error) (Sandbox, error) {
		_ = unlockFile(lock)
		return nil, err
	}
	workspace := filepath.Join(m.root, id)
	if wipe {
		if err := os.RemoveAll(workspace); err != nil {
			return fail(fmt.Errorf("clean sandbox workspace: %w", err))
		}
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return fail(err)
	}
	activated, err := prepareSandboxEnvironment(workspace, profile, resources.Environment)
	if err != nil {
		return fail(err)
	}
	if len(activated.Overlay) > 0 {
		path := activated.Overlay["PATH"]
		profile.Environment = mergeEnvironmentMaps(activated.Overlay, profile.Environment)
		if path != "" {
			profile.Environment["PATH"] = path
		}
	}
	profile, resources.Dockerfile = isolateLocalDriver(profile, resources.Dockerfile)
	var sandbox Sandbox
	switch strings.TrimSpace(profile.Driver) {
	case "", "local":
		sandbox = &localSandbox{id: id, workspace: workspace, environment: profile.Environment}
	case "seatbelt":
		sandbox, err = createSeatbelt(id, workspace, profile, requirements)
	case "bwrap":
		sandbox, err = createBwrap(id, workspace, profile, requirements)
	case "docker":
		sandbox, err = createDocker(ctx, id, workspace, profile, requirements, resources.Dockerfile)
	default:
		err = fmt.Errorf("unsupported sandbox driver %q", profile.Driver)
	}
	if err != nil {
		return fail(err)
	}
	managed := &managedSandbox{Sandbox: sandbox, slots: m.slots, lock: lock, ephemeral: ephemeral, workspace: workspace, credentials: credentials}
	if err := runEnvironmentInstall(ctx, managed, activated); err != nil {
		_ = managed.Close(ctx)
		return nil, err
	}
	return managed, nil
}

// DestroyRun deletes the host workspace for a finished SOP run: the persistent
// git checkout and any per-node directories. Artifacts must already be in the
// store before calling this.
func (m *Manager) DestroyRun(runID string) error {
	if m == nil || strings.TrimSpace(m.root) == "" {
		return nil
	}
	id := safeID(runID)
	if id == "" {
		return nil
	}
	// Removing the directory alone leaves the container running against a bind
	// that no longer exists. Docker reports such a container as active, so no
	// prune can reclaim its writable layer afterwards.
	removeDockerSandbox(context.Background(), dockerContainerName(id), tempDockerImageName(id))
	_, err := m.removeMatching(func(name string) bool {
		return name == id || strings.HasPrefix(name, id+"-") || name == "persist-"+id || strings.HasPrefix(name, "persist-"+id+"-")
	})
	return err
}

// DiscardRunContainer drops a run's sandbox container and image but keeps its
// workspace. A rerun needs the existing git checkout; it does not need the
// writable layer the previous attempt filled with installs and caches.
func (m *Manager) DiscardRunContainer(ctx context.Context, runID string) {
	if m == nil {
		return
	}
	if id := safeID(runID); id != "" {
		removeDockerSandbox(ctx, dockerContainerName(id), tempDockerImageName(id))
	}
}

// RunsWithIdleDockerSandbox narrows runIDs to those that still have a sandbox
// container on this host and whose workspace no process currently holds open.
func (m *Manager) RunsWithIdleDockerSandbox(ctx context.Context, runIDs []string) ([]string, error) {
	if m == nil || strings.TrimSpace(m.root) == "" || len(runIDs) == 0 {
		return nil, nil
	}
	found, err := dockerSandboxesUnder(ctx, m.root)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	workspaces := make(map[string]struct{}, len(found))
	for _, workspace := range found {
		workspaces[workspace] = struct{}{}
	}
	var out []string
	for _, runID := range runIDs {
		id := safeID(runID)
		if id == "" {
			continue
		}
		for _, workspace := range []string{id, "persist-" + id} {
			if _, ok := workspaces[workspace]; ok && !m.workspaceInUse(workspace) {
				out = append(out, runID)
				break
			}
		}
	}
	return out, nil
}

// ReclaimDockerSandboxes removes sandbox containers left behind by runs that no
// longer own a workspace. It reads the live set from Docker instead of from an
// in-process map, which is the only way to reach containers inherited from an
// earlier process: a deploy restart drops every sandbox handle while the
// containers keep running.
func (m *Manager) ReclaimDockerSandboxes(ctx context.Context, liveRunIDs []string) (int, error) {
	if m == nil || strings.TrimSpace(m.root) == "" {
		return 0, nil
	}
	found, err := dockerSandboxesUnder(ctx, m.root)
	if err != nil || len(found) == 0 {
		return 0, err
	}
	var removed int
	for container, workspace := range staleSandboxContainers(found, liveRunIDs) {
		// The flock is held for as long as a sandbox is open, including by
		// another process on this host. Reclaiming then would docker rm a
		// container with an agent running inside it.
		if m.workspaceInUse(workspace) {
			continue
		}
		removeDockerSandbox(ctx, container, tempDockerImageName(workspace))
		removed++
	}
	return removed, nil
}

// ReclaimCachedDockerImages drops content-hashed images that no catalog
// Dockerfile still produces and that no container is using. Live recipes and
// in-flight sandboxes keep their tags so the next run does not rebuild.
func (m *Manager) ReclaimCachedDockerImages(ctx context.Context, dockerfiles []domain.DockerfileDefinition) (int, error) {
	if m == nil || !dockerAvailable() {
		return 0, nil
	}
	keep := make(map[string]struct{}, len(dockerfiles))
	for _, dockerfile := range dockerfiles {
		if strings.TrimSpace(dockerfile.Content) == "" {
			continue
		}
		keep[cachedDockerImageName(dockerfile)] = struct{}{}
	}
	return reclaimCachedDockerImages(ctx, keep)
}

// staleSandboxContainers narrows container -> workspace pairs to those whose
// workspace no longer belongs to a run that is allowed to keep one.
func staleSandboxContainers(found map[string]string, liveRunIDs []string) map[string]string {
	keep := make(map[string]struct{}, len(liveRunIDs)*2)
	for _, runID := range liveRunIDs {
		if id := safeID(runID); id != "" {
			keep[id] = struct{}{}
			keep["persist-"+id] = struct{}{}
		}
	}
	stale := map[string]string{}
	for container, workspace := range found {
		if _, live := keep[workspace]; !live {
			stale[container] = workspace
		}
	}
	return stale
}

// ReclaimExcept deletes workspaces that do not belong to the given live run IDs.
// Waiting/running checkouts are kept so an approval or resume can continue.
func (m *Manager) ReclaimExcept(liveRunIDs []string) (int, error) {
	if m == nil || strings.TrimSpace(m.root) == "" {
		return 0, nil
	}
	keep := make(map[string]struct{}, len(liveRunIDs)*2)
	for _, runID := range liveRunIDs {
		id := safeID(runID)
		if id == "" {
			continue
		}
		keep[id] = struct{}{}
		keep["persist-"+id] = struct{}{}
	}
	return m.removeMatching(func(name string) bool {
		for prefix := range keep {
			if name == prefix || strings.HasPrefix(name, prefix+"-") {
				return false
			}
		}
		return true
	})
}

func (m *Manager) removeMatching(match func(name string) bool) (int, error) {
	root, err := filepath.Abs(m.root)
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	var first error
	var removed int
	for _, entry := range entries {
		name := entry.Name()
		if name == sandboxLockDir || strings.HasPrefix(name, ".") {
			continue
		}
		if !match(name) {
			continue
		}
		dir := filepath.Join(root, name)
		rel, relErr := filepath.Rel(root, dir)
		if relErr != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		// A live Exec has this directory as cwd. Removing it makes Node throw
		// "ENOENT: no such file or directory, uv_cwd" instead of a Git error.
		if m.workspaceInUse(name) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		removed++
	}
	return removed, first
}

type managedSandbox struct {
	Sandbox
	once          sync.Once
	slots         chan struct{}
	lock          *os.File
	ephemeral     bool
	workspace     string
	credentials   RunCredentials
	execMu        sync.RWMutex
	credentialsMu sync.RWMutex
}

func (s *managedSandbox) Driver() string {
	if value, ok := s.Sandbox.(interface{ Driver() string }); ok {
		return value.Driver()
	}
	return ""
}

type isolationEnvSource interface {
	IsolationEnv() map[string]string
}

func sandboxIsolationEnv(sb Sandbox) map[string]string {
	if src, ok := sb.(isolationEnvSource); ok {
		return src.IsolationEnv()
	}
	return nil
}

func (s *managedSandbox) IsolationEnv() map[string]string {
	if src, ok := s.Sandbox.(isolationEnvSource); ok {
		return src.IsolationEnv()
	}
	return nil
}

func (s *processSandbox) IsolationEnv() map[string]string { return s.profile.Environment }

func (s *localSandbox) IsolationEnv() map[string]string { return s.environment }

func (s *dockerSandbox) IsolationEnv() map[string]string { return s.profile.Environment }

func (s *managedSandbox) Close(ctx context.Context) error {
	var err error
	s.once.Do(func() {
		err = s.Sandbox.Close(ctx)
		if s.ephemeral && s.workspace != "" {
			_ = os.RemoveAll(s.workspace)
		}
		_ = unlockFile(s.lock)
	})
	return err
}

// Detach drops the workspace flock without docker rm. Deploy drain parks the
// run so the next worker can reuse the same bind-mounted container.
func (s *managedSandbox) Detach() error {
	s.once.Do(func() {
		_ = unlockFile(s.lock)
	})
	return nil
}

func (s *managedSandbox) Recreate(ctx context.Context) error {
	if rec, ok := s.Sandbox.(interface{ Recreate(context.Context) error }); ok {
		return rec.Recreate(ctx)
	}
	return nil
}

func (s *managedSandbox) BindWorkspace(ctx context.Context) error {
	if bind, ok := s.Sandbox.(interface{ BindWorkspace(context.Context) error }); ok {
		return bind.BindWorkspace(ctx)
	}
	return s.Recreate(ctx)
}

// SetCredentials replaces the token snapshot used by later Exec calls.
// The workspace and container stay as they are.
func (s *managedSandbox) SetCredentials(credentials RunCredentials) {
	s.credentialsMu.Lock()
	defer s.credentialsMu.Unlock()
	s.credentials = credentials
}

func (s *managedSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.slots }()
	if command.Concurrent {
		s.execMu.RLock()
		defer s.execMu.RUnlock()
	} else {
		s.execMu.Lock()
		defer s.execMu.Unlock()
	}
	s.credentialsMu.RLock()
	credentials := s.credentials
	s.credentialsMu.RUnlock()
	bound, err := bindCommand(s.Sandbox, s.workspace, command, credentials)
	if err != nil {
		return "", err
	}
	return s.Sandbox.Exec(ctx, bound, sink)
}
