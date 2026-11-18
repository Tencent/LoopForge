// Package verifysidecar implements the hash-only verification sidecar that
// LoopForge SOP nodes talk to. It is a stub for the AegisOS verification
// service: the wire contract (tree hash, spec hash, toolchain/build
// fingerprints, verdict and delivery binding) matches AegisOS semantics so the
// real service can replace it without changing the SOP.
//
// The service is meant to run as a same-host process next to LoopForge and is
// bound to loopback. It never touches the network and only reads repositories
// under Root.
package verifysidecar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agentflow/internal/critique"
	"agentflow/internal/verification"
)

type Server struct {
	// Root bounds every repo path the service will read.
	Root string
	// Critique is the model endpoint used by /critique. An unconfigured
	// endpoint answers REJECT, never PASS.
	Critique critique.Config
	// AllowTestCommand enables running the request's testCommand. Off by
	// default because the request is operator-supplied shell.
	AllowTestCommand bool
	// MavenCacheDir, when set, is mounted at /root/.m2 across runs.
	MavenCacheDir string
	// AcceptanceImage overrides DefaultAcceptanceImage.
	AcceptanceImage string
	// GitTimeout bounds one git invocation.
	GitTimeout time.Duration
	// TestTimeout bounds a test command run.
	TestTimeout time.Duration
}

func New(root string) *Server {
	return &Server{
		Root:        root,
		GitTimeout:  30 * time.Second,
		TestTimeout: 10 * time.Minute,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /verify", s.verify)
	mux.HandleFunc("POST /critique", s.critique)
	mux.HandleFunc("POST /delivery-check", s.deliveryCheck)
	return mux
}

type verifyRequest struct {
	Repo        string   `json:"repo"`
	Ref         string   `json:"ref"`
	Spec        string   `json:"spec"`
	BaseCommit  string   `json:"baseCommit,omitempty"`
	TestRoots   []string `json:"testRoots,omitempty"`
	MavenImage  string   `json:"mavenImage,omitempty"`
	Worktree    bool     `json:"worktree,omitempty"`
	TestCommand string   `json:"testCommand,omitempty"`
	// AcceptanceSpec is a directory of generated JUnit sources; when set the
	// acceptance image compiles and runs them and its verdict joins the result.
	AcceptanceSpec  string `json:"acceptanceSpec,omitempty"`
	AcceptanceImage string `json:"acceptanceImage,omitempty"`
}

type acceptance struct {
	Status     string                          `json:"status"` // PASS, FAIL, SKIPPED
	ExitCode   int                             `json:"exitCode,omitempty"`
	DurationMs int64                           `json:"durationMs,omitempty"`
	Reason     string                          `json:"reason,omitempty"`
	Surefire   *verification.SurefireSummary   `json:"surefire,omitempty"`
	JUnit      *verification.AcceptanceOutcome `json:"junit,omitempty"`
}

type verifyResponse struct {
	TreeHash    string     `json:"treeHash"`    // 40-hex git tree object (sha1), as in AegisOS
	SpecHash    string     `json:"specHash"`    // 64-hex sha256 of the spec at that tree
	ToolchainFp string     `json:"toolchainFp"` // 64-hex sha256
	BuildFp     string     `json:"buildFp"`     // 64-hex sha256
	Acceptance  acceptance `json:"acceptance"`
}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !decode(w, r, &req) {
		return
	}
	repo, err := s.repo(req.Repo)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = "HEAD"
	}
	var treeHash string
	if req.Worktree {
		treeHash, err = verification.WorkTree(r.Context(), repo)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("hash working tree: %w", err))
			return
		}
	} else {
		treeHash, err = s.gitOutput(repo, "rev-parse", "--verify", ref+"^{tree}")
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("resolve %s^{tree}: %w", ref, err))
			return
		}
	}
	if !isGitTreeHash(treeHash) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("resolved tree hash %q is not a git sha1", treeHash))
		return
	}
	spec := strings.TrimSpace(req.Spec)
	if spec == "" {
		writeError(w, http.StatusBadRequest, errors.New("spec is required: it is the acceptance spec the tree is verified against"))
		return
	}
	if !safeRelative(spec) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("spec %q must be a relative path inside the repo", spec))
		return
	}
	specContent, err := s.gitOutputBytes(repo, "show", ref+":"+spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("read spec %s at %s: %w", spec, ref, err))
		return
	}
	buildFp, err := verification.BuildFingerprint(r.Context(), repo, ref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response := verifyResponse{
		TreeHash:    treeHash,
		SpecHash:    sha256Hex(specContent),
		ToolchainFp: verification.ToolchainFingerprint(req.MavenImage),
		BuildFp:     buildFp,
		Acceptance:  s.runAcceptance(r.Context(), repo, req),
	}
	writeJSON(w, http.StatusOK, response)
}

