package tracker

import (
	"agentflow/internal/domain"
)

// EnrichDeliveryMRStates is deprecated: MR states are persisted in mr_delivery_states
// and refreshed by StartMRStateSync. Kept for compatibility; only finalizes items.
func (s *Service) EnrichDeliveryMRStates(items []domain.AnalyticsDelivery) {
	for i := range items {
		domain.FinalizeAnalyticsDelivery(&items[i])
	}
}
