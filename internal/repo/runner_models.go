package repo

import (
	"time"

	"agentflow/internal/apperr"
	"agentflow/internal/domain"
	"gorm.io/gorm"
)

// SetRunnerLoginModels saves the owner's account catalog independently of the
// API Key catalog and of the runtime inventory written by the runner.
func (r *RunnersRepo) SetRunnerLoginModels(id string, ownerID int64, provider string, models []domain.CLIModel) error {
	models, err := normalizeCLIModels(models)
	if err != nil {
		return apperr.New(apperr.Invalid, err.Error())
	}
	return r.DB.Transaction(func(tx *gorm.DB) error {
		row, err := r.lockRunner(tx, id)
		if err != nil {
			return err
		}
		runner := runnerFromRow(row)
		if ownerID <= 0 || runner.OwnerUserID != ownerID {
			return apperr.New(apperr.Forbidden, "只能管理自己绑定的执行机器")
		}
		catalogs := runner.LoginModels
		if catalogs == nil {
			catalogs = make(map[string][]domain.CLIModel)
		}
		if len(models) == 0 {
			delete(catalogs, provider)
		} else {
			catalogs[provider] = models
		}
		return tx.Model(&runnerRow{}).Where("id = ?", id).Updates(map[string]any{
			"login_model_catalogs": jsonBytes(catalogs),
			"updated_at":           time.Now().UTC(),
		}).Error
	})
}
