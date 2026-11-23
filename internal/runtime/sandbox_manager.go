package runtime

import (
	"agentflow/internal/domain"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
)

// ErrSandboxInUse tells callers that cleanup was deferred, not completed.
var ErrSandboxInUse = errors.New("sandbox is in use")

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

// RunWorkspaceExists reports whether the local workspace has materialized.
// Inspection failures are returned to the caller so a workspace cannot be
// mistaken for a missing checkout when its state is unknown.
func (m *Manager) RunWorkspaceExists(runID string) (bool, error) {
	workspace := m.RunWorkspace(runID)
	if workspace == "" {
		return false, fmt.Errorf("workspace root is not configured")
	}
	_, err := os.Stat(workspace)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect run workspace: %w", err)
	}
	return true, nil
}

// LiveDockerSandbox reports whether a Docker sandbox currently binds the
// workspace. The check belongs to the host environment manager so workflow
// orchestration does not need to invoke Docker directly.
func (m *Manager) LiveDockerSandbox(workspace string) bool {
	workspace = filepath.Clean(workspace)
	if workspace == "" || workspace == "." {
		return false
	}
	name := "agentflow-" + filepath.Base(workspace)
	return exec.Command("docker", "inspect", name).Run() == nil
}

func (m *Manager) Create(ctx context.Context, runID, nodeID string, profile domain.SandboxProfile) (Sandbox, error) {
	id := safeID(runID + "-" + nodeID)
	return m.open(ctx, id, id, profile, true, true, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
}

// AcquirePersistent reuses a run/issue workspace across SOP nodes. An existing
// git checkout is kept so later nodes continue the same branch.
func (m *Manager) AcquirePersistent(ctx context.Context, persistID string, profile domain.SandboxProfile) (Sandbox, error) {
	id := "persist-" + safeID(persistID)
	return m.open(ctx, id, id, profile, false, false, SandboxRequirements{}, RunCredentials{}, SandboxResources{})
}

// AcquireRun opens or reconstructs the one sandbox/workspace owned by a
// workflow run. Requirements must be aggregated across all workflow providers
// before this call so Docker mounts cannot depend on whichever node runs first.
func (m *Manager) AcquireRun(ctx context.Context, runID string, profile domain.SandboxProfile, requirements SandboxRequirements, credentials RunCredentials, resources SandboxResources) (Sandbox, error) {
	return m.AcquireRunIn(ctx, runID, runID, profile, requirements, credentials, resources)
}

// AcquireRunIn opens the sandbox of runID on the workspace of workspaceRunID.
// A child SOP keeps its own sandbox (lock, container, environment install)
// but works in the checkout of the run that owns the workspace.
func (m *Manager) AcquireRunIn(ctx context.Context, runID, workspaceRunID string, profile domain.SandboxProfile, requirements SandboxRequirements, credentials RunCredentials, resources SandboxResources) (Sandbox, error) {
	if strings.TrimSpace(workspaceRunID) == "" {
		workspaceRunID = runID
	}
	return m.open(ctx, safeID(runID), safeID(workspaceRunID), profile, false, false, requirements, credentials, resources)
}

func (m *Manager) open(ctx context.Context, id, dir string, profile domain.SandboxProfile, wipe, ephemeral bool, requirements SandboxRequirements, credentials RunCredentials, resources SandboxResources) (Sandbox, error) {
	lock, err := m.acquireLock(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("sandbox lock: %w", err)
	}
	// Ephemeral sandboxes wipe their directory on open and close, so they must
	// exclude all other users for their entire lifetime.
	workspaceLock, err := m.acquireWorkspaceUse(ctx, dir, wipe || ephemeral)
	if err != nil {
		_ = unlockFile(lock)
		return nil, fmt.Errorf("workspace lock: %w", err)
	}
	fail := func(err error) (Sandbox, error) {
		_ = unlockFile(workspaceLock)
		_ = unlockFile(lock)
		return nil, err
	}
	if profile.Driver, err = domain.ResolveSandboxDriver(profile.Driver, goruntime.GOOS); err != nil {
		return fail(err)
	}
	workspace := filepath.Join(m.root, dir)
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
	profile.Environment = overlayEnvironment(profile.Environment, activated.Overlay)
	profile, resources.Dockerfile = isolateLocalDriver(profile, resources.Dockerfile)
	sandbox, err := createDriverSandbox(ctx, "sandbox", id, workspace, profile, requirements, resources.Dockerfile)
	if err != nil {
		return fail(err)
	}
	managed := &managedSandbox{Sandbox: sandbox, slots: m.slots, lock: lock, workspaceLock: workspaceLock, ephemeral: ephemeral, workspace: workspace, credentials: credentials}
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
	lock, acquired, err := m.tryAcquireLock(id)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrSandboxInUse
	}
	defer unlockFile(lock)
	workspaceLock, acquired, err := m.tryAcquireWorkspaceDeletion(id)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrSandboxInUse
	}
	defer unlockFile(workspaceLock)
	// Removing the directory alone leaves the container running against a bind
	// that no longer exists. Docker reports such a container as active, so no
	// prune can reclaim its writable layer afterwards.
	removeDockerSandbox(context.Background(), dockerContainerName(id), tempDockerImageName(id))
	_, err = m.removeMatching(func(name string) bool {
		return name == id || strings.HasPrefix(name, id+"-") || name == "persist-"+id || strings.HasPrefix(name, "persist-"+id+"-")
	}, true, id)
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
		lock, acquired, err := m.tryAcquireLock(id)
		if err != nil || !acquired {
			return
		}
		defer unlockFile(lock)
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
		// A child SOP's container is named after the child but binds the
		// workspace of the run owning the checkout; its lock is the child's.
		if _, ok := found[dockerContainerName(id)]; ok {
			if !m.workspaceInUse(id) {
				out = append(out, runID)
			}
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
		id := strings.TrimPrefix(container, dockerSandboxPrefix)
		lock, acquired, err := m.tryAcquireLock(id)
		if err != nil {
			return removed, err
		}
		if !acquired {
			continue
		}
		removeDockerSandbox(ctx, container, tempDockerImageName(workspace))
		_ = unlockFile(lock)
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
	}, false)
}

