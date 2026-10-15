package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPRRepoFor(t *testing.T) {
	root := workspaceFixture(t)
	a := filepath.Join(root, "alpha")
	gitTestRun(t, a, "remote", "add", "origin", "git@github.com:me/sontaran.git")
	gitTestRun(t, a, "remote", "add", "upstream", "https://github.com/zenact/sontaran")
	if o, r, h, ok := prRepoFor(a, "main"); !ok || o != "zenact" || r != "sontaran" || h != "me" {
		t.Fatalf("fork: %s %s %s %v", o, r, h, ok)
	}
	// A branch tracking the upstream remote is pushed there, not to the fork.
	gitTestRun(t, a, "config", "branch.feat.remote", "upstream")
	if _, _, h, _ := prRepoFor(a, "feat"); h != "zenact" {
		t.Fatalf("upstream-tracking head owner = %q, want zenact", h)
	}
	b := filepath.Join(root, "beta")
	gitTestRun(t, b, "remote", "add", "origin", "https://github.com/zenact/x.git")
	if o, r, h, ok := prRepoFor(b, "main"); !ok || o != "zenact" || r != "x" || h != "zenact" {
		t.Fatalf("origin only: %s %s %s %v", o, r, h, ok)
	}
	gitTestRun(t, b, "remote", "set-url", "origin", "https://gitlab.com/zenact/x.git")
	if _, _, _, ok := prRepoFor(b, "main"); ok {
		t.Fatal("a gitlab remote must not resolve")
	}
	if _, _, _, ok := prRepoFor(filepath.Join(root, "alpha-wt"), "feat"); !ok {
		t.Fatal("a linked worktree shares its repo's remotes")
	}
}

// fakeGitHub stands in for api.github.com: REST under /repos, GraphQL at
// /graphql. handle sees every request; the URL vars are restored after the test.
func fakeGitHub(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body map[string]any)) *[]string {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, r.Method+" "+r.URL.Path)
		handle(w, r, body)
	}))
	t.Cleanup(srv.Close)
	oldAPI, oldGQL := githubAPIBase, githubGraphQLURL
	githubAPIBase, githubGraphQLURL = srv.URL, srv.URL+"/graphql"
	t.Cleanup(func() { githubAPIBase, githubGraphQLURL = oldAPI, oldGQL })
	return &seen
}

func prNode(num int, owner, review, checks string, threads ...[2]bool) map[string]any {
	ts := []any{}
	for _, th := range threads {
		ts = append(ts, map[string]any{"isResolved": th[0], "isOutdated": th[1]})
	}
	return map[string]any{
		"number": num, "title": fmt.Sprintf("PR %d", num), "url": fmt.Sprintf("https://github.com/o/r/pull/%d", num),
		"state": "OPEN", "isDraft": false, "reviewDecision": review, "headRefOid": "abc",
		"headRepositoryOwner": map[string]any{"login": owner},
		"commits":             map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": checks}}}}},
		"reviewThreads":       map[string]any{"nodes": ts},
	}
}

func TestFetchPRSummaries(t *testing.T) {
	var aliasesPerRequest []int
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		vars := body["variables"].(map[string]any)
		repo := map[string]any{}
		n := 0
		for k, v := range vars {
			if !strings.HasPrefix(k, "h") {
				continue
			}
			n++
			alias := "b" + strings.TrimPrefix(k, "h")
			switch v {
			case "feat":
				// Newest first: someone else's fork with the same branch name, then ours.
				repo[alias] = map[string]any{"nodes": []any{
					prNode(9, "someone-else", "APPROVED", "SUCCESS"),
					prNode(7, "me", "CHANGES_REQUESTED", "FAILURE", [2]bool{false, false}, [2]bool{true, false}, [2]bool{false, true}),
				}}
			default:
				repo[alias] = map[string]any{"nodes": []any{}}
			}
		}
		aliasesPerRequest = append(aliasesPerRequest, n)
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": repo}})
	})
	got, err := fetchPRSummaries(context.Background(), "tok", "o", "r", "me", []string{"feat", "none"})
	if err != nil {
		t.Fatal(err)
	}
	pr, ok := got["feat"]
	if !ok || pr.Number != 7 || pr.Review != "CHANGES_REQUESTED" || pr.Checks != "FAILURE" || pr.Unresolved != 1 {
		t.Fatalf("feat = %+v %v", pr, ok)
	}
	if _, ok := got["none"]; ok {
		t.Fatal("a branch with no PR must be absent")
	}
	var many []string
	for i := 0; i < 25; i++ {
		many = append(many, fmt.Sprintf("x%d", i))
	}
	aliasesPerRequest = nil
	if _, err := fetchPRSummaries(context.Background(), "tok", "o", "r", "me", many); err != nil {
		t.Fatal(err)
	}
	if len(aliasesPerRequest) != 2 || aliasesPerRequest[0] > 20 || aliasesPerRequest[1] > 20 {
		t.Fatalf("aliases per request = %v, want two requests of at most 20", aliasesPerRequest)
	}
}

func TestGitHubGraphQLErrors(t *testing.T) {
	fakeGitHub(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{"message": "bad credentials"}}})
	})
	_, err := fetchPRSummaries(context.Background(), "tok", "o", "r", "me", []string{"feat"})
	if err == nil || !strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("err = %v, want the GraphQL error surfaced", err)
	}
}
