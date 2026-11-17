package repo

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// payloadMentionsChunk bounds the columns of one payloadMentions query.
const payloadMentionsChunk = 20

// payloadMentions reports which of ids appear anywhere in the JSON payload of
// a row of table, skipping rows matched by exclude (an SQL condition with its
// arguments, or empty). Each chunk of ids costs one table scan that returns a
// single row of flags. A failed query reports every id of its chunk as
// mentioned, so callers that delete what nothing mentions keep it instead.
func payloadMentions(db *gorm.DB, table string, ids []string, exclude string, excludeArgs ...any) map[string]bool {
	found := map[string]bool{}
	has := "instr(CAST(payload AS TEXT), ?) > 0"
	if db.Dialector.Name() == "mysql" {
		has = "INSTR(CAST(payload AS CHAR), ?) > 0"
	}
	for start := 0; start < len(ids); start += payloadMentionsChunk {
		chunk := ids[start:min(start+payloadMentionsChunk, len(ids))]
		cols := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+len(excludeArgs))
		for i, id := range chunk {
			cols[i] = fmt.Sprintf("MAX(CASE WHEN %s THEN 1 ELSE 0 END)", has)
			args = append(args, id)
		}
		query := "SELECT " + strings.Join(cols, ", ") + " FROM " + table
		if exclude != "" {
			query += " WHERE NOT (" + exclude + ")"
			args = append(args, excludeArgs...)
		}
		flags := make([]sql.NullInt64, len(chunk))
		dest := make([]any, len(chunk))
		for i := range flags {
			dest[i] = &flags[i]
		}
		if err := db.Raw(query, args...).Row().Scan(dest...); err != nil {
			for _, id := range chunk {
				found[id] = true
			}
			continue
		}
		for i, id := range chunk {
			if flags[i].Valid && flags[i].Int64 > 0 {
				found[id] = true
			}
		}
	}
	return found
}
