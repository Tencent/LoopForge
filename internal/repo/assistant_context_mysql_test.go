package repo

import (
	"agentflow/internal/domain"
	"encoding/json"
	"fmt"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"os"
	"testing"
)

// Exercise the production MySQL JSON projection without writing any rows.
func TestMySQLAssistantParticipantProjectionReadOnly(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("MySQL required")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	defer conn.Close()
	r := &AssistantRepo{deps: &deps{DB: db}}
	payload := `{"messages":[{"role":"user","authorId":11,"authorName":"alice","content":"hello"},{"role":"assistant","authorId":999,"content":"AI"},{"role":"user","authorId":12,"authorName":"bob","content":"world"},{"role":"user","content":"legacy"}]}`
	var result string
	if err := db.Raw("SELECT ("+r.assistantParticipantSummarySQL()+") FROM (SELECT CAST(? AS JSON) AS payload) AS legacy", payload).Scan(&result).Error; err != nil {
		t.Fatal(err)
	}
	var messages []domain.AssistantMessage
	if err := json.Unmarshal([]byte(result), &messages); err != nil {
		t.Fatal(err, result)
	}
	s := domain.AssistantSession{Shared: true}
	deriveAssistantParticipants(&s, messages)
	if len(s.Participants) != 2 || !s.UnknownParticipants {
		t.Fatalf("wrong projection: %+v", s)
	}
}

// Only "this server cannot run the projection" disables it for the process;
// a transient failure (lock wait, lost connection) keeps trying next time.
func TestSummaryProjectionUnsupportedOnlyForMissingJSONTable(t *testing.T) {
	for _, number := range []uint16{1064, 1305} {
		if !summaryProjectionUnsupported(fmt.Errorf("list: %w", &mysqldriver.MySQLError{Number: number})) {
			t.Fatalf("error %d must disable the projection", number)
		}
	}
	for _, err := range []error{&mysqldriver.MySQLError{Number: 1205}, fmt.Errorf("driver: bad connection"), nil} {
		if summaryProjectionUnsupported(err) {
			t.Fatalf("%v must not disable the projection", err)
		}
	}
}