// runAcceptance runs the test command inside the Maven toolchain container and
// decides the verdict from the surefire reports, never from the exit code.
func (s *Server) runAcceptance(ctx context.Context, repo string, req verifyRequest) acceptance {
	result := acceptance{Status: "SKIPPED", Reason: "no testCommand or acceptanceSpec supplied"}
	command := strings.TrimSpace(req.TestCommand)
	spec := strings.TrimSpace(req.AcceptanceSpec)
	if command == "" && spec == "" {
		return result
	}
	if !s.AllowTestCommand {
		return acceptance{Status: "SKIPPED", Reason: "test execution is disabled on this sidecar (start it with -allow-test-command)"}
	}

	if command != "" {
		runDir := repo
		if strings.TrimSpace(req.BaseCommit) != "" || len(req.TestRoots) > 0 {
			dir, cleanup, err := verification.PrepareRunDir(ctx, repo, req.BaseCommit, req.TestRoots)
			if err != nil {
				return acceptance{Status: "FAIL", Reason: err.Error()}
			}
			defer cleanup()
			runDir = dir
		}
		runner := verification.MavenRunner{Image: req.MavenImage, Timeout: s.TestTimeout, CacheDir: s.MavenCacheDir}
		outcome, err := runner.RunTests(ctx, runDir, command)
		if err != nil {
			return acceptance{Status: "FAIL", Reason: err.Error()}
		}
		result = acceptance{
			Status:     outcome.Status,
			ExitCode:   outcome.ExitCode,
			DurationMs: outcome.DurationMs,
			Reason:     truncate(outcome.Reason, 4000),
			Surefire:   outcome.Surefire,
		}
	}

	if spec != "" {
		dir, err := s.repo(spec)
		if err != nil {
			return acceptance{Status: "FAIL", Reason: "acceptanceSpec: " + err.Error()}
		}
		image := strings.TrimSpace(req.AcceptanceImage)
		if image == "" {
			image = s.AcceptanceImage
		}
		runner := verification.AcceptanceRunner{Image: image, Timeout: s.TestTimeout}
		outcome, err := runner.Run(ctx, dir)
		result.JUnit = &outcome
		if err != nil {
			result.Status, result.Reason = "FAIL", err.Error()
			return result
		}
		if result.Status == "SKIPPED" {
			result.Status, result.Reason = outcome.Status, truncate(outcome.Reason, 4000)
			return result
		}
		if outcome.Status != "PASS" {
			result.Status = "FAIL"
		}
		result.Reason = strings.TrimSpace(result.Reason + "; " + outcome.Reason)
	}
	return result
}

type critiqueRequest struct {
	DiffHash        string                `json:"diffHash"`
	Repo            string                `json:"repo,omitempty"`
	BaseCommit      string                `json:"baseCommit,omitempty"`
	Patch           string                `json:"patch,omitempty"`
	Requirement     string                `json:"requirementTitle,omitempty"`
	RequirementBody string                `json:"requirementBody,omitempty"`
	Clauses         string                `json:"clauses,omitempty"`
	Spec            string                `json:"spec,omitempty"`
	Verification    critique.Verification `json:"verification,omitempty"`
}

