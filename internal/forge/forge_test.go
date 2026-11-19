package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agentflow/internal/domain"
)

func TestBindRepositoryRequiresTrustedMatchingClone(t *testing.T) {
	t.Setenv(InstancesEnv, "https://FORGE.example.org:443/code/, https://other.example.org")
	r, remote, err := BindRepository("https://forge.example.org/code/Acme/Widget.git", "ssh://git@forge.example.org:2222/Acme/Widget.git")
	if err != nil || r.Project() != "forge:https://forge.example.org/code/acme/widget" || remote.Port != "2222" {
		t.Fatalf("binding = %+v, %+v, %v", r, remote, err)
	}
	for _, tc := range []struct{ web, clone string }{
		{"https://forge.example.org/other/acme/widget", "git@forge.example.org:acme/widget.git"},
		{"https://forge.example.org/code/acme/widget", "git@other.example.org:acme/widget.git"},
		{"https://forge.example.org/code/acme/widget", "git@forge.example.org:acme/else.git"},
		{"https://user:secret@forge.example.org/code/acme/widget", "git@forge.example.org:acme/widget.git"},
		{"https://forge.example.org/code/acme/widget?token=secret", "git@forge.example.org:acme/widget.git"},
		{"https://forge.example.org/code/acme/widget", "ssh://git:secret@forge.example.org/acme/widget.git"},
	} {
		if _, _, err := BindRepository(tc.web, tc.clone); err == nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("invalid binding errored unsafely: %v", err)
		}
	}
}

func useTestForge(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	old := apiClient
	apiClient = newAPIClient(server.Client().Transport)
	t.Cleanup(func() { apiClient = old })
	t.Setenv(InstancesEnv, server.URL+"/code")
	return server.URL + "/code"
}

