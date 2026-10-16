package domain

import (
	"path"
	"strconv"
	"strings"
	"time"
)

// Runner connection model (docs: cli-connect.md).
//
// A Runner is a long-lived daemon on a user machine or CVM that connects to
// the control plane over outbound HTTPS only. The platform never opens an
// inbound port to the machine. A RunnerRuntime is one Agent CLI (claude,
// codex, codebuddy, ...) discovered on that machine. RunnerTask is a durable
// execution unit persisted by the control plane; a connection drop never
// loses a task.

const (
	RunnerStatusOnline  = "online"
	RunnerStatusOffline = "offline"
)

// RunnerTask states. LEASED sits between QUEUED and RUNNING so a claim that
// never starts is still recoverable through lease expiry.
const (
	RunnerTaskQueued   = "queued"
	RunnerTaskLeased   = "leased"
	RunnerTaskRunning  = "running"
	RunnerTaskSuccess  = "success"
	RunnerTaskFailed   = "failed"
	RunnerTaskCanceled = "canceled"
)

// RunnerTaskDesiredState is carried on lease renewal responses so a cancel
// reaches the runner without an extra polling channel.
const (
	RunnerTaskDesiredCancel        = "cancel"
	RunnerTaskActionCleanupRun     = "cleanup_run"
	RunnerTaskActionArchivePreview = "archive_preview"
	RunnerTaskActionPullWorkBranch = "pull_work_branch"
	RunnerTaskActionPushWorkBranch = "push_work_branch"
	RunnerTaskActionIntegrate      = "integrate"
	RunnerTaskActionAlignBaseline  = "align_baseline"
)

// RunnerVersionWorkBranchSync is the first runner version that executes the
// pull/push work-branch actions. Older runners would misread them as an Agent
// invocation, so the engine must not queue them there.
const RunnerVersionWorkBranchSync = "0.2.0"

// RunnerVersionReportedOutputs is the first runner version that writes
// Invocation.Inputs, clears the gate result before each invocation, and
// reports RunnerTask.Outputs. The engine validates and registers artifacts
// from those outputs instead of a control-plane copy of the workspace.
const RunnerVersionReportedOutputs = "0.3.0"

// RunnerVersionIntegrate is the first runner version that executes the
// integrate action: merge the latest integration branch into the work branch
// in the run's checkout and push it (and, when asked, the integration branch).
const RunnerVersionIntegrate = "0.4.0"

// RunnerVersionAlignBaseline is the first runner version that executes the
// align_baseline action: merge a repository's default branch into its
// integration branch in a throwaway clone and push it, or report conflicts.
const RunnerVersionAlignBaseline = "0.5.0"

// RunnerVersionWorkspacePreparation is the first runner that prepares restored
// workspaces and binds interactive terminals to the frozen run sandbox.
const RunnerVersionWorkspacePreparation = "0.6.0"

