package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workspaceFixture builds a folder of checkouts like ~/workspace/zenact:
// two repos, a linked worktree, a plain folder and an empty .git dir.
func workspaceFixture(t *testing.T) string {
	t.Helper()
	if !gitInstalled() {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	initRepo := func(dir string) {
		d := filepath.Join(root, dir)
		gitTestRun(t, d, "init", "-q", "-b", "main")
		gitTestRun(t, d, "config", "user.email", "t@example.com")
		gitTestRun(t, d, "config", "user.name", "T")
		gitTestRun(t, d, "config", "commit.gpgsign", "false")
		gitTestRun(t, d, "add", "-A")
		gitTestRun(t, d, "commit", "-qm", "init")
	}
	write("alpha/a.go", "one\n")
	write("alpha/sub/s.go", "s\n")
	initRepo("alpha")
	write("beta/b.go", "b\n")
	initRepo("beta")
	gitTestRun(t, filepath.Join(root, "alpha"), "worktree", "add", "-q", "-b", "feat", filepath.Join(root, "alpha-wt"))
	write("docs/notes.md", "notes\n")
	os.MkdirAll(filepath.Join(root, "empty", ".git"), 0o755)
	write("empty/e.txt", "e\n")
	return root
}

func TestWorkspaceDiscovery(t *testing.T) {
	root := workspaceFixture(t)
	ix := NewIndex(root)
	ix.Build()
	if !ix.Workspace() {
		t.Fatal("Workspace() = false for a folder of repos")
	}
	var got []string
	for _, c := range ix.Checkouts() {
		got = append(got, c.Prefix)
	}
	want := []string{"alpha", "alpha-wt", "beta"}
	if len(got) != len(want) {
		t.Fatalf("checkouts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("checkouts = %v, want %v", got, want)
		}
	}
	c, rr, ok := ix.RepoFor("alpha/sub/s.go")
	if !ok || c.Prefix != "alpha" || rr != "sub/s.go" || c.Abs != filepath.Join(root, "alpha") {
		t.Fatalf("RepoFor = %+v %q %v", c, rr, ok)
	}
	if _, _, ok := ix.RepoFor("docs/notes.md"); ok {
		t.Fatal("docs/notes.md should not belong to a checkout")
	}
	if _, _, ok := ix.RepoFor("alpha-wtx/f"); ok {
		t.Fatal("prefix match must stop at a path boundary")
	}
	for _, f := range ix.Files() {
		if filepath.Base(f.Path) == ".git" {
			t.Fatalf("worktree .git file indexed: %s", f.Path)
		}
	}
	if !ix.GitOn() {
		t.Fatal("GitOn() = false in workspace mode")
	}
}

func TestWorkspaceOffForSingleRepo(t *testing.T) {
	root := gitRepo(t)
	ix := NewIndex(root)
	ix.Build()
	if ix.Workspace() {
		t.Fatal("a single repo must not turn on workspace mode")
	}
}

func TestWorkspaceStatusMerged(t *testing.T) {
	root := workspaceFixture(t)
	os.WriteFile(filepath.Join(root, "alpha/a.go"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(root, "beta/new.go"), []byte("n\n"), 0o644)
	os.WriteFile(filepath.Join(root, "alpha-wt/sub/s.go"), []byte("changed\n"), 0o644)
	// A repo nested in a checkout: the outer porcelain lists it as "inner/".
	os.MkdirAll(filepath.Join(root, "beta/inner"), 0o755)
	os.WriteFile(filepath.Join(root, "beta/inner/i.go"), []byte("i\n"), 0o644)
	gitTestRun(t, filepath.Join(root, "beta/inner"), "init", "-q")

	ix := NewIndex(root)
	ix.Build()
	st := ix.GitStatusMap()
	want := map[string]string{"alpha/a.go": "M", "beta/new.go": "U", "alpha-wt/sub/s.go": "M", "beta/inner/i.go": "U"}
	for k, v := range want {
		if st[k] != v {
			t.Errorf("status[%q] = %q, want %q (all: %v)", k, st[k], v, st)
		}
	}
	for k := range st {
		if strings.HasSuffix(k, "/") {
			t.Errorf("directory key leaked into status: %q", k)
		}
	}
	kids, _ := ix.Children("")
	for _, k := range kids {
		if (k.Name == "alpha" || k.Name == "alpha-wt") && !k.Dirty {
			t.Errorf("%s should be dirty", k.Name)
		}
	}
	if ch, _ := ix.CheckoutCounts("alpha"); ch != 1 {
		t.Errorf("CheckoutCounts(alpha) = %d, want 1", ch)
	}
	// Refreshing one checkout keeps the others' cached status.
	os.WriteFile(filepath.Join(root, "beta/b.go"), []byte("bb\n"), 0o644)
	ix.UpdateGitStatusFor([]string{"beta"})
	st = ix.GitStatusMap()
	if st["beta/b.go"] != "M" || st["alpha/a.go"] != "M" {
		t.Errorf("after partial refresh: %v", st)
	}
}
