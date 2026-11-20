package repo

import (
	"os"
	"testing"

	"agentflow/internal/domain"
)

func TestAssistantMessageSendPreference(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			var st *Store
			var err error
			if driver == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
				}
				st, err = Open(dsn)
			} else {
				st, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			const userID = int64(801036)
			if err := st.DB.Where("user_id = ?", userID).Delete(&assistantPreferenceRow{}).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.DB.Where("user_id = ?", userID).Delete(&assistantPreferenceRow{}) })
			if st.AssistantPreferences(userID).SteerOnSend {
				t.Fatal("messages should queue by default")
			}
			for _, immediate := range []bool{true, false} {
				saved, err := st.PutAssistantPreferences(userID, domain.AssistantPreferences{SteerOnSend: immediate, Instructions: "keep this rule"})
				if err != nil {
					t.Fatal(err)
				}
				if saved.SteerOnSend != immediate || st.AssistantPreferences(userID).SteerOnSend != immediate || saved.Instructions != "keep this rule" {
					t.Fatalf("saved preferences: %+v", saved)
				}
				if st.AssistantPreferences(userID + 1).SteerOnSend {
					t.Fatal("preference leaked to another user")
				}
			}
		})
	}
}