func (s *Server) critique(w http.ResponseWriter, r *http.Request) {
	var req critiqueRequest
	if !decode(w, r, &req) {
		return
	}
	hash := strings.ToLower(strings.TrimSpace(req.DiffHash))
	patch := req.Patch
	if strings.TrimSpace(patch) == "" {
		repo, err := s.repo(req.Repo)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("patch or repo is required for critique"))
			return
		}
		computed, diff, err := verification.DiffHash(r.Context(), repo, req.BaseCommit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if hash == "" {
			hash = computed
		} else if hash != computed {
			writeError(w, http.StatusBadRequest, fmt.Errorf("diff hash %s does not match the repository diff %s", hash, computed))
			return
		}
		patch = string(diff)
	}
	if !isSHA256(hash) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("diffHash %q is not a 64-hex sha256", req.DiffHash))
		return
	}
	answer, err := critique.Review(r.Context(), s.Critique, critique.Request{
		DiffHash: hash, Patch: patch,
		Requirement: req.Requirement, RequirementBody: req.RequirementBody,
		Clauses: req.Clauses, Spec: req.Spec, Verification: req.Verification,
	})
	if err != nil {
		// Model/configuration failures still answer with a REJECT verdict so the
		// SOP records the reason instead of failing to parse the answer.
		answer.DiffHash = hash
	}
	writeJSON(w, http.StatusOK, answer)
}

type deliveryCheckRequest struct {
	Repo         string `json:"repo"`
	Commit       string `json:"commit"`
	VerifiedTree string `json:"verifiedTree"`
}

type deliveryCheckResponse struct {
	Ok       bool   `json:"ok"`
	Reason   string `json:"reason"`
	TreeHash string `json:"treeHash,omitempty"`
}

func (s *Server) deliveryCheck(w http.ResponseWriter, r *http.Request) {
	var req deliveryCheckRequest
	if !decode(w, r, &req) {
		return
	}
	repo, err := s.repo(req.Repo)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	commit := strings.TrimSpace(req.Commit)
	verified := strings.ToLower(strings.TrimSpace(req.VerifiedTree))
	if commit == "" || !isGitTreeHash(verified) {
		writeError(w, http.StatusBadRequest, errors.New("commit is required and verifiedTree must be a 40-hex git tree hash"))
		return
	}
	treeHash, err := s.gitOutput(repo, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("resolve %s: %w", commit, err))
		return
	}
	if treeHash != verified {
		writeJSON(w, http.StatusOK, deliveryCheckResponse{Ok: false, TreeHash: treeHash,
			Reason: fmt.Sprintf("commit %s points at tree %s, verified tree is %s", commit, treeHash, verified)})
		return
	}
	writeJSON(w, http.StatusOK, deliveryCheckResponse{Ok: true, TreeHash: treeHash, Reason: "commit tree matches the verified tree"})
}

// repo validates the request path and returns it absolute.
func (s *Server) repo(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("repo is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repo %q is outside the sidecar root %q", abs, root)
	}
	return abs, nil
}

func (s *Server) gitOutput(repo string, args ...string) (string, error) {
	raw, err := s.gitOutputBytes(repo, args...)
	return strings.TrimSpace(string(raw)), err
}

func (s *Server) gitOutputBytes(repo string, args ...string) ([]byte, error) {
	ctx, cancel := timeoutContext(s.GitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, errors.New("use POST"))
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func isGitTreeHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func safeRelative(path string) bool {
	clean := filepath.Clean(path)
	return clean != "." && !filepath.IsAbs(clean) && !strings.HasPrefix(clean, "..")
}

func timeoutContext(limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), limit)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