func (m *Manager) removeMatching(match func(name string) bool, reportBusy bool, alreadyLocked ...string) (int, error) {
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
		var lock *os.File
		if len(alreadyLocked) == 0 || name != alreadyLocked[0] {
			var acquired bool
			lock, acquired, err = m.tryAcquireLock(name)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			if !acquired {
				if reportBusy && first == nil {
					first = ErrSandboxInUse
				}
				continue
			}
		}
		var workspaceLock *os.File
		if len(alreadyLocked) == 0 || name != alreadyLocked[0] {
			var acquired bool
			var lockErr error
			workspaceLock, acquired, lockErr = m.tryAcquireWorkspaceDeletion(name)
			if lockErr != nil || !acquired {
				if lock != nil {
					_ = unlockFile(lock)
				}
				if lockErr != nil && first == nil {
					first = lockErr
				} else if !acquired && reportBusy && first == nil {
					first = ErrSandboxInUse
				}
				continue
			}
		}
		removeErr := os.RemoveAll(dir)
		_ = unlockFile(workspaceLock)
		if lock != nil {
			_ = unlockFile(lock)
		}
		if removeErr != nil {
			if first == nil {
				first = removeErr
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
	workspaceLock *os.File
	ephemeral     bool
	workspace     string
	credentials   RunCredentials
	execMu        sync.Mutex
	credentialsMu sync.RWMutex
}

// Unwrap returns the underlying driver sandbox so wrappers remain transparent
// to runtime helpers such as browser provisioning and conversation CLI setup.
func (s *managedSandbox) Unwrap() Sandbox {
	if s == nil {
		return nil
	}
	return s.Sandbox
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
			if removeErr := os.RemoveAll(s.workspace); err == nil {
				err = removeErr
			}
		}
		_ = unlockFile(s.lock)
		_ = unlockFile(s.workspaceLock)
	})
	return err
}

// Detach drops the workspace flock without docker rm. Deploy drain parks the
// run so the next worker can reuse the same bind-mounted container.
func (s *managedSandbox) Detach() error {
	s.once.Do(func() {
		_ = unlockFile(s.lock)
		_ = unlockFile(s.workspaceLock)
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

// CredentialSnapshot returns the current credential snapshot. Read-only use:
// preparation code validating a node's declared credentials up front.
func (s *managedSandbox) CredentialSnapshot() RunCredentials {
	s.credentialsMu.RLock()
	defer s.credentialsMu.RUnlock()
	return s.credentials
}

// sandboxCredentialSnapshot lets runtime preparation code read a sandbox's
// credentials without widening the public Sandbox interface.
type sandboxCredentialSnapshot interface{ CredentialSnapshot() RunCredentials }

func (s *managedSandbox) Exec(ctx context.Context, command CommandSpec, sink EventSink) (string, error) {
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.slots }()
	s.credentialsMu.RLock()
	credentials := s.credentials
	s.credentialsMu.RUnlock()
	command, closeGit, err := prepareRunGitBinding(ctx, s.Sandbox, command, credentials)
	if err != nil {
		return "", err
	}
	defer closeGit()
	if credentials.GitHTTPCredentials != nil {
		credentials.GitHTTPHosts = runGitHosts(credentials)
	}
	// Binding writes the credential helpers into the workspace; parallel
	// branches then run their commands concurrently.
	s.execMu.Lock()
	bound, err := bindCommand(ctx, s.Sandbox, s.workspace, command, credentials)
	s.execMu.Unlock()
	if err != nil {
		return "", err
	}
	cleanup, err := bindCommandGitSSH(ctx, s.Sandbox, s.workspace, bound, credentials)
	if err != nil {
		return "", err
	}
	defer cleanup()
	defer scrubRunGitCredentials(ctx, s.Sandbox, s.workspace, bound, credentials)
	out, err := s.Sandbox.Exec(ctx, bound, runGitEventSink(sink, credentials))
	return redactRunGitSecrets(out, credentials), err
}

// overlayEnvironment layers an activated environment's overlay under env,
// except PATH: the overlay's PATH wins so its tools stay first on the path.
// An empty overlay returns env unchanged.
func overlayEnvironment(env, overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return env
	}
	merged := mergeEnvironmentMaps(overlay, env)
	if path := overlay["PATH"]; path != "" {
		merged["PATH"] = path
	}
	return merged
}

// createDriverSandbox builds the sandbox for profile.Driver. kind only names
// the caller in the unsupported-driver error.
func createDriverSandbox(ctx context.Context, kind, id, workspace string, profile domain.SandboxProfile, requirements SandboxRequirements, dockerfile domain.DockerfileDefinition) (Sandbox, error) {
	switch strings.TrimSpace(profile.Driver) {
	case "", "local":
		return &localSandbox{id: id, workspace: workspace, environment: profile.Environment}, nil
	case "seatbelt":
		return createSeatbelt(id, workspace, profile, requirements)
	case "bwrap":
		return createBwrap(id, workspace, profile, requirements)
	case "docker":
		return createDocker(ctx, id, workspace, profile, requirements, dockerfile)
	default:
		return nil, fmt.Errorf("unsupported %s driver %q", kind, profile.Driver)
	}
}
