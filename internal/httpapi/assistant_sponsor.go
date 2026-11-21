package httpapi

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Users who have not configured their own CodeBuddy API Key can still chat
// with the platform's shared key (see repo.SharedCodeBuddyKey), limited to
// the models a platform administrator allowed. SOP runs use it the same way
// through the engine, and AI scheduled tasks through scheduleCredentialUser;
// clarification, reports and bots keep requiring the user's own key.
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
// their own key are returned unchanged. Other turns use the shared key,
// its environment and catalog while keeping the caller's identity,
// permissions and Git credentials.
func (h *assistantHandlers) assistantChatUser(user domain.User) (chatUser domain.User, shared []string, ok bool) {
	if strings.TrimSpace(user.CodeBuddyAPIKey) != "" {
		return user, nil, true
	}
	key, found := h.assistantSharedKey()
	if !found || (key.SponsorID != 0 && key.SponsorID == user.ID) {
		return user, nil, false
	}
	user.CodeBuddyAPIKey = key.APIKey
	user.CodeBuddyBaseURL = ""
	user.CodeBuddyInternetEnvironment = key.InternetEnvironment
	user.CodeBuddyModels = nil
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

// chatCatalog is the chat catalog for a chat user (see assistantChatUser). A
// turn on the shared key only sees the allowed models, defaulting to the
// first; a user with their own key keeps their environment's catalog and
// default, which the shared key's models must not replace.
func (h *assistantHandlers) chatCatalog(ctx context.Context, user domain.User) assistantModelCatalog {
	key, shared := h.assistantSharedKey()
	if !shared || !h.usesSharedKey(user) {
		return withCodeBuddyPersonalModels(ctx, assistantChatCatalog(user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey, nil), user)
	}
	return restrictAssistantCatalog(assistantChatCatalog(user.CodeBuddyInternetEnvironment, user.CodeBuddyAPIKey, key.Models), key.Models)
}

// turnCatalog is chatCatalog for a turn on model. The CodeBuddy model list is
// cached in memory per instance, so a model picked from another instance's
// list, or before a restart, would otherwise be replaced by the default
// without a word. A miss refreshes the list once before deciding.
func (h *assistantHandlers) turnCatalog(ctx context.Context, user domain.User, model string) assistantModelCatalog {
	catalog := h.chatCatalog(ctx, user)
	model = strings.TrimSpace(model)
	if model == "" || assistantCatalogLists(catalog, model) {
		return catalog
	}
	refreshOfficialCodeBuddyModels(ctx, user)
	catalog = h.chatCatalog(ctx, user)
	if !assistantCatalogLists(catalog, model) {
		log.Printf("assistant model %q is not offered to user %d after catalog refresh", model, user.ID)
	}
	return catalog
}

func assistantCatalogLists(catalog assistantModelCatalog, id string) bool {
	for _, model := range catalog.Models {
		if model.ID == id {
			return true
		}
	}
	return false
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