func TestForgeSubmitAndMergeSameRepositoryHead(t *testing.T) {
	const current = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const stale = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, scenario := range []string{"create", "update", "moved", "protected"} {
		t.Run(scenario, func(t *testing.T) {
			const prefix = "/code/api/v1/repos/acme/app"
			var wrote, merged bool
			branch := func(name, ref, sha string, id int64) pullBranch {
				return pullBranch{Name: name, Ref: ref, Commit: sha, RepoID: id}
			}
			pull := Pull{Number: 9, State: "open", Base: branch("main", "main", "", 31), Head: branch("feature/fix", "refs/pull/9/head", current, 31)}
			instance := useTestForge(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "token test-credential" {
					t.Error("request did not use the configured token")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET " + prefix:
					_, _ = w.Write([]byte(`{"id":31,"full_name":"Acme/App"}`))
				case "GET " + prefix + "/pulls":
					if r.URL.Query().Get("state") != "open" || r.URL.Query().Get("head") != "feature/fix" || r.URL.Query().Get("base") != "main" {
						t.Errorf("unexpected search: %s", r.URL.RawQuery)
					}
					fork, historical := pull, pull
					fork.Number, fork.Head.RepoID = 12, 43
					historical.Number, historical.State, historical.Merged = 11, "closed", true
					switch r.URL.Query().Get("page") {
					case "1":
						w.Header().Set("Link", `<https://ignored.example.org>; rel="next"`)
						_ = json.NewEncoder(w).Encode([]Pull{fork, historical})
					default:
						items := []Pull{}
						if scenario != "create" {
							p := pull
							if scenario == "moved" {
								p.Head.Commit = stale
							}
							items = append(items, p)
						}
						_ = json.NewEncoder(w).Encode(items)
					}
				case "POST " + prefix + "/pulls", "PATCH " + prefix + "/pulls/9":
					wrote = true
					var body map[string]string
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["title"] != "delivery" || (r.Method == http.MethodPost && (body["head"] != "feature/fix" || body["base"] != "main")) {
						t.Errorf("wrong mutation: %v", body)
					}
					_ = json.NewEncoder(w).Encode(pull)
				case "POST " + prefix + "/pulls/9/merge":
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["head_commit_id"] != current || body["do"] != "merge" || body["delete_branch_after_merge"] != false || body["force_merge"] != nil {
						t.Errorf("unsafe merge input: %v", body)
					}
					if scenario == "protected" {
						http.Error(w, "test-credential", http.StatusMethodNotAllowed)
						return
					}
					merged = true
					w.WriteHeader(http.StatusNoContent)
				case "GET " + prefix + "/pulls/9":
					p := pull
					p.Merged = merged
					_ = json.NewEncoder(w).Encode(p)
				default:
					t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			ctx := t.Context()
			ref, _ := domain.ParseForgeRepositoryURL(instance + "/acme/app")
			c, err := OpenRepository(ctx, ref, "test-credential")
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.SubmitPull(ctx, "feature/fix", "main", current, "delivery", "body", true)
			if scenario == "moved" {
				if err == nil || wrote {
					t.Fatalf("stale PR was accepted: %+v, %v", got, err)
				}
				return
			}
			existing := scenario != "create"
			if err != nil || !wrote || got.Pull.Number != 9 || got.Existing != existing || got.Updated != existing {
				t.Fatalf("delivery = %+v, %v", got, err)
			}
			gotMerge, err := c.MergePull(ctx, got.Pull, "feature/fix", "main", current)
			if scenario == "protected" {
				if err == nil || strings.Contains(err.Error(), "test-credential") || merged {
					t.Fatalf("protected merge = %+v, %v", gotMerge, err)
				}
			} else if err != nil || gotMerge.Delivery(ref).State != "merged" {
				t.Fatalf("merge = %+v, %v", gotMerge, err)
			}
		})
	}
}

func TestForgeBranchContainsChecksCommitAncestry(t *testing.T) {
	const (
		head     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		advanced = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		prefix   = "/code/api/v1/repos/acme/app"
		branch   = "agentflow/integration"
	)
	for _, tc := range []struct {
		name, tip, response string
		contains, wantErr   bool
	}{
		{name: "identical", tip: head, contains: true},
		{name: "ancestor", tip: advanced, response: `{"total_commits":0}`, contains: true},
		{name: "not integrated", tip: advanced, response: `{"total_commits":1}`},
		{name: "unconfirmed", tip: advanced, response: `{}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var compared atomic.Bool
			instance := useTestForge(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case prefix:
					_, _ = w.Write([]byte(`{"id":31,"full_name":"acme/app"}`))
				case prefix + "/branches/" + branch:
					_ = json.NewEncoder(w).Encode(map[string]any{"name": branch, "commit": map[string]string{"id": tc.tip}})
				case prefix + "/compare/" + advanced + "..." + head:
					compared.Store(true)
					if r.URL.Query().Get("files") != "false" || r.URL.Query().Get("verification") != "false" {
						t.Errorf("unexpected comparison options: %s", r.URL.RawQuery)
					}
					_, _ = w.Write([]byte(tc.response))
				default:
					t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			ref, _ := domain.ParseForgeRepositoryURL(instance + "/acme/app")
			client, err := OpenRepository(t.Context(), ref, "test-credential")
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.BranchContains(t.Context(), branch, head)
			if got != tc.contains || (err != nil) != tc.wantErr || compared.Load() != (tc.tip != head) {
				t.Fatalf("contains=%v compared=%v err=%v", got, compared.Load(), err)
			}
		})
	}
}

func TestForgeTokenNeverFollowsRedirect(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed a redirect with a Forge credential")
	}))
	defer sink.Close()
	instance := useTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/code/api/v1/user" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		http.Redirect(w, r, sink.URL, http.StatusFound)
	})
	if err := VerifyToken(context.Background(), instance, "test-credential"); err != statusError(http.StatusFound) {
		t.Fatalf("verification did not stop at redirect: %v", err)
	}
	if err := VerifyToken(context.Background(), strings.TrimSuffix(instance, "/code"), "test-credential"); err != ErrInstanceNotAllowed {
		t.Fatalf("unlisted path accepted: %v", err)
	}
}

func TestForgeBranchPullsCarryHistoricalAndCurrentHeads(t *testing.T) {
	const current = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const previous = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	instance := useTestForge(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("head") != "feature/fix" || r.URL.Query().Get("base") != "main" {
				t.Errorf("wrong branch query %s", r.URL.RawQuery)
			}
			pull := Pull{Number: 3, State: "closed", Merged: true,
				Head: pullBranch{Name: "feature/fix", Commit: previous, RepoID: 31},
				Base: pullBranch{Name: "main", RepoID: 31}}
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("Link", `<https://ignored.example.org>; rel="next"`)
				fork := pull
				fork.Number, fork.Head.RepoID = 4, 99
				_ = json.NewEncoder(w).Encode([]Pull{fork, pull})
			} else {
				pull.Number, pull.State, pull.Merged, pull.Head.Commit = 5, "open", false, current
				_ = json.NewEncoder(w).Encode([]Pull{pull})
			}
			return
		}
		_, _ = w.Write([]byte(`{"id":31,"full_name":"Acme/App"}`))
	})
	client, err := OpenRepository(t.Context(), domain.ForgeRepository{Instance: instance, Owner: "acme", Name: "app"}, "test-credential")
	if err != nil {
		t.Fatal(err)
	}
	pulls, err := client.BranchPulls(t.Context(), "feature/fix", "main")
	if err != nil || len(pulls) != 2 {
		t.Fatalf("branch PRs = %+v, %v", pulls, err)
	}
	if pulls[0].Delivery(client.ref).HeadSHA != previous || pulls[1].Delivery(client.ref).HeadSHA != current {
		t.Fatal("branch discovery dropped the commit used by MR verification")
	}
}