type Runner struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	RunnerVersion  string   `json:"runnerVersion"`
	Labels         []string `json:"labels,omitempty"`
	MaxConcurrency int      `json:"maxConcurrency"`
	// RunLimit caps concurrently admitted top-level SOP runs on a personal
	// machine, counted like the project limit. 0 means unlimited.
	RunLimit   int             `json:"runLimit"`
	Status     string          `json:"status"`
	Runtimes   []RunnerRuntime `json:"runtimes,omitempty"`
	LastSeenAt *time.Time      `json:"lastSeenAt,omitempty"`
	// OwnerUserID is the user who bound this machine via bind-cli.sh; the
	// personal 执行器 page lists machines by owner.
	OwnerUserID      int64      `json:"ownerUserId,omitempty"`
	SessionTokenHash string     `json:"-"`
	SessionExpiresAt *time.Time `json:"-"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// PublicRunner strips credential material before returning runner data to
// management callers. Session hashes never leave the store.
func PublicRunner(r Runner) Runner {
	r.SessionTokenHash = ""
	r.SessionExpiresAt = nil
	return r
}

func PublicRunners(items []Runner) []Runner {
	out := make([]Runner, len(items))
	for i, r := range items {
		out[i] = PublicRunner(r)
	}
	return out
}

// SupportsWorkspacePreparation guards the transient control protocol: older
// daemons ignore the sandbox fields and must never receive a terminal open.
func (r Runner) SupportsWorkspacePreparation() bool {
	return versionAtLeast(r.RunnerVersion, RunnerVersionWorkspacePreparation)
}

// SupportsAlignBaseline reports whether the runner executes align_baseline.
func (r Runner) SupportsAlignBaseline() bool {
	return versionAtLeast(r.RunnerVersion, RunnerVersionAlignBaseline)
}

// SupportsIntegrate reports whether the runner executes the integrate action.
func (r Runner) SupportsIntegrate() bool {
	return versionAtLeast(r.RunnerVersion, RunnerVersionIntegrate)
}

// SupportsWorkBranchSync reports whether the runner last reported a version
// that understands the pull/push work-branch actions.
func (r Runner) SupportsWorkBranchSync() bool {
	return versionAtLeast(r.RunnerVersion, RunnerVersionWorkBranchSync)
}

// SupportsReportedOutputs reports whether the runner returns node outputs
// (files, repository patches, workspace facts) with its task completion.
func (r Runner) SupportsReportedOutputs() bool {
	return versionAtLeast(r.RunnerVersion, RunnerVersionReportedOutputs)
}

// versionAtLeast compares dotted numeric versions; malformed input is older.
func versionAtLeast(have, want string) bool {
	parse := func(v string) ([]int, bool) {
		parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
		out := make([]int, len(parts))
		for i, part := range parts {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, false
			}
			out[i] = n
		}
		return out, true
	}
	h, ok := parse(have)
	if !ok {
		return false
	}
	w, _ := parse(want)
	for i := 0; i < len(w); i++ {
		hv := 0
		if i < len(h) {
			hv = h[i]
		}
		if hv != w[i] {
			return hv > w[i]
		}
	}
	return true
}

// SupportsProvider reports whether the runner last reported a runtime for the
// provider. A runner with no runtimes still accepts tasks only when the task
// does not target it explicitly.
func (r Runner) SupportsProvider(provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return false
	}
	for _, rt := range r.Runtimes {
		if strings.EqualFold(rt.Provider, provider) && rt.Status != RuntimeStatusDisabled {
			return true
		}
	}
	return false
}

// RuntimeAuthMode returns the configured credential source for a provider.
// CodeBuddy has no reusable local-login flow, so its default is the
// responsible user's personal credential; other CLIs default to local login.
func (r Runner) RuntimeAuthMode(provider string) string {
	provider = strings.TrimSpace(provider)
	for _, rt := range r.Runtimes {
		if strings.EqualFold(rt.Provider, provider) {
			if rt.AuthMode == RuntimeAuthPersonal {
				return RuntimeAuthPersonal
			}
			if rt.AuthMode == RuntimeAuthLocal {
				return RuntimeAuthLocal
			}
			break
		}
	}
	if strings.EqualFold(provider, "codebuddy") {
		return RuntimeAuthPersonal
	}
	return RuntimeAuthLocal
}

const (
	RuntimeStatusOnline   = "online"
	RuntimeStatusOffline  = "offline"
	RuntimeStatusDisabled = "disabled"
	RuntimeAuthLocal      = "local"
	RuntimeAuthPersonal   = "personal"
)

// RunnerRuntime is one discovered Agent CLI on the runner machine. Credentials
// for the CLI stay on that machine; the control plane only learns the binary
// path, version, and capabilities.
type RunnerRuntime struct {
	Provider     string   `json:"provider"`
	BinaryPath   string   `json:"binaryPath"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Status       string   `json:"status"`
	// AuthMode is controlled by the platform. "local" uses the CLI login
	// state on this machine; "personal" injects task-scoped user credentials.
	AuthMode string `json:"authMode,omitempty"`
}

