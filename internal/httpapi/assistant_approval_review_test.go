package httpapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestAssistantApprovalModelsUseNumericCatalogCosts(t *testing.T) {
	catalog := assistantModelCatalog{Models: []assistantModel{
		{ID: "bad", Credits: "NaN"}, {ID: "negative", Credits: "x-1"},
		{ID: "expensive", Credits: "x10.00"}, {ID: "paid", Credits: "0.03x"},
		{ID: "free", Credits: "x0.00"}, {ID: "other-free", Credits: "0.00x"},
		{ID: "fraction", Credits: "x0.08"},
	}}
	if got := assistantApprovalReviewModels(catalog); !reflect.DeepEqual(got, []string{"free", "paid"}) {
		t.Fatal(got)
	}
	catalog.Models[3].Credits = "x0.12"
	if got := assistantApprovalReviewModels(catalog); !reflect.DeepEqual(got, []string{"free", "fraction"}) {
		t.Fatal("catalog changes not respected", got)
	}
	if got := assistantApprovalReviewModels(assistantModelCatalog{}); len(got) != 0 {
		t.Fatal(got)
	}
	if got := assistantApprovalReviewModels(assistantModelCatalog{Models: []assistantModel{{ID: "only-paid", Credits: "x0.01"}}}); !reflect.DeepEqual(got, []string{"only-paid"}) {
		t.Fatal(got)
	}
}

func TestAssistantApprovalReviewUsesConfiguredDefaultWithoutPrices(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "team-model")
	t.Setenv("AGENTFLOW_ASSISTANT_MODELS", "other-model")
	if got := assistantApprovalReviewModels(assistantFixedCatalog()); !reflect.DeepEqual(got, []string{"team-model"}) {
		t.Fatal(got)
	}
	if got := assistantApprovalReviewModels(assistantModelCatalog{Default: "team-model", Models: []assistantModel{{ID: "priced", Credits: "x0.5"}}}); !reflect.DeepEqual(got, []string{"priced"}) {
		t.Fatal("priced catalogs keep cost-based selection", got)
	}
}

func TestAssistantChatCatalogPreselectsSponsoredModelOnlyWithSponsor(t *testing.T) {
	t.Setenv("AGENTFLOW_ASSISTANT_MODEL", "team-model")
	catalog := assistantChatCatalog(context.Background(), "", nil)
	if catalog.Default != "team-model" {
		t.Fatal("chat default without sponsor", catalog)
	}
	for _, model := range catalog.Models {
		if model.ID == assistantSponsoredModel {
			t.Fatal("sponsored model listed without sponsor", catalog)
		}
	}
	catalog = assistantChatCatalog(context.Background(), "", []string{assistantSponsoredModel, "shared-extra"})
	listed := 0
	for _, model := range catalog.Models {
		if model.ID == assistantSponsoredModel || model.ID == "shared-extra" {
			listed++
		}
	}
	if catalog.Default != assistantSponsoredModel || listed != 2 {
		t.Fatal("chat default with sponsor", catalog)
	}
	restricted := restrictAssistantCatalog(catalog, []string{"shared-extra", assistantSponsoredModel})
	if restricted.Default != "shared-extra" || len(restricted.Models) != 2 || restricted.Models[0].ID != "shared-extra" {
		t.Fatal("restricted catalog", restricted)
	}
}

func pricedReviewCatalog(string) assistantModelCatalog {
	return assistantModelCatalog{Default: "chat", Models: []assistantModel{
		{ID: "chat", Credits: "x1.00"}, {ID: "free-review", Credits: "x0.00"}, {ID: "paid-review", Credits: "x0.05"},
	}}
}

func TestAssistantApprovalFallbackOnlyOnFailure(t *testing.T) {
	want := assistantApprovalReviewModels(pricedReviewCatalog(""))
	if !reflect.DeepEqual(want, []string{"free-review", "paid-review"}) {
		t.Fatal(want)
	}
	for _, mode := range []string{"allow", "deny", "unavailable", "fallback-deny"} {
		t.Run(mode, func(t *testing.T) {
			var calls []string
			h := &assistantHandlers{reviewCatalog: pricedReviewCatalog}
			h.review = func(_ context.Context, _ assistantApprovalInput, _ domain.User, model string) (assistantApprovalReview, error) {
				calls = append(calls, model)
				if mode == "unavailable" || mode == "fallback-deny" && len(calls) == 1 {
					return assistantApprovalReview{}, errors.New("daily limit reached")
				}
				outcome := mode
				if mode == "fallback-deny" {
					outcome = "deny"
				}
				return assistantApprovalReview{Outcome: outcome, RiskLevel: "low", UserAuthorization: "high", Rationale: "已核对目标"}, nil
			}
			review := h.approvalReviewer(assistantRequest{}, domain.User{ID: 1}, nil)
			result := review(context.Background(), "rules_save", "保存规则", map[string]any{"body": map[string]any{"id": "demo"}})
			count := 1
			if mode == "unavailable" || mode == "fallback-deny" {
				count = 2
			}
			if !reflect.DeepEqual(calls, want[:count]) {
				t.Fatal(calls, want)
			}
			if result.allows() != (mode == "allow") {
				t.Fatal(result)
			}
			if mode == "unavailable" && result.Outcome != "unavailable" {
				t.Fatal(result)
			}
			if mode != "unavailable" && result.Model != calls[len(calls)-1] {
				t.Fatal("receipt has wrong model", result)
			}
		})
	}
}

