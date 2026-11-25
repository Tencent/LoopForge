package repo

import (
	"fmt"
	"os"
	"testing"
	"time"

	"agentflow/internal/domain"
)

func TestMySQLRunnerLoginCatalogSurvivesReregistration(t *testing.T) {
	dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AGENTFLOW_TEST_MYSQL_DSN is not set")
	}
	store, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	runner := domain.Runner{ID: fmt.Sprintf("runner-models-%d", time.Now().UnixNano()), OwnerUserID: 1, Name: "Model catalog runner"}
	t.Cleanup(func() { _ = store.DeregisterRunner(runner.ID, time.Now().UTC()) })
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunnerLoginModels(runner.ID, runner.OwnerUserID, "codex", []domain.CLIModel{{ID: "account-model"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunnerLoginModels(runner.ID, runner.OwnerUserID, "claude-code", []domain.CLIModel{{ID: "account-sonnet"}}); err != nil {
		t.Fatal(err)
	}
	runner.Status = domain.RunnerStatusOnline
	if err := store.PutRunner(runner); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Runner(runner.ID)
	if len(got.LoginModels["codex"]) != 1 || len(got.LoginModels["claude-code"]) != 1 {
		t.Fatalf("reregistration lost a catalog: %+v", got.LoginModels)
	}
}
