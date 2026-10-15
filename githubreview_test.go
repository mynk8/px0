package main

import (
	"context"
	"encoding/json"
	"errors"
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

func TestPRRemoteName(t *testing.T) {
	root := workspaceFixture(t)
	a := filepath.Join(root, "alpha")
	gitTestRun(t, a, "remote", "add", "origin", "git@github.com:me/sontaran.git")
	gitTestRun(t, a, "remote", "add", "upstream", "https://github.com/zenact-ai/sontaran.git")
	if got := prRemoteName(a, "zenact-ai", "sontaran"); got != "upstream" {
		t.Fatalf("fork setup: %q, want upstream", got)
	}
	if got := prRemoteName(a, "ME", "Sontaran"); got != "origin" {
		t.Fatalf("case-insensitive origin match: %q", got)
	}
	if got := prRemoteName(a, "zenact-ai", "other"); got != "" {
		t.Fatalf("no matching remote: %q", got)
	}
}

// fakeGitHub stands in for api.github.com; it records "METHOD path?query".
func fakeGitHub(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *[]string {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	return &seen
}

func TestPRNumberFor(t *testing.T) {
	root := workspaceFixture(t)
	a := filepath.Join(root, "alpha")
	gitTestRun(t, a, "remote", "add", "origin", "https://github.com/me/x.git")
	gitTestRun(t, a, "remote", "add", "upstream", "https://github.com/o/x.git")
	seen := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("head") == "me:feat" {
			json.NewEncoder(w).Encode([]any{map[string]any{"number": 42}})
			return
		}
		json.NewEncoder(w).Encode([]any{})
	})
	ctx := context.Background()
	o, rp, n, err := prNumberFor(ctx, "tok", checkout{Prefix: "alpha-wt", Abs: filepath.Join(root, "alpha-wt")})
	if err != nil || o != "o" || rp != "x" || n != 42 {
		t.Fatalf("branch lookup: %s %s %d %v", o, rp, n, err)
	}
	if len(*seen) != 1 || !strings.Contains((*seen)[0], "/repos/o/x/pulls") || !strings.Contains((*seen)[0], "state=open") {
		t.Fatalf("requests = %v", *seen)
	}
	if _, _, _, err := prNumberFor(ctx, "tok", checkout{Prefix: "alpha", Abs: a}); !errors.Is(err, errNoPR) {
		t.Fatalf("main has no PR: err = %v, want errNoPR", err)
	}
	// A review checkout names its PR; no lookup needed.
	gitTestRun(t, a, "worktree", "add", "-q", "--detach", filepath.Join(root, "alpha-review-pr7"))
	*seen = nil
	if _, _, n, err := prNumberFor(ctx, "tok", checkout{Prefix: "alpha-review-pr7", Abs: filepath.Join(root, "alpha-review-pr7")}); err != nil || n != 7 || len(*seen) != 0 {
		t.Fatalf("review checkout: %d %v, requests %v", n, err, *seen)
	}
}

// prWorkspace is a fixture workspace whose alpha repo has fork-style remotes
// and whose alpha-wt branch (feat) has PR #42 on a fake GitHub.
func prWorkspace(t *testing.T) (*Server, string, *[]string) {
	t.Helper()
	root := workspaceFixture(t)
	isolateSettings(t)
	t.Setenv("GITHUB_TOKEN", "tok")
	a := filepath.Join(root, "alpha")
	gitTestRun(t, a, "remote", "add", "origin", "https://github.com/me/x.git")
	gitTestRun(t, a, "remote", "add", "upstream", "https://github.com/o/x.git")
	head := strings.TrimSpace(gitTestRun(t, filepath.Join(root, "alpha-wt"), "rev-parse", "HEAD"))
	seen := fakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/x/pulls" && r.URL.Query().Get("head") == "me:feat":
			json.NewEncoder(w).Encode([]any{map[string]any{"number": 42}})
		case r.URL.Path == "/repos/o/x/pulls":
			json.NewEncoder(w).Encode([]any{})
		case r.URL.Path == "/repos/o/x/pulls/42":
			json.NewEncoder(w).Encode(map[string]any{"number": 42, "title": "Feat", "state": "open",
				"user": map[string]any{"login": "me"}, "base": map[string]any{"ref": "main"},
				"head": map[string]any{"ref": "feat", "sha": head}})
		case r.URL.Path == "/repos/o/x":
			json.NewEncoder(w).Encode(map[string]any{"permissions": map[string]any{"push": true}})
		case r.URL.Path == "/repos/o/x/issues/42/comments":
			json.NewEncoder(w).Encode([]any{})
		case r.URL.Path == "/repos/o/x/pulls/42/comments":
			json.NewEncoder(w).Encode([]any{map[string]any{"id": 9, "body": "nit", "path": "sub/s.go", "line": 1, "side": "RIGHT", "user": map[string]any{"login": "rev"}}})
		default:
			http.NotFound(w, r)
		}
	})
	ix := NewIndex(root)
	ix.Build()
	return NewServer(ix, nil), root, seen
}

func TestWorkspacePRRoutesPerCheckout(t *testing.T) {
	s, _, _ := prWorkspace(t)
	code, m := get(t, s, "/api/pr/meta?repo=alpha-wt")
	if code != 200 || m["number"] != float64(42) || m["writeAccess"] != true {
		t.Fatalf("meta: %d %v", code, m)
	}
	_, m = get(t, s, "/api/pr/existing-comments?repo=alpha-wt")
	rc := m["reviewComments"].([]any)
	if len(rc) != 1 || rc[0].(map[string]any)["path"] != "alpha-wt/sub/s.go" {
		t.Fatalf("existing comments not prefixed: %v", m)
	}
	// Drafts cross the boundary workspace-relative.
	code, c := agentPostJSON(t, s, "/api/pr/comments?repo=alpha-wt", map[string]any{"path": "alpha-wt/sub/s.go", "line": 1, "side": "RIGHT", "body": "fix"})
	if code != 200 || c["path"] != "alpha-wt/sub/s.go" {
		t.Fatalf("draft: %d %v", code, c)
	}
	if p, _, _ := s.prFor(nil, httptestRequest("/api/pr/comments?repo=alpha-wt")); p == nil || len(p.comments) != 1 || p.comments[0].Path != "sub/s.go" {
		t.Fatal("draft must be stored relative to the checkout")
	}
	if code, _ := agentPostJSON(t, s, "/api/pr/comments?repo=alpha-wt", map[string]any{"path": "beta/b.go", "line": 1, "body": "x"}); code != 400 {
		t.Fatalf("draft outside the checkout: %d, want 400", code)
	}
	_, m = get(t, s, "/api/pr/comments?repo=alpha-wt")
	if d := m["comments"].([]any); len(d) != 1 || d[0].(map[string]any)["path"] != "alpha-wt/sub/s.go" {
		t.Fatalf("drafts list: %v", m)
	}
	if code, _ := get(t, s, "/api/pr/meta?repo=beta"); code != 404 {
		t.Fatalf("checkout without a PR: %d, want 404", code)
	}
	if code, _ := get(t, s, "/api/pr/meta?repo=nope"); code != 400 {
		t.Fatalf("unknown checkout: %d, want 400", code)
	}
	if code, _ := get(t, s, "/api/pr/meta"); code != 400 {
		t.Fatalf("no repo in workspace mode: %d, want 400", code)
	}
}

func httptestRequest(url string) *http.Request { return httptest.NewRequest(http.MethodGet, url, nil) }
