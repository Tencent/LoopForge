package repo

// Read-only detection of the MySQL schema pieces gorm AutoMigrate cannot
// create (see migrations/001_run_obs_generated_columns.sql). Nothing here
// writes to the database: the columns are still installed by the operator
// (scripts/install-schema-extensions.sh for Compose, the SQL file for an
// external MySQL). Startup only logs what is missing, and a query that hits
// a missing column answers with the same instruction instead of the raw
// driver error.

import (
	"errors"
	"fmt"
	"strings"

	"agentflow/internal/apperr"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// schemaExtensionColumn is one generated column this release reads on MySQL.
type schemaExtensionColumn struct {
	Table     string
	Column    string
	Migration string
}

// requiredSchemaExtensions lists the generated columns that run details and
// analytics coverage query directly on MySQL.
var requiredSchemaExtensions = []schemaExtensionColumn{
	{Table: "run_telemetry", Column: "adapter", Migration: "migrations/001_run_obs_generated_columns.sql"},
	{Table: "run_logs", Column: "level", Migration: "migrations/001_run_obs_generated_columns.sql"},
}

// missingSchemaExtensions returns the required columns that has reports as
// absent. A lookup error stops the scan so a partial answer is never shown as
// complete.
func missingSchemaExtensions(has func(table, column string) (bool, error)) ([]schemaExtensionColumn, error) {
	var missing []schemaExtensionColumn
	for _, col := range requiredSchemaExtensions {
		ok, err := has(col.Table, col.Column)
		if err != nil {
			return nil, err
		}
		if !ok {
			missing = append(missing, col)
		}
	}
	return missing, nil
}

// mysqlColumnExists checks information_schema for a column in the current
// database.
func mysqlColumnExists(db *gorm.DB) func(table, column string) (bool, error) {
	return func(table, column string) (bool, error) {
		var n int64
		err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?", table, column).Scan(&n).Error
		return n > 0, err
	}
}

// schemaExtensionsHint is the operator instruction for missing columns.
func schemaExtensionsHint(missing []schemaExtensionColumn) string {
	names := make([]string, 0, len(missing))
	files := make([]string, 0, len(missing))
	seen := map[string]bool{}
	for _, col := range missing {
		names = append(names, col.Table+"."+col.Column)
		if !seen[col.Migration] {
			seen[col.Migration] = true
			files = append(files, col.Migration)
		}
	}
	return fmt.Sprintf("数据库缺少生成列 %s：Compose 部署请执行 bash scripts/install-schema-extensions.sh；自备 MySQL 请执行 %s 后重试",
		strings.Join(names, "、"), strings.Join(files, "、"))
}

// schemaExtensionsWarning reports, on MySQL, the generated columns this
// release reads but the database lacks, with the command that installs them.
// It returns "" when nothing is missing or the dialect does not use them.
// Open logs it instead of refusing to start: on a fresh Compose install the
// platform must come up (and AutoMigrate create the base tables) before the
// installer can add the columns.
func schemaExtensionsWarning(d *deps) (string, error) {
	if d == nil || !d.isMySQL() {
		return "", nil
	}
	missing, err := missingSchemaExtensions(mysqlColumnExists(d.DB))
	if err != nil || len(missing) == 0 {
		return "", err
	}
	return schemaExtensionsHint(missing), nil
}

// asSchemaExtensionError turns MySQL "Unknown column" (1054) on one of the
// required generated columns into an Unavailable error naming the script to
// run. Any other error is returned unchanged.
func asSchemaExtensionError(err error) error {
	var sqlErr *mysql.MySQLError
	if err == nil || !errors.As(err, &sqlErr) || sqlErr.Number != 1054 {
		return err
	}
	var missing []schemaExtensionColumn
	for _, col := range requiredSchemaExtensions {
		if strings.Contains(sqlErr.Message, "'"+col.Column+"'") {
			missing = append(missing, col)
		}
	}
	if len(missing) == 0 {
		return err
	}
	return apperr.New(apperr.Unavailable, schemaExtensionsHint(missing))
}
