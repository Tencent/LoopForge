package engine

import (
	"agentflow/internal/repo"
	"agentflow/internal/runtime"
)

const (
	codeBuddyAPIKeyEnv   = "CODEBUDDY_API_KEY"
	codeBuddyInternetEnv = "CODEBUDDY_INTERNET_ENVIRONMENT"
)

func resolveCodeBuddyCredential(s *repo.Store, userID int64) runtime.CodeBuddyCredential {
	return runtime.CodeBuddyCredential{
		APIKey:              resolveCodeBuddyAPIKey(s, userID),
		InternetEnvironment: resolveCodeBuddyInternet(s, userID),
	}
}

func resolveCodeBuddyAPIKey(s *repo.Store, userID int64) string {
	if s == nil || userID <= 0 {
		return ""
	}
	u, ok := s.User(userID)
	if !ok {
		return ""
	}
	return u.CodeBuddyAPIKey
}

func resolveCodeBuddyInternet(s *repo.Store, userID int64) string {
	if s == nil || userID <= 0 {
		return ""
	}
	u, ok := s.User(userID)
	if !ok {
		return ""
	}
	return u.CodeBuddyInternetEnvironment
}
