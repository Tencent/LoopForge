package repo

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// payloadRow is the generic projection used to read JSON-payload tables.
type payloadRow struct {
	ID      string     `gorm:"column:id"`
	Name    string     `gorm:"column:name"`
	Payload []byte     `gorm:"column:payload"`
	Created *time.Time `gorm:"column:created_at"`
	Updated *time.Time `gorm:"column:updated_at"`
}

// payloadOne reads a single JSON payload row by primary key.
func payloadOne[T any](db *gorm.DB, table, id string) (T, bool) {
	var zero T
	var row payloadRow
	if err := db.Table(table).Where("id = ?", id).Take(&row).Error; err != nil {
		return zero, false
	}
	var item T
	if json.Unmarshal(row.Payload, &item) != nil {
		return zero, false
	}
	return item, true
}

// payloadList reads every JSON payload row matching the optional condition.
func payloadList[T any](db *gorm.DB, table, query string, args ...any) []T {
	q := db.Table(table)
	if query != "" {
		q = q.Where(query, args...)
	}
	var rows []payloadRow
	if q.Find(&rows).Error != nil {
		return []T{}
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		var item T
		if json.Unmarshal(row.Payload, &item) == nil {
			out = append(out, item)
		}
	}
	return out
}

// upsert inserts a row, replacing every column when the primary key already
// exists. Callers pass the full desired row; CreatedAt preservation is the
// caller's responsibility (it copies the old value before writing).
func upsert(db *gorm.DB, table string, values map[string]any) error {
	cols := make([]string, 0, len(values))
	for name := range values {
		cols = append(cols, name)
	}
	sort.Strings(cols)
	cl := clause.OnConflict{DoUpdates: clause.AssignmentColumns(cols)}
	if db.Dialector.Name() == dialectPostgres {
		// Postgres requires a conflict target for DO UPDATE, and gorm's
		// AutoMigrate names the primary key constraint of every table
		// "<table>_pkey", so the target can be derived from the table name.
		cl.OnConstraint = table + "_pkey"
	}
	if err := db.Table(table).Clauses(cl).Create(values).Error; err != nil {
		return fmt.Errorf("%s write: %w", table, err)
	}
	return nil
}

// upsertColumns inserts a row and updates only the given columns on conflict.
func upsertColumns(db *gorm.DB, table string, values map[string]any, updateCols []string) error {
	cl := clause.OnConflict{DoUpdates: clause.AssignmentColumns(updateCols)}
	if db.Dialector.Name() == dialectPostgres {
		// See upsert: Postgres needs the conflict target, the primary key
		// constraint carries the table's default name.
		cl.OnConstraint = table + "_pkey"
	}
	if err := db.Table(table).Clauses(cl).Create(values).Error; err != nil {
		return fmt.Errorf("%s write: %w", table, err)
	}
	return nil
}

// insertIgnore inserts a row, keeping the existing row on conflict. gorm's
// OnConflict{DoNothing} renders an empty ON DUPLICATE KEY UPDATE clause on
// MySQL, so the dialect-specific statement is built here. Table and column
// names are compile-time literals; values are always bound as parameters.
//
// Postgres has no INSERT IGNORE. ON CONFLICT DO NOTHING without a conflict
// target is its equivalent: the row is skipped on a conflict with any unique
// constraint, which is what INSERT IGNORE does.
func insertIgnore(db *gorm.DB, table string, values map[string]any) (bool, error) {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	cols := make([]string, len(names))
	placeholders := make([]string, len(names))
	args := make([]any, 0, len(names))
	quote := "`"
	if db.Dialector.Name() == dialectPostgres {
		quote = `"`
	}
	for i, name := range names {
		cols[i] = quote + name + quote
		placeholders[i] = "?"
		args = append(args, values[name])
	}
	verb := "INSERT OR IGNORE"
	switch db.Dialector.Name() {
	case dialectMySQL:
		verb = "INSERT IGNORE"
	case dialectPostgres:
		verb = "INSERT"
	}
	stmt := fmt.Sprintf("%s INTO %s (%s) VALUES (%s)", verb, table, strings.Join(cols, ","), strings.Join(placeholders, ","))
	if db.Dialector.Name() == dialectPostgres {
		stmt += " ON CONFLICT DO NOTHING"
	}
	res := db.Exec(stmt, args...)
	return res.RowsAffected > 0, res.Error
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t.UTC()
	return &value
}

func jsonBytes(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return []byte("{}")
	}
	return raw
}

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
