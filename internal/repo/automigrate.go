package repo

import (
	"agentflow/internal/domain"
	"fmt"

	"gorm.io/gorm"
)

// autoMigrateIncremental completes every incremental DDL the running database
// is missing (columns, tables, indexes) using gorm's Migrator. Unlike a full
// AutoMigrate — which would also ALTER existing columns to match model
// defaults (e.g. rewriting JSON columns to longblob) — this only ever ADDS
// what is absent, so a production schema migrated by the master binary is
// left untouched except for genuinely missing objects.
//
// The runs.repositories / run_artifacts.repository_dir / runs.pause_requested
// / runs.human_context / runs.cancel_requested / runs.sandbox_id additions
// from the master migrations are all covered here, which removes any
// dependency on schema_migrations marker IDs.
func autoMigrateIncremental(db *gorm.DB) error {
	m := db.Migrator()
	for _, model := range allModels() {
		if !m.HasTable(model) {
			create := m
			// Bot chat may contain supplementary Unicode (emoji). Do not inherit
			// a legacy database utf8mb3 default when creating this new table.
			if _, ok := model.(domain.BotInteraction); ok && db.Dialector.Name() == "mysql" {
				create = db.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci").Migrator()
			}
			if err := create.CreateTable(model); err != nil {
				return fmt.Errorf("create table %T: %w", model, err)
			}
			continue
		}
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return fmt.Errorf("parse schema %T: %w", model, err)
		}
		for _, field := range stmt.Schema.Fields {
			if field.DBName == "" {
				continue
			}
			if !m.HasColumn(model, field.DBName) {
				if err := m.AddColumn(model, field.DBName); err != nil {
					return fmt.Errorf("add column %T.%s: %w", model, field.DBName, err)
				}
			}
		}
		for _, index := range stmt.Schema.ParseIndexes() {
			if !m.HasIndex(model, index.Name) {
				if err := m.CreateIndex(model, index.Name); err != nil {
					return fmt.Errorf("create index %T.%s: %w", model, index.Name, err)
				}
			}
		}
	}
	return nil
}
