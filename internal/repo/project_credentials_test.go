package repo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"agentflow/internal/domain"
)

func TestProjectCredentialStorageAndRotation(t *testing.T) {
	t.Setenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	for _, backend := range []string{"sqlite", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			var st *Store
			var err error
			if backend == "mysql" {
				dsn := os.Getenv("AGENTFLOW_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("set AGENTFLOW_TEST_MYSQL_DSN to test MySQL")
				}
				st, err = Open(dsn)
			} else {
				st, err = OpenTest()
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(st.Close)
			ctx := t.Context()
			projectID, otherID := "credential-example", "credential-other"
			for _, id := range []string{projectID, otherID} {
				if err := st.PutProject(domain.Project{ID: id, Name: id}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					st.DB.Where("project_id = ?", id).Delete(&projectCredentialRow{})
					st.DB.Where("id = ?", id).Delete(&projectRow{})
				})
			}
			const id = "credential-record"
			username, password := "fixture-user", "fixture-first-password"
			input := domain.ProjectCredentialInput{Name: "Registry", Kind: domain.ProjectCredentialPassword, Fields: []domain.ProjectCredentialVariableInput{
				{Name: "REGISTRY_USERNAME", Value: &username},
				{Name: "REGISTRY_PASSWORD", Value: &password},
			}}
			metadata, err := st.SaveProjectCredential(ctx, projectID, id, true, input)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(metadata.Variables, []string{"REGISTRY_USERNAME", "REGISTRY_PASSWORD"}) {
				t.Fatalf("credential fields = %v", metadata.Variables)
			}
			var row projectCredentialRow
			if err := st.DB.Where("id = ?", id).Take(&row).Error; err != nil {
				t.Fatal(err)
			}
			items, err := st.ProjectCredentials(ctx, projectID)
			if err != nil || len(items) != 1 {
				t.Fatalf("list: %v, count %d", err, len(items))
			}
			public, _ := json.Marshal(items)
			for _, data := range [][]byte{row.Ciphertext, public} {
				if bytes.Contains(data, []byte(password)) || bytes.Contains(data, []byte(username)) {
					t.Fatal("plaintext appears in a stored or public value")
				}
			}
			if _, err := st.ResolveProjectCredentialEnv(ctx, otherID, []string{id}); !errors.Is(err, ErrProjectCredentialUnavailable) {
				t.Fatalf("cross-project resolution: %v", err)
			}
			if _, err := st.ProjectCredentialVariableNames(ctx, projectID, []string{id, id}); err == nil || !strings.Contains(err.Error(), "重复") {
				t.Fatalf("duplicate credential binding: %v", err)
			}
			if err := st.DeleteProjectCredential(ctx, otherID, id); !errors.Is(err, ErrProjectCredentialUnavailable) {
				t.Fatalf("cross-project deletion: %v", err)
			}
			replacement := "fixture-rotated-password"
			input.Fields = []domain.ProjectCredentialVariableInput{
				{Name: "REGISTRY_LOGIN", PreviousName: "REGISTRY_USERNAME"},
				{Name: "REGISTRY_PASSWORD", Value: &replacement},
			}
			if _, err := st.SaveProjectCredential(ctx, projectID, id, false, input); err != nil {
				t.Fatal(err)
			}
			env, err := st.ResolveProjectCredentialEnv(ctx, projectID, []string{id})
			if err != nil || len(env) != 2 || env["REGISTRY_LOGIN"] != username || env["REGISTRY_PASSWORD"] != replacement {
				t.Fatalf("rotate and preserve fields: %v", err)
			}
			ids := []string{id, "missing-binding"}
			available, err := st.AvailableProjectCredentialMetadata(ctx, projectID, ids)
			if err != nil || len(available) != 1 || available[0].ID != id {
				t.Fatalf("available binding metadata: %v, count %d", err, len(available))
			}
			env, err = st.ResolveAvailableProjectCredentialEnv(ctx, projectID, ids)
			if err != nil || len(env) != 2 || env["REGISTRY_PASSWORD"] != replacement {
				t.Fatalf("available binding environment: %v", err)
			}
			if _, err := st.ResolveProjectCredentialEnv(ctx, projectID, ids); !errors.Is(err, ErrProjectCredentialUnavailable) {
				t.Fatalf("strict resolution accepted a missing binding: %v", err)
			}
			input.Disabled = true
			input.Fields = []domain.ProjectCredentialVariableInput{{Name: "REGISTRY_LOGIN"}, {Name: "REGISTRY_PASSWORD"}}
			if _, err := st.SaveProjectCredential(ctx, projectID, id, false, input); err != nil {
				t.Fatal(err)
			}
			if _, err := st.ResolveProjectCredentialEnv(ctx, projectID, []string{id}); !errors.Is(err, ErrProjectCredentialUnavailable) {
				t.Fatalf("disabled credential resolved: %v", err)
			}
			if env, err := st.ResolveAvailableProjectCredentialEnv(ctx, projectID, ids); err != nil || len(env) != 0 {
				t.Fatalf("only disabled or missing credentials should yield an empty environment: %v", err)
			}

			t.Setenv("AGENTFLOW_PROJECT_CREDENTIAL_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
			input.Disabled = false
			if _, err := st.SaveProjectCredential(ctx, projectID, id, false, input); err == nil {
				t.Fatal("an update with omitted values succeeded after the key changed")
			}
			input.Fields[0].Value, input.Fields[1].Value = &username, &replacement
			restored, err := st.SaveProjectCredential(ctx, projectID, id, false, input)
			if err != nil || restored.ID != id {
				t.Fatalf("replace all values with the new key: %v", err)
			}
			env, err = st.ResolveProjectCredentialEnv(ctx, projectID, []string{id})
			if err != nil || len(env) != 2 || env["REGISTRY_LOGIN"] != username || env["REGISTRY_PASSWORD"] != replacement {
				t.Fatalf("the same binding did not resolve after replacement: %v", err)
			}
		})
	}
}
