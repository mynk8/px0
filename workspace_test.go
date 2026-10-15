package main

import (
	"bytes"
	"io"
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

func TestWorkspaceFileDiffGutterStage(t *testing.T) {
	root := workspaceFixture(t)
	os.WriteFile(filepath.Join(root, "alpha/sub/s.go"), []byte("s2\n"), 0o644)
	ix := NewIndex(root)
	ix.Build()
	s := NewServer(ix, nil)

	if code, m := get(t, s, "/api/meta"); code != 200 || m["git"] != true || m["workspace"] != true {
		t.Fatalf("meta: %d %v", code, m)
	}
	if _, m := get(t, s, "/api/file?path=alpha/sub/s.go"); m["diffAvailable"] != true {
		t.Fatalf("diffAvailable: %v", m)
	}
	if _, m := get(t, s, "/api/diff?path=alpha/sub/s.go"); m["available"] != true || !strings.Contains(m["diff"].(string), "+s2") {
		t.Fatalf("diff: %v", m)
	}
	if _, m := get(t, s, "/api/gutter?path=alpha/sub/s.go"); m["available"] != true {
		t.Fatalf("gutter: %v", m)
	}
	// Outside every checkout: opens, no diff, never 500.
	if code, m := get(t, s, "/api/file?path=docs/notes.md"); code != 200 || m["diffAvailable"] == true {
		t.Fatalf("plain file: %d %v", code, m)
	}
	if code, m := get(t, s, "/api/diff?path=docs/notes.md"); code != 200 || m["available"] == true {
		t.Fatalf("plain diff: %d %v", code, m)
	}
	if code, _ := agentPostJSON(t, s, "/api/git/stage", map[string]string{"path": "alpha/sub/s.go"}); code != 200 {
		t.Fatalf("stage: %d", code)
	}
	if out := gitTestRun(t, filepath.Join(root, "alpha"), "diff", "--cached", "--name-only"); strings.TrimSpace(out) != "sub/s.go" {
		t.Fatalf("staged in alpha = %q", out)
	}
	if code, _ := agentPostJSON(t, s, "/api/git/unstage", map[string]string{"path": "alpha/sub/s.go"}); code != 200 {
		t.Fatalf("unstage: %d", code)
	}
}

func TestWorkspaceWholeRepoEndpoints(t *testing.T) {
	root := workspaceFixture(t)
	os.WriteFile(filepath.Join(root, "beta/b.go"), []byte("b2\n"), 0o644)
	ix := NewIndex(root)
	ix.Build()
	s := NewServer(ix, nil)

	// No repo, or an unknown one: refused, nothing runs in the workspace root.
	if code, _ := agentPostJSON(t, s, "/api/git/stage", map[string]string{"path": "."}); code != 400 {
		t.Fatalf("stage-all without repo: %d, want 400", code)
	}
	if code, _ := agentPostJSON(t, s, "/api/git/commit?repo=nope", map[string]string{"message": "x"}); code != 400 {
		t.Fatalf("commit unknown repo: %d, want 400", code)
	}
	if code, _ := agentPost(t, s, "/api/git/push"); code != 400 {
		t.Fatalf("push without repo: %d, want 400", code)
	}
	if code, _ := agentPostJSON(t, s, "/api/git/stage?repo=beta", map[string]string{"path": "."}); code != 200 {
		t.Fatalf("stage-all beta: %d", code)
	}
	if code, m := agentPostJSON(t, s, "/api/git/commit?repo=beta", map[string]string{"message": "beta change"}); code != 200 {
		t.Fatalf("commit beta: %d %v", code, m)
	}
	if out := gitTestRun(t, filepath.Join(root, "beta"), "log", "-1", "--format=%s"); strings.TrimSpace(out) != "beta change" {
		t.Fatalf("beta head = %q", out)
	}
	_, m := get(t, s, "/api/git/log?repo=beta")
	commits := m["commits"].([]any)
	sha := commits[0].(map[string]any)["full"].(string)
	_, m = get(t, s, "/api/commitfiles?repo=beta&sha="+sha)
	f0 := m["files"].([]any)[0].(map[string]any)
	if f0["path"] != "beta/b.go" {
		t.Fatalf("commit file path = %v, want beta/b.go", f0["path"])
	}
	if code, _ := get(t, s, "/api/commitdetail?repo=beta&sha="+sha); code != 200 {
		t.Fatalf("commitdetail: %d", code)
	}
	if _, m := get(t, s, "/api/diff?path=beta/b.go&ref="+sha); m["available"] != true {
		t.Fatalf("commit diff: %v", m)
	}
}

func TestWorkspaceRepoStatesAndWatcher(t *testing.T) {
	root := workspaceFixture(t)
	ix := NewIndex(root)
	ix.Build()
	gw := NewGitWatcher(ix)
	gw.scanCheckoutMeta() // record the starting stamps
	p := gw.Refresh()
	if !p.Workspace || len(p.Repos) != 3 {
		t.Fatalf("payload workspace=%v repos=%v", p.Workspace, p.Repos)
	}
	by := map[string]RepoState{}
	for _, r := range gw.RepoStates() {
		by[r.Path] = r
	}
	if by["alpha-wt"].Group != "alpha" || by["alpha-wt"].Main || !by["alpha"].Main {
		t.Fatalf("grouping: %+v", by)
	}
	if by["alpha-wt"].Branch != "feat" || by["beta"].Branch != "main" || by["beta"].Head == "" {
		t.Fatalf("branches: %+v", by)
	}
	// A CLI command in one checkout is seen through its metadata.
	os.WriteFile(filepath.Join(root, "beta/c.go"), []byte("c\n"), 0o644)
	gitTestRun(t, filepath.Join(root, "beta"), "add", "c.go")
	changed := gw.scanCheckoutMeta()
	if len(changed) != 1 || changed[0] != "beta" {
		t.Fatalf("scanCheckoutMeta = %v, want [beta]", changed)
	}
	gw.refreshFor(changed)
	for _, r := range gw.RepoStates() {
		if r.Path == "beta" && (r.Staged != 1 || r.Changes != 1) {
			t.Fatalf("beta = %+v, want 1 staged, 1 change", r)
		}
	}
	s := NewServer(ix, nil)
	if _, m := get(t, s, "/api/repos"); len(m["repos"].([]any)) != 3 {
		t.Fatalf("/api/repos = %v", m)
	}
}

func TestWorkspaceThisBranchBase(t *testing.T) {
	root := workspaceFixture(t)
	wt := filepath.Join(root, "alpha-wt")
	os.WriteFile(filepath.Join(wt, "feat.go"), []byte("f\n"), 0o644)
	gitTestRun(t, wt, "add", "feat.go")
	gitTestRun(t, wt, "commit", "-qm", "feat")
	ix := NewIndex(root)
	ix.Build()
	s := NewServer(ix, nil)
	// Committed on the branch: invisible against HEAD.
	if ix.GitStatusMap()["alpha-wt/feat.go"] != "" {
		t.Fatal("committed file should be clean against HEAD")
	}
	if code, m := agentPost(t, s, "/api/git/base?repo=alpha-wt&mode=branch"); code != 200 || m["parent"] != "main" {
		t.Fatalf("base: %d %v", code, m)
	}
	if ix.GitStatusMap()["alpha-wt/feat.go"] != "A" {
		t.Fatalf("this-branch status: %v", ix.GitStatusMap())
	}
	if _, m := get(t, s, "/api/diff?path=alpha-wt/feat.go"); m["available"] != true {
		t.Fatalf("this-branch diff: %v", m)
	}
	if code, _ := agentPost(t, s, "/api/git/base?repo=alpha-wt&mode=sideways"); code != 400 {
		t.Fatalf("bad mode: %d, want 400", code)
	}
	agentPost(t, s, "/api/git/base?repo=alpha-wt&mode=head")
	if ix.GitStatusMap()["alpha-wt/feat.go"] != "" {
		t.Fatal("back to HEAD should hide committed changes")
	}
}

func TestParentRefTracksFeatureBranch(t *testing.T) {
	root := workspaceFixture(t)
	a := filepath.Join(root, "alpha")
	gitTestRun(t, a, "branch", "base-feature")
	gitTestRun(t, a, "branch", "child", "base-feature")
	gitTestRun(t, a, "config", "branch.child.remote", ".")
	gitTestRun(t, a, "config", "branch.child.merge", "refs/heads/base-feature")
	if got := parentRef(a, "child"); got != "base-feature" {
		t.Fatalf("parentRef = %q, want base-feature", got)
	}
	if got := parentRef(a, "feat"); got != "main" {
		t.Fatalf("trunk fallback = %q, want main", got)
	}
}

func TestSetCommand(t *testing.T) {
	root := workspaceFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var buf bytes.Buffer
	// From a subfolder: the checkout top level is stored.
	if err := runSetCmd([]string{"add", filepath.Join(root, "alpha/sub"), "-note", "review filter"}, &buf); err != nil {
		t.Fatal(err)
	}
	ws := readSet()
	if len(ws.Items) != 1 || ws.Items[0].Path != filepath.Join(root, "alpha") || ws.Items[0].Note != "review filter" {
		t.Fatalf("set = %+v", ws)
	}
	// Adding again updates the note, no duplicate; -note may come first.
	if err := runSetCmd([]string{"add", "-note", "v2", filepath.Join(root, "alpha")}, &buf); err != nil {
		t.Fatal(err)
	}
	if ws = readSet(); len(ws.Items) != 1 || ws.Items[0].Note != "v2" {
		t.Fatalf("re-add = %+v", ws)
	}
	if err := runSetCmd([]string{"add", filepath.Join(root, "docs")}, &buf); err == nil {
		t.Fatal("adding a non-checkout should fail")
	}
	buf.Reset()
	runSetCmd([]string{"list"}, &buf)
	if !strings.Contains(buf.String(), filepath.Join(root, "alpha")+"\tv2") {
		t.Fatalf("list = %q", buf.String())
	}
	runSetCmd([]string{"remove", filepath.Join(root, "alpha")}, &buf)
	if len(readSet().Items) != 0 {
		t.Fatal("remove failed")
	}
}

func TestAgentNameFromCmdline(t *testing.T) {
	cases := map[string]string{
		"/home/u/.local/bin/claude\x00-p\x00hi\x00":                    "claude",
		"node\x00/usr/lib/node_modules/@openai/codex/bin/codex.js\x00": "codex",
		"codex\x00exec\x00":        "codex",
		"/usr/bin/vim\x00file\x00": "",
		"bash\x00-c\x00claude\x00": "",
	}
	for in, want := range cases {
		if got := agentNameFromCmdline([]byte(in)); got != want {
			t.Errorf("agentNameFromCmdline(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoStatesCarrySetAndNote(t *testing.T) {
	root := workspaceFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runSetCmd([]string{"add", filepath.Join(root, "beta"), "-note", "wip"}, io.Discard)
	ix := NewIndex(root)
	ix.Build()
	gw := NewGitWatcher(ix)
	gw.Refresh()
	for _, r := range gw.RepoStates() {
		if r.Path == "beta" && (!r.InSet || r.Note != "wip") {
			t.Fatalf("beta = %+v", r)
		}
		if r.Path == "alpha" && r.InSet {
			t.Fatal("alpha should not be in the set")
		}
	}
	// A change to the set file is picked up by the poll.
	runSetCmd([]string{"add", filepath.Join(root, "alpha")}, io.Discard)
	if !gw.pollSet() {
		t.Fatal("pollSet did not see the new item")
	}
	for _, r := range gw.RepoStates() {
		if r.Path == "alpha" && !r.InSet {
			t.Fatal("alpha should now be in the set")
		}
	}
}

func TestScopedFindAndSearch(t *testing.T) {
	root := workspaceFixture(t)
	ix := NewIndex(root)
	ix.Build()
	s := NewServer(ix, nil)
	_, m := get(t, s, "/api/find?q=go&scope=beta")
	res := m["results"].([]any)
	if len(res) == 0 {
		t.Fatal("scoped find found nothing in beta")
	}
	for _, r := range res {
		if p := r.(map[string]any)["path"].(string); !strings.HasPrefix(p, "beta/") {
			t.Fatalf("find leaked %s", p)
		}
	}
	_, m = get(t, s, "/api/search?q=one&scope=alpha-wt")
	for _, r := range m["results"].([]any) {
		if p := r.(map[string]any)["path"].(string); !strings.HasPrefix(p, "alpha-wt/") {
			t.Fatalf("search leaked %s", p)
		}
	}
	if m["files"].(float64) == 0 {
		t.Fatal("scoped search found nothing in alpha-wt/a.go")
	}
	if got := scopeFiles(ix.Files(), "alpha"); len(got) != 2 {
		t.Fatalf("scopeFiles(alpha) = %v, want a.go and sub/s.go only", got)
	}
}

func TestWorkspaceAgentRunsInCheckout(t *testing.T) {
	root := workspaceFixture(t)
	out := t.TempDir()
	// The harness edits sub/s.go relative to its cwd and records where it ran.
	s := agentServer(t, root, writeHarness(t,
		"pwd > "+filepath.Join(out, "pwd.txt")+"\nprintf 'touched\\n' >> sub/s.go\nprintf '%s' \"$1\" > "+filepath.Join(out, "prompt.txt")+"\n"))
	if code, _ := agentPost(t, s, "/api/agent/edit?path=alpha/sub/s.go&l1=1&l2=1&instruction=hi"); code != 200 {
		t.Fatalf("edit = %d, want 200", code)
	}
	job := waitIdle(t, s)
	if job.Error != "" {
		t.Fatalf("harness failed: %s (log: %s)", job.Error, job.Log)
	}
	pwd, _ := os.ReadFile(filepath.Join(out, "pwd.txt"))
	if strings.TrimSpace(string(pwd)) != filepath.Join(root, "alpha") {
		t.Fatalf("harness ran in %q, want the alpha checkout", pwd)
	}
	if len(job.Changed) != 1 || job.Changed[0] != "alpha/sub/s.go" {
		t.Fatalf("changed = %v, want [alpha/sub/s.go]", job.Changed)
	}
	if !job.Tracked {
		t.Fatal("tracked should be true inside a checkout")
	}
	prompt, _ := os.ReadFile(filepath.Join(out, "prompt.txt"))
	if !strings.Contains(string(prompt), "@sub/s.go line 1") {
		t.Fatalf("prompt should name the file relative to the checkout:\n%s", prompt)
	}
}
