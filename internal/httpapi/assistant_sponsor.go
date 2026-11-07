package httpapi

import (
	"context"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Users who have not configured their own CodeBuddy API Key can still chat
// with the platform's shared key (see repo.SharedCodeBuddyKey), limited to
// the models a platform administrator allowed. SOP runs use it the same way
// through the engine; scheduled tasks, clarification, reports and bots keep
// requiring the user's own key.
const (
	assistantSponsorLoginEnv = repo.CodeBuddySponsorLoginEnv
	assistantSponsoredModel  = repo.SponsoredCodeBuddyModel
)

func errAssistantSponsoredModel(models []string) string {
	return "未配置个人 CodeBuddy API Key 时只能使用平台管理员允许的模型（" + strings.Join(models, "、") + "）。要使用其他模型，请先在个人配置中配置自己的 CodeBuddy API Key"
}

// assistantSharedKey returns the key users without their own borrow.
func (h *assistantHandlers) assistantSharedKey() (repo.SharedCodeBuddyKey, bool) {
	if h == nil || h.deps == nil || h.repo == nil {
		return repo.SharedCodeBuddyKey{}, false
	}
	return h.repo.SharedCodeBuddyKey()
}

// assistantChatUser returns the identity a chat turn runs as. Users with
// their own key are returned unchanged. Otherwise the shared key and its
// network environment are substituted and shared lists the models the turn
// may use; everything else (ID, permissions, Git credentials) stays the
// caller's own.
func (h *assistantHandlers) assistantChatUser(user domain.User) (chatUser domain.User, shared []string, ok bool) {
	if strings.TrimSpace(user.CodeBuddyAPIKey) != "" {
		return user, nil, true
	}
	key, found := h.assistantSharedKey()
	if !found || (key.SponsorID != 0 && key.SponsorID == user.ID) {
		return user, nil, false
	}
	user.CodeBuddyAPIKey = key.APIKey
	user.CodeBuddyInternetEnvironment = key.InternetEnvironment
	return user, key.Models, true
}

// assistantSponsoredModelFor limits shared-key turns to the allowed models.
// An empty request picks the default; any other model is refused.
func assistantSponsoredModelFor(shared []string, requested string) (string, bool) {
	return repo.SharedCodeBuddyKey{Models: shared}.Model(requested)
}

// usesSharedKey reports whether a chat turn for user runs on the shared key:
// the stored account has no key of its own.
func (h *assistantHandlers) usesSharedKey(user domain.User) bool {
	if h == nil || h.deps == nil || h.repo == nil || user.ID <= 0 {
		return false
	}
	stored, ok := h.repo.User(user.ID)
	return ok && strings.TrimSpace(stored.CodeBuddyAPIKey) == ""
}

// chatCatalog is the chat catalog for a chat user (see assistantChatUser):
// shared models are listed, and a shared-key user only sees the allowed ones.
func (h *assistantHandlers) chatCatalog(ctx context.Context, user domain.User) assistantModelCatalog {
	key, shared := h.assistantSharedKey()
	if !shared {
		return assistantChatCatalog(ctx, user.CodeBuddyInternetEnvironment, nil)
	}
	catalog := assistantChatCatalog(ctx, user.CodeBuddyInternetEnvironment, key.Models)
	if h.usesSharedKey(user) {
		catalog = restrictAssistantCatalog(catalog, key.Models)
	}
	return catalog
}

// restrictAssistantCatalog keeps the allowed models, in the allowed order.
func restrictAssistantCatalog(catalog assistantModelCatalog, allowed []string) assistantModelCatalog {
	byID := map[string]assistantModel{}
	for _, model := range catalog.Models {
		byID[model.ID] = model
	}
	restricted := assistantModelCatalog{SponsoredModel: catalog.SponsoredModel, SponsoredModels: catalog.SponsoredModels}
	for _, id := range allowed {
		model, ok := byID[id]
		if !ok {
			model = assistantListedModel(id, id, "")
		}
		restricted.Models = append(restricted.Models, model)
	}
	if len(allowed) > 0 {
		restricted.Default = allowed[0]
	}
	return restricted
}
