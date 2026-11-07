package httpapi

import (
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Users who have not configured their own CodeBuddy API Key can still chat
// with the sponsor's key (see repo.CodeBuddySponsor), limited to
// assistantSponsoredModel. SOP runs are sponsored by the engine the same way;
// scheduled tasks, clarification, reports and bots keep requiring the user's
// own key.
const (
	assistantSponsorLoginEnv = repo.CodeBuddySponsorLoginEnv
	assistantSponsoredModel  = repo.SponsoredCodeBuddyModel
)

func errAssistantSponsoredModel() string {
	return "未配置个人 CodeBuddy API Key 时只能使用 Deepseek-V4.1-Flash。要使用其他模型，请先在个人配置中配置自己的 CodeBuddy API Key"
}

// assistantSponsor returns the account whose key is lent, if one is
// configured and has a key.
func (h *assistantHandlers) assistantSponsor() (domain.User, bool) {
	if h == nil || h.deps == nil || h.repo == nil {
		return domain.User{}, false
	}
	return h.repo.CodeBuddySponsor()
}

// assistantChatUser returns the identity a chat turn runs as. Users with
// their own key are returned unchanged. Otherwise the sponsor's key and
// network environment are substituted and sponsored is true; everything else
// (ID, permissions, Git credentials) stays the caller's own.
func (h *assistantHandlers) assistantChatUser(user domain.User) (chatUser domain.User, sponsored, ok bool) {
	if strings.TrimSpace(user.CodeBuddyAPIKey) != "" {
		return user, false, true
	}
	sponsor, found := h.assistantSponsor()
	if !found || sponsor.ID == user.ID {
		return user, false, false
	}
	user.CodeBuddyAPIKey = sponsor.CodeBuddyAPIKey
	user.CodeBuddyInternetEnvironment = sponsor.CodeBuddyInternetEnvironment
	return user, true, true
}

// assistantSponsoredModelFor pins sponsored turns to the sponsored model. An
// empty request picks it; any other explicit model is refused.
func assistantSponsoredModelFor(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == assistantSponsoredModel {
		return assistantSponsoredModel, true
	}
	return "", false
}
