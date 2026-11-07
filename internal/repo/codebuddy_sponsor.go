package repo

import (
	"os"
	"strings"

	"agentflow/internal/domain"
)

// Users who have not configured their own CodeBuddy API Key borrow the key of
// the account named by AGENTFLOW_ASSISTANT_SPONSOR_LOGIN, limited to
// SponsoredCodeBuddyModel. It covers chat conversations and SOP runs, so a new
// user can start working before configuring anything; scheduled prompts,
// reports, bots and run evaluation keep requiring the user's own key.
const (
	CodeBuddySponsorLoginEnv = "AGENTFLOW_ASSISTANT_SPONSOR_LOGIN"
	SponsoredCodeBuddyModel  = "deepseek-v4.1-flash"
)

// CodeBuddySponsor returns the account whose key is lent, if one is configured
// and has a key.
func (s *Store) CodeBuddySponsor() (domain.User, bool) {
	login := strings.TrimSpace(os.Getenv(CodeBuddySponsorLoginEnv))
	if login == "" || s == nil {
		return domain.User{}, false
	}
	sponsor, ok := s.UserByLoginName(login)
	if !ok || strings.TrimSpace(sponsor.CodeBuddyAPIKey) == "" {
		return domain.User{}, false
	}
	return sponsor, true
}

// SponsoredCodeBuddyModelAvailable returns the sponsored model when a sponsor
// is configured, or "" otherwise.
func (s *Store) SponsoredCodeBuddyModelAvailable() string {
	if _, ok := s.CodeBuddySponsor(); ok {
		return SponsoredCodeBuddyModel
	}
	return ""
}
