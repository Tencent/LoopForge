package httpapi

import (
	"context"
	"log"
	"strings"

	"agentflow/internal/domain"
	"agentflow/internal/repo"
)

// Users who have not configured their own API Key for a CLI can still chat
// with the platform's shared key for that CLI (see repo.SharedModelKey),
// limited to the models a platform administrator allowed. LOOP runs use it the
// same way through the engine, and AI scheduled tasks through
// scheduleCredentialUser for CodeBuddy; clarification, reports and bots keep
// requiring the user's own key.
const (
	assistantSponsorLoginEnv = repo.CodeBuddySponsorLoginEnv
	assistantSponsoredModel  = repo.SponsoredCodeBuddyModel
)

func errAssistantSponsoredModel(label string, models []string) string {
	return "未配置个人 " + label + " API Key 时只能使用平台管理员允许的模型（" + strings.Join(models, "、") + "）。要使用其他模型，请先在个人配置中配置自己的 " + label + " API Key"
}

// assistantSharedKey returns the CodeBuddy key users without their own borrow.
func (h *assistantHandlers) assistantSharedKey() (repo.SharedModelKey, bool) {
	return h.assistantSharedModelKey(domain.SharedModelProviderCodeBuddy)
}

// assistantSharedModelKey returns the shared credential of one CLI.
func (h *assistantHandlers) assistantSharedModelKey(provider string) (repo.SharedModelKey, bool) {
	if h == nil || h.deps == nil || h.repo == nil {
		return repo.SharedModelKey{}, false
	}
	return h.repo.SharedModelKey(provider)
}

// assistantChatUser returns the identity a CodeBuddy chat turn runs as. Users
// with their own key are returned unchanged. Other turns use the shared key,
// its environment and catalog while keeping the caller's identity, permissions
// and Git credentials.
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

// assistantChatUserForEngine is assistantChatUser for any shared CLI: it
// substitutes that CLI's shared key and Base URL when the user configured
// none, and reports the models the shared key allows.
func (h *assistantHandlers) assistantChatUserForEngine(user domain.User, engine string) (chatUser domain.User, shared []string, ok bool) {
	if engine == assistantEngineCodeBuddy {
		return h.assistantChatUser(user)
	}
	if key, _ := assistantEngineCredential(user, engine); key != "" {
		return user, nil, true
	}
	if !sharedEngine(engine) {
		return user, nil, false
	}
	key, found := h.assistantSharedModelKey(engine)
	if !found || strings.TrimSpace(key.APIKey) == "" || len(key.Models) == 0 || (key.SponsorID != 0 && key.SponsorID == user.ID) {
		return user, nil, false
	}
	switch engine {
	case assistantEngineCodex:
		user.CodexAPIKey, user.CodexBaseURL, user.CodexModels = key.APIKey, key.BaseURL, nil
	case assistantEngineClaude:
		user.ClaudeCodeAPIKey, user.ClaudeCodeBaseURL, user.ClaudeCodeModels = key.APIKey, key.BaseURL, nil
	}
	return user, key.Models, true
}

// sharedEngine reports whether a CLI's key can be shared platform-wide.
func sharedEngine(engine string) bool {
	_, ok := domain.NormalizeSharedModelProvider(engine)
	return ok && engine != domain.SharedModelProviderCodeBuddy
}

// assistantSharedEngineModels lists, per CLI, the models the platform shares
// with this user: none for a CLI the user configured, and none they lend
// themselves.
func assistantSharedEngineModels(r *repo.Store, user domain.User) map[string][]string {
	shared := map[string][]string{}
	if r == nil || user.ID <= 0 {
		return shared
	}
	for _, engine := range domain.SharedModelProviders {
		if key, _ := assistantEngineCredential(user, engine); key != "" {
			continue
		}
		key, found := r.SharedModelKey(engine)
		if !found || strings.TrimSpace(key.APIKey) == "" || len(key.Models) == 0 || (key.SponsorID != 0 && key.SponsorID == user.ID) {
			continue
		}
		shared[engine] = key.Models
	}
	return shared
}

// assistantSponsoredModelFor limits shared-key turns to the allowed models.
// An empty request picks the default; any other model is refused.
func assistantSponsoredModelFor(shared []string, requested string) (string, bool) {
	return repo.SharedModelKey{Models: shared}.Model(requested)
}

// usesSharedKey reports whether a CodeBuddy chat turn for user runs on the
// shared key: the stored account has no key of its own.
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
