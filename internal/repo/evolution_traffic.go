package repo

import (
	"agentflow/internal/domain"
)

func (r *EvolutionRepo) AdjustEvoCanaryTraffic(expected domain.EvoProposal, percent int) (domain.EvoProposal, error) {
	if percent < 1 || percent > 99 {
		return expected, ErrEvoStale
	}
	out := expected
	err := r.assetTx(expected.AssetKind, expected.AssetID, func(tr *EvolutionRepo) error {
		current, busy, err := tr.currentAssetProposal(expected.AssetKind, expected.AssetID)
		if err != nil {
			return err
		}
		if !busy || current.ID != expected.ID {
			return ErrEvoBusy
		}
		p, ok := tr.EvoProposal(expected.ID)
		if !ok || p.Revision != expected.Revision || p.Status != domain.EvoProposalCanary || p.Canary == nil || p.Canary.Status != domain.EvoCanaryRunning {
			return ErrEvoStale
		}
		content, raw, err := tr.catalog.evolutionAsset(tr.DB, p.AssetKind, p.AssetID)
		if err != nil {
			return err
		}
		if domain.ContentHash(content) != p.BaseHash || (p.BaseCatalogHash != "" && domain.ContentHash(string(raw)) != p.BaseCatalogHash) {
			return ErrEvoStale
		}
		if p.BaseVersion > 0 {
			latest, exists := tr.LatestAssetVersion(p.AssetKind, p.AssetID)
			if !exists || latest.Version != p.BaseVersion {
				return ErrEvoStale
			}
		}
		if p.Canary.Percent == percent {
			out = p
			return nil
		}
		p.Canary.Percent = percent
		if err := tr.writeProposal(p); err != nil {
			return err
		}
		out, _ = tr.EvoProposal(p.ID)
		return nil
	})
	return out, err
}
