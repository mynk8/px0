package main

import (
	"os"
	"path/filepath"
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
