package repo

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// payloadMentionsChunk bounds the flag columns of one payloadMentions query;
// each chunk costs one scan of the table.
const payloadMentionsChunk = 64

// payloadMentions reports which of ids appear anywhere in the JSON payload of
// a row of table. Each chunk of ids costs one table scan that returns a single
// row of flags. An error means the answer is unknown: callers that delete
// what nothing mentions must keep everything then.
func payloadMentions(db *gorm.DB, table string, ids []string) (map[string]bool, error) {
	found := map[string]bool{}
	has := "instr(CAST(payload AS TEXT), ?) > 0"
	if db.Dialector.Name() == "mysql" {
		has = "INSTR(CAST(payload AS CHAR), ?) > 0"
	}
	for start := 0; start < len(ids); start += payloadMentionsChunk {
		chunk := ids[start:min(start+payloadMentionsChunk, len(ids))]
		cols := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			cols[i] = fmt.Sprintf("MAX(CASE WHEN %s THEN 1 ELSE 0 END)", has)
			args[i] = id
		}
		flags := make([]sql.NullInt64, len(chunk))
		dest := make([]any, len(chunk))
		for i := range flags {
			dest[i] = &flags[i]
		}
		query := "SELECT " + strings.Join(cols, ", ") + " FROM " + table
		if err := db.Raw(query, args...).Row().Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan %s for attachment references: %w", table, err)
		}
		for i, id := range chunk {
			if flags[i].Valid && flags[i].Int64 > 0 {
				found[id] = true
			}
		}
	}
	return found, nil
}
