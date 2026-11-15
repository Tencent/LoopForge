package tracker

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"agentflow/internal/domain"
)

// pullHost is the pull-request API of a public Git host addressed by a
// prefixed project ("github:owner/name"). The delivery flow (find, create,
// merge, refresh) lives once in this file; a host only maps its own API to
// these primitives. Gongfeng goes through its MCP gateway instead.
type pullHost interface {
	// findPull returns the newest pull request whose source is branch, into
	// target when target is set, in any state.
	findPull(branch, target string) (hostedPull, bool, error)
	createPull(branch, target, title, body string) (hostedPull, error)
	// mergePull merges p with a merge commit and fails unless the host
	// confirms the merge.
	mergePull(p hostedPull) error
	pull(number int) (hostedPull, error)
}

// hostedPull is a host's pull request reduced to what delivery needs.
type hostedPull struct {
	Number       int
	State        string // "opened", "merged" or "closed"
	TargetBranch string
	HeadSHA      string
	MergedAt     time.Time // the host's merge time; zero until merged
}

// ref is the "#N" reference returned to delivery callers.
func (p hostedPull) ref() string { return "#" + strconv.Itoa(p.Number) }

func (p hostedPull) delivery(project string) domain.DeliveryMR {
	return domain.DeliveryMR{Project: project, IID: p.Number, URL: domain.GongfengMRURL(project, p.Number), State: p.State, GongfengState: p.State, TargetBranch: p.TargetBranch}
}

// isPullHostProject reports whether project is served by a pullHost rather
// than the Gongfeng gateway.
func isPullHostProject(project string) bool {
	return strings.HasPrefix(project, "github:")
}

// pullHostFor returns the host serving project; ok is false for Gongfeng.
func (s *Service) pullHostFor(project string) (host pullHost, ok bool, err error) {
	if strings.HasPrefix(project, "github:") {
		g, err := s.githubClient(project)
		if err != nil {
			return nil, true, err
		}
		return g, true, nil
	}
	return nil, false, nil
}

func (s *Service) findHostedBranch(project, branch string) (domain.DeliveryMR, bool, error) {
	h, _, err := s.pullHostFor(project)
	if err != nil {
		return domain.DeliveryMR{}, false, err
	}
	p, ok, err := h.findPull(branch, "")
	if err != nil || !ok {
		return domain.DeliveryMR{}, ok, err
	}
	return p.delivery(project), true, nil
}

// mergeHostedBranch reuses a merged pull request, opens a new one when none
// is open (an empty target only merges an existing one) and merges it.
func (s *Service) mergeHostedBranch(project, branch, target, title, description string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("source branch is required")
	}
	if branch == target {
		return "", nil
	}
	h, _, err := s.pullHostFor(project)
	if err != nil {
		return "", err
	}
	p, found, err := h.findPull(branch, target)
	if err != nil {
		return "", err
	}
	if found && p.State == "merged" {
		return p.ref(), nil
	}
	if !found || p.State == "closed" {
		if target == "" {
			return "", ErrNoOpenMR
		}
		if title == "" {
			title = "LoopForge: " + branch + " → " + target
		}
		if p, err = h.createPull(branch, target, title, description); err != nil {
			return "", err
		}
	}
	if err := h.mergePull(p); err != nil {
		return "", err
	}
	return p.ref(), nil
}

func (s *Service) refreshHostedDelivery(store MRDeliveryStore, project string, number int) error {
	h, _, err := s.pullHostFor(project)
	if err != nil {
		return err
	}
	p, err := h.pull(number)
	if err != nil {
		return err
	}
	return store.UpdateMRDeliveryState(project, number, p.State, p.TargetBranch, p.MergedAt, time.Now().UTC())
}