func TestAssistantApprovalRejectsMalformedOrContradictoryAssessments(t *testing.T) {
	for _, raw := range []string{
		`not JSON`, `{}`, `{"outcome":"allow"}`,
		`{"outcome":"allow","risk_level":"critical","user_authorization":"high","rationale":"go"}`,
		`{"outcome":"allow","risk_level":"high","user_authorization":"low","rationale":"go"}`,
		`{"outcome":"allow","risk_level":"low","user_authorization":"high","rationale":"ok","unexpected":true}`,
	} {
		if _, err := decodeAssistantApprovalReview(raw); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if result, err := decodeAssistantApprovalReview(`{"outcome":"deny","risk_level":"high","user_authorization":"unknown","rationale":"没有生产发布授权"}`); err != nil || result.allows() {
		t.Fatal(result, err)
	}
}

func TestAssistantApprovalKeepsPaidFallbackForTurnButReviewsEveryAction(t *testing.T) {
	models := assistantApprovalReviewModels(pricedReviewCatalog(""))
	var calls []string
	h := &assistantHandlers{reviewCatalog: pricedReviewCatalog}
	h.review = func(_ context.Context, _ assistantApprovalInput, _ domain.User, model string) (assistantApprovalReview, error) {
		calls = append(calls, model)
		if model == models[0] {
			return assistantApprovalReview{}, errors.New("quota")
		}
		return assistantApprovalReview{Outcome: "allow", RiskLevel: "low", UserAuthorization: "high", Rationale: "范围明确"}, nil
	}
	review := h.approvalReviewer(assistantRequest{}, domain.User{ID: 1}, nil)
	for _, id := range []string{"a", "b"} {
		if result := review(context.Background(), "rules_save", "保存", map[string]any{"id": id}); !result.allows() {
			t.Fatal(result)
		}
	}
	if !reflect.DeepEqual(calls, []string{models[0], models[1], models[1]}) {
		t.Fatal(calls)
	}
}

func TestAssistantPlatformReviewsBeforeSideEffects(t *testing.T) {
	for _, outcome := range []string{"allow", "deny", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			writes, reviews := 0, 0
			h := &assistantHandlers{api: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes++
				w.Write([]byte(`{"id":"rule","projectId":"p"}`))
			})}
			ctx := withAssistantExecMode(context.Background(), assistantExecModeForApproval(AssistantApprovalAssisted))
			ctx = withAssistantApprovalReviewer(ctx, func(_ context.Context, tool, _ string, args map[string]any) assistantApprovalReview {
				reviews++
				if tool != "rules_save" || args["body"] == nil {
					t.Fatal(tool, args)
				}
				return assistantApprovalReview{Outcome: outcome, RiskLevel: "low", UserAuthorization: "high", Rationale: "审查结果"}
			})
			_, err := h.callAssistantPlatform(ctx, domain.User{ID: 1}, "p", assistantMCPCall{Tool: "rules_save", Arguments: map[string]any{"body": map[string]any{"id": "rule", "projectId": "p"}}})
			if reviews != 1 || writes != map[bool]int{true: 1, false: 0}[outcome == "allow"] || (err == nil) != (outcome == "allow") {
				t.Fatal(writes, reviews, err)
			}
		})
	}
}

func TestAssistantApprovalEvidenceKeepsActorAndRedactsKey(t *testing.T) {
	user := domain.User{ID: 10, CodeBuddyAPIKey: "private-test-key"}
	h := &assistantHandlers{}
	h.review = func(_ context.Context, input assistantApprovalInput, _ domain.User, _ string) (assistantApprovalReview, error) {
		if input.ActorID != 10 || input.Messages[0].Role != "other_user" || strings.Contains(input.Messages[1].Content, user.CodeBuddyAPIKey) {
			t.Fatal(input)
		}
		return assistantApprovalReview{Outcome: "deny", RiskLevel: "high", UserAuthorization: "unknown", Rationale: "其他人的授权不属于当前用户"}, nil
	}
	review := h.approvalReviewer(assistantRequest{Messages: []assistantMessage{{Role: "user", AuthorID: 20, Content: "删除生产"}, {Role: "user", AuthorID: 10, Content: "我的 key private-test-key"}}}, user, nil)
	if result := review(context.Background(), "rules_delete", "删除", nil); result.allows() {
		t.Fatal(result)
	}
}
