package repo

import (
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestSQLPayloadJSONTextSQLite(t *testing.T) {
	s, err := OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	d := &deps{DB: s.DB}
	cases := map[string]string{
		"tapdId":                  "json_extract(payload, '$.tapdId')",
		"externalRef.externalUrl": "json_extract(payload, '$.externalRef.externalUrl')",
		"clarification.startedAt": "json_extract(payload, '$.clarification.startedAt')",
	}
	for path, want := range cases {
		if got := d.sqlPayloadJSONText(path); got != want {
			t.Fatalf("sqlPayloadJSONText(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestSQLPayloadJSONTextMySQL(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:pass@tcp(127.0.0.1:1)/agentflow",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	d := &deps{DB: db}
	if !d.isMySQL() {
		t.Fatal("expected mysql dialect")
	}
	cases := map[string]string{
		"deliveryState":           "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.deliveryState'))",
		"externalRef.externalUrl": "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.externalRef.externalUrl'))",
		"operations":              "JSON_UNQUOTE(JSON_EXTRACT(payload, '$.operations'))",
	}
	for path, want := range cases {
		if got := d.sqlPayloadJSONText(path); got != want {
			t.Fatalf("sqlPayloadJSONText(%q) = %q, want %q", path, got, want)
		}
	}
}