type RunnerTask struct {
	ID           string     `json:"id"`
	RunnerID     string     `json:"runnerId,omitempty"` // empty = any compatible runner
	Provider     string     `json:"provider"`
	Prompt       string     `json:"prompt"`
	Workspace    string     `json:"workspace,omitempty"` // relative to the runner workspace root, or an absolute path
	Args         []string   `json:"args,omitempty"`      // optional extra CLI args before the prompt
	Status       string     `json:"status"`
	DesiredState string     `json:"desiredState,omitempty"`
	LeaseOwner   string     `json:"leaseOwner,omitempty"`
	LeaseUntil   *time.Time `json:"leaseUntil,omitempty"`
	Attempt      int        `json:"attempt"`
	MaxAttempts  int        `json:"maxAttempts"`
	ExitCode     *int       `json:"exitCode,omitempty"`
	Result       string     `json:"result,omitempty"`
	Error        string     `json:"error,omitempty"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	// RunID/NodeID link a task to a workflow execution. Runner events for a
	// linked task are bridged into the run timeline (adapter runner-v1).
	RunID      string            `json:"runId,omitempty"`
	NodeID     string            `json:"nodeId,omitempty"`
	Invocation *RunnerInvocation `json:"invocation,omitempty"`
	// Sandbox is the workflow sandbox definition resolved by the control
	// plane. The runner creates it locally so Docker/seatbelt/bwrap isolation
	// lives on the same machine that executes the Agent CLI.
	Sandbox *RunnerSandboxSpec `json:"sandbox,omitempty"`
	// Command is a terminal task: the runner executes it with
	// `sh -lc <command>` in the task workspace. Used for workflow scripts and
	// verification gates; never for Agent CLI invocations.
	Command string `json:"command,omitempty"`
	// Action identifies a built-in daemon operation that must not be executed
	// through a shell. cleanup_run removes the finished run's runner-side
	// sandbox resources and workspace after all Agent/gate tasks complete.
	// archive_preview packs a declared static directory and streams it to the
	// control-plane preview store. pull_work_branch / push_work_branch bring
	// the checkouts of Invocation.Repositories flagged SyncWorkBranch up to,
	// or onto, their remote work branch; Result is a JSON []RunnerWorkBranchHead.
	// integrate merges the latest IntegrateBranch into the work branch of
	// each Invocation.Repositories entry that names one and pushes it; Result
	// is a JSON []RunnerIntegrationResult. align_baseline merges the
	// DefaultBranch of the single Invocation.Repositories entry into its
	// IntegrateBranch in a throwaway clone; Result is a JSON RunnerAlignResult.
	Action string `json:"action,omitempty"`
	// WorkBranchMessage is the commit message for push_work_branch.
	WorkBranchMessage string `json:"workBranchMessage,omitempty"`
	// IntegrationLabel names the run in integrate commit messages.
	IntegrationLabel string `json:"integrationLabel,omitempty"`
	// Artifacts is the artifact content uploaded by the runner at completion
	// (name → file content). The engine writes it into the run workspace.
	Artifacts map[string]string `json:"artifacts,omitempty"`
	// Outputs is reported by runners >= RunnerVersionReportedOutputs for
	// tasks whose invocation asks for it, on success and on failure.
	Outputs *RunnerTaskOutputs `json:"outputs,omitempty"`
	// PreviewArchive is set on archive_preview tasks. Upload tokens are
	// stripped from management responses.
	PreviewArchive *RunnerPreviewArchiveSpec `json:"previewArchive,omitempty"`
}

// RunnerSandboxRequirements freezes the union needed by every run node, so
// interactive terminals and script/agent tasks use the same sandbox identity.
type RunnerSandboxRequirements struct {
	CLIStates   []RunnerCLIStateRequirement `json:"cliStates,omitempty"`
	PrivateHome bool                        `json:"privateHome,omitempty"`
	BunFS       bool                        `json:"bunFS,omitempty"`
}
type RunnerCLIStateRequirement struct {
	Directory string   `json:"directory"`
	Files     []string `json:"files,omitempty"`
}

type RunnerSandboxSpec struct {
	Requirements *RunnerSandboxRequirements `json:"requirements,omitempty"`
	Profile      SandboxProfile             `json:"profile"`
	Dockerfile   *DockerfileDefinition      `json:"dockerfile,omitempty"`
	Environment  *EnvironmentDefinition     `json:"environment,omitempty"`
}

// PublicRunnerTask strips task-scoped credentials from management responses.
// The authenticated claiming runner receives the original task payload.
func PublicRunnerTask(task RunnerTask) RunnerTask {
	if task.PreviewArchive != nil {
		copySpec := *task.PreviewArchive
		copySpec.UploadToken = ""
		task.PreviewArchive = &copySpec
	}
	if task.Invocation == nil {
		return task
	}
	copyInvocation := *task.Invocation
	copyInvocation.CodeBuddyAPIKey = ""
	copyInvocation.CodeBuddyInternetEnvironment = ""
	copyInvocation.CodexAPIKey = ""
	copyInvocation.CodexBaseURL = ""
	copyInvocation.ClaudeCodeAPIKey = ""
	copyInvocation.ClaudeCodeBaseURL = ""
	copyInvocation.TaihuPAT = ""
	if copyInvocation.Repository != nil {
		copyRepository := *copyInvocation.Repository
		copyRepository.GitToken = ""
		copyInvocation.Repository = &copyRepository
	}
	if len(copyInvocation.Repositories) > 0 {
		copyRepositories := make([]RunnerRepository, len(copyInvocation.Repositories))
		for i, repository := range copyInvocation.Repositories {
			repository.GitToken = ""
			copyRepositories[i] = repository
		}
		copyInvocation.Repositories = copyRepositories
	}
	task.Invocation = &copyInvocation
	return task
}

// RunnerInvocation is the self-contained package a runner daemon needs to
// reconstruct one Agent node invocation in its own task workspace. Provider
// credentials are included only when the runtime selects personal credentials
// and are scrubbed from durable storage when the task finishes.
type RunnerInvocation struct {
	ArchivedFiles   []WorkspaceArtifactFile `json:"archivedFiles,omitempty"`
	Concurrent      bool                    `json:"concurrent,omitempty"`
	RunID           string                  `json:"runId,omitempty"`
	NodeID          string                  `json:"nodeId,omitempty"`
	InvocationID    string                  `json:"invocationId,omitempty"`
	ResumeSessionID string                  `json:"resumeSessionId,omitempty"`
	GateResultPath  string                  `json:"gateResultPath,omitempty"`
	Executor        ExecutorDefinition      `json:"executor"`
	Agent           AgentDefinition         `json:"agent"`
	Assets          AgentAssetBundle        `json:"assets"`
	// Artifacts are the node's artifact contracts: the runner uploads the
	// content of each declared path when it completes, and the engine writes
	// them into the run workspace so verification and handoff keep working.
	Artifacts         []ArtifactContract `json:"artifacts,omitempty"`
	ArtifactDirectory string             `json:"artifactDirectory,omitempty"`
	// Inputs are upstream artifacts the runner writes read-only into the
	// task workspace before the invocation (paths under .agentflow/inputs).
	Inputs []RunnerInputFile `json:"inputs,omitempty"`
	// ReportOutputs asks the runner to return RunnerTask.Outputs: the
	// OutputFiles that exist, the repository-patch inventory of every
	// checkout, and workspace facts. OutputFiles are workspace-relative
	// paths resolved by the control plane. RecoverOutputs lets the runner
	// move a missing output file found elsewhere in the workspace into
	// place, as the control plane does for its own sandboxes.
	ReportOutputs                bool     `json:"reportOutputs,omitempty"`
	OutputFiles                  []string `json:"outputFiles,omitempty"`
	RecoverOutputs               bool     `json:"recoverOutputs,omitempty"`
	HumanContext                 string   `json:"humanContext,omitempty"`
	CredentialSource             string   `json:"credentialSource,omitempty"`
	CodeBuddyAPIKey              string   `json:"codebuddyApiKey,omitempty"`
	CodeBuddyInternetEnvironment string   `json:"codebuddyInternetEnvironment,omitempty"`
	CodexAPIKey                  string   `json:"codexApiKey,omitempty"`
	CodexBaseURL                 string   `json:"codexBaseUrl,omitempty"`
	ClaudeCodeAPIKey             string   `json:"claudeCodeApiKey,omitempty"`
	ClaudeCodeBaseURL            string   `json:"claudeCodeBaseUrl,omitempty"`
	TaihuPAT                     string   `json:"taihuPat,omitempty"`
	// MCPCallServers is the project/personal HTTP MCP set for `agentflow mcp call`
	// in scripts, verification gates, and Agent hooks. It is unbound; TaihuPAT
	// is applied in memory on the runner. Agent Assets stay allowlist-scoped.
	MCPCallServers []MCPServerDefinition `json:"mcpCallServers,omitempty"`
	// GitUserName and GitUserEmail are the frozen requirement owner's commit
	// identity. The runner applies them to each checkout with --local scope.
	GitUserName  string `json:"gitUserName,omitempty"`
	GitUserEmail string `json:"gitUserEmail,omitempty"`
	// Repository makes the runner machine check out the run's code base into
	// the task workspace, so the sandbox and workspace live on that machine.
	// Legacy single-repository field; Repositories is authoritative when set.
	Repository *RunnerRepository `json:"repository,omitempty"`
	// Repositories lists every repository the run touches (multi-repository
	// support). Each entry checks out into its Dir under the task workspace.
	Repositories []RunnerRepository `json:"repositories,omitempty"`
}

// RunnerRepository describes the checkout a runner machine should create.
// GitToken is only set for private remotes and is cleared from the stored
// payload once the task completes.
type RunnerRepository struct {
	RepositoryID string `json:"repositoryId,omitempty"`
	Name         string `json:"name,omitempty"`
	CloneURL     string `json:"cloneUrl,omitempty"`
	Ref          string `json:"ref,omitempty"`
	WorkBranch   string `json:"workBranch,omitempty"`
	GitToken     string `json:"gitToken,omitempty"`
	// Dir is the workspace-relative checkout directory (empty is only kept
	// for persisted legacy runs that already checked out at the workspace root).
	Dir string `json:"dir,omitempty"`
	// SyncWorkBranch is the control plane's decision that WorkBranch may
	// receive intermediate node commits (it is not a shared base/integration
	// branch). Only such repositories are touched by the work-branch actions.
	SyncWorkBranch bool `json:"syncWorkBranch,omitempty"`
	// IntegrateBranch is set only on integrate actions: the branch whose
	// latest state is merged into WorkBranch. PushIntegrate additionally
	// pushes the merged result to it (no Gongfeng MR is used).
	IntegrateBranch string `json:"integrateBranch,omitempty"`
	PushIntegrate   bool   `json:"pushIntegrate,omitempty"`
	// DefaultBranch is set only on align_baseline: the branch merged into
	// IntegrateBranch.
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

// RunnerInputFile is one file written into the task workspace before a run.
type RunnerInputFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// RunnerTaskOutputs is what a runner reports about the task workspace after
// an invocation or script.
type RunnerTaskOutputs struct {
	// Files maps a workspace-relative OutputFiles path to its content. Missing
	// files and non-regular files are absent; oversized ones are in TooLarge.
	Files    map[string]string `json:"files,omitempty"`
	TooLarge []string          `json:"tooLarge,omitempty"`
	// Patches maps a repository checkout dir ("" is the workspace root) to
	// its repository-patch inventory. Unchanged checkouts are absent.
	Patches map[string]string `json:"patches,omitempty"`
	// Recovered lists "path ← source" moves made for RecoverOutputs.
	Recovered []string              `json:"recovered,omitempty"`
	Facts     *RunnerWorkspaceFacts `json:"facts,omitempty"`
}

// RunnerWorkspaceFacts are workspace properties the control plane used to
// read from its own copy of the workspace.
type RunnerWorkspaceFacts struct {
	Frontend         bool   `json:"frontend"`
	FrontendEvidence string `json:"frontendEvidence,omitempty"`
}

// RunnerWorkBranchHead reports a work branch that a pull/push action moved.
// RunnerIntegrationResult is the outcome of an integrate action for one
// repository. Conflicts lists files left conflicted; the merge then stays in
// progress in the checkout for a repair node to finish.
type RunnerIntegrationResult struct {
	RepositoryID    string   `json:"repositoryId,omitempty"`
	Name            string   `json:"name,omitempty"`
	WorkBranch      string   `json:"workBranch"`
	IntegrateBranch string   `json:"integrateBranch"`
	Head            string   `json:"head,omitempty"`
	Conflicts       []string `json:"conflicts,omitempty"`
	PushedIntegrate bool     `json:"pushedIntegrate,omitempty"`
}

// RunnerAlignResult is the outcome of align_baseline. With Conflicts the
// merge was aborted and nothing was pushed.
type RunnerAlignResult struct {
	Head      string          `json:"head,omitempty"`
	Conflicts []string        `json:"conflicts,omitempty"`
	Blocks    []ConflictBlock `json:"blocks,omitempty"`
}

type RunnerWorkBranchHead struct {
	RepositoryID string `json:"repositoryId,omitempty"`
	Name         string `json:"name,omitempty"`
	WorkBranch   string `json:"workBranch"`
	Head         string `json:"head"`
}

// RunnerTaskEvent is one batched output record reported by the runner. Seq is
// assigned by the runner and used with the task id for idempotency, so an
// HTTP retry never duplicates log data.
type RunnerTaskEvent struct {
	Seq  int64     `json:"seq"`
	Type string    `json:"type"` // stdout, stderr, system, observation
	Data string    `json:"data"`
	Ts   time.Time `json:"ts"`
}

// SafeWorkspaceRelPath reports whether p is a clean, slash-separated path
// inside a workspace (not absolute, not the root, no parent segments).
func SafeWorkspaceRelPath(p string) bool {
	if p == "" || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
		return false
	}
	clean := path.Clean(p)
	return clean == p && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
