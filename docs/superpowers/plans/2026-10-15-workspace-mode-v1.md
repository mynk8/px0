# Workspace Mode v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Open a folder of many git repos and worktrees in px0 and review, iterate and drive agents across them: git everywhere, a working set of checkouts that agents control, per-checkout scope, "This branch" diffs, and the coding harness running in the right checkout.

**Architecture:** A new `workspace.go` owns checkout discovery, longest-prefix routing (`RepoFor`), per-checkout status caches, branch-base inference, the working set file and agent-process detection. Existing code changes only at its git call sites (root → owning checkout) and in the watcher (one metadata record per checkout). The frontend gets one new module, `web/src/workspace.js`, that renders the grouped explorer root, the scope bar and the active-repo plumbing, reusing px0's tree rows, sidebar toggle, diff menu and git panel.

**Tech Stack:** Go 1.25 (stdlib only), git CLI, plain ES modules bundled by `scripts/build-web.js`.

**Spec:** `docs/superpowers/specs/2026-10-15-workspace-mode-git-design.md` (part 1) and `docs/superpowers/specs/2026-10-15-workspace-mode-ui-design.md` (part 2, "v1 scope" section).

## Global Constraints

- Single-repo behaviour unchanged: the full existing test suite passes untouched.
- Workspace mode turns on only when the opened folder is a directory, is not inside a git repo, and contains at least one valid checkout. No flag. `-no-git` turns all git off.
- No files written into the workspace. Per-user state lives under `~/.px0/` (or `$XDG_CONFIG_HOME/px0/`).
- Single static binary, no CGO, no new Go dependencies.
- Read-only for history: nothing creates, rebases or pushes branches beyond px0's existing git panel actions.
- Run Go commands with `GOWORK=off` (the workspace root has an unrelated `go.work`) and build `web/app.js` first: `node scripts/build-web.js`.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. A folder with an empty `.git` directory (the zenact root has one) must not count as a checkout, and must not make the root look like a repo. Pinned in Task 1.
2. An outer checkout's porcelain lists a nested repo as an untracked `inner/` entry; that bogus key must not reach the status map. Pinned in Task 2.
3. A whole-repo action (commit, push, stage-all) in workspace mode with no or an unknown `repo` must fail with 400, never run git in the workspace root or the server's cwd. Pinned in Task 4.
4. A file outside every checkout (`docs/notes.md`) opens normally with `diffAvailable: false`, and diff/gutter return `available: false`, never 500. Pinned in Task 3.
5. `px0 set add` from a subfolder of a worktree adds the worktree top level, and a path that is not a git checkout fails with a clear error. Pinned in Task 7.

---

## File Structure

| File | Responsibility |
|---|---|
| `workspace.go` (new) | checkout type, discovery helpers, `RepoFor`, status cache refresh/merge, `RepoState`, trunk/parent/base inference, scope filtering of the file list |
| `workingset.go` (new) | `~/.px0/set.json` read/write, `px0 set` CLI, agent-process scan |
| `index.go` | discovery hook in the walk, `.git` file skip, workspace detection in `Build`, `UpdateGitStatusFor` |
| `git.go` | `gitCommonDir` |
| `git_watcher.go` | per-checkout meta polling, repo states, working-set/agent polling, payload fields |
| `server.go` | per-file routing, `repo` param for whole-repo endpoints, `/api/git/base`, `/api/repos`, `scope` on find/search, meta |
| `search.go` | `SearchOpts.Scope` |
| `agent.go`, `thread.go` | harness runs in the owning checkout |
| `main.go` | `px0 set` subcommand dispatch |
| `workspace_test.go` (new) | all new Go tests |
| `web/src/workspace.js` (new) | grouped root, view modes, scope, `activeRepo`, `repoQuery` |
| `web/src/tree.js`, `gitpanel.js`, `gitstream.js`, `diff.js`, `palette.js`, `search.js`, `unpushed.js`, `commitfiles.js`, `main.js`, `web/index.html`, `web/style.css` | hooks into the above |

---

### Task 1: Checkout discovery and routing

**Files:**
- Create: `workspace.go`, `workspace_test.go`
- Modify: `index.go` (Index struct :46-64, walk loop :385-401, end of `Build` :472-486)

**Interfaces:**
- Produces:
  - `type checkout struct { Prefix, Abs string }`
  - `func (ix *Index) Workspace() bool`
  - `func (ix *Index) Checkouts() []checkout` (sorted by Prefix)
  - `func (ix *Index) RepoFor(rel string) (c checkout, repoRel string, ok bool)` — longest prefix; `repoRel` is `rel` with `c.Prefix + "/"` removed
  - `func (ix *Index) CheckoutByPrefix(p string) (checkout, bool)`
  - `func (ix *Index) GitOn() bool` — single mode: `gitAvailable(ix.root)`; workspace: `!gitDisabled && len(checkouts) > 0`
  - test helper `workspaceFixture(t) string` returning a root with `alpha/` (repo), `alpha-wt/` (linked worktree of alpha on branch `feat`), `beta/` (repo), `docs/notes.md` (plain), `empty/.git/` (empty dir, not a repo)

- [ ] **Step 1: Write the failing tests**

```go
// workspace_test.go
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
		gitTestRun(t, filepath.Join(root, dir), "init", "-q", "-b", "main")
		gitTestRun(t, filepath.Join(root, dir), "config", "user.email", "t@example.com")
		gitTestRun(t, filepath.Join(root, dir), "config", "user.name", "T")
		gitTestRun(t, filepath.Join(root, dir), "config", "commit.gpgsign", "false")
		gitTestRun(t, filepath.Join(root, dir), "add", "-A")
		gitTestRun(t, filepath.Join(root, dir), "commit", "-qm", "init")
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `node scripts/build-web.js && GOWORK=off go test -count=1 -run 'TestWorkspace' .`
Expected: FAIL to compile — `ix.Workspace undefined`.

- [ ] **Step 3: Implement**

Add to the `Index` struct in `index.go`:

```go
	workspace bool       // root is not a repo but holds checkouts (workspace.go)
	checkouts []checkout // sorted by Prefix
	repoGS    map[string]map[string]string // checkout prefix -> repo-relative status
	repoSG    map[string]map[string]bool   // checkout prefix -> repo-relative staged
	repoMode  map[string]string            // checkout prefix -> "head" | "branch"
	repoBase  map[string]string            // checkout prefix -> resolved diff base
```

In the walk loop of `Build`, right after the symlink check, skip `.git` files (linked worktrees) and record repo roots:

```go
			if !isDir && name == ".git" {
				continue // a linked worktree's pointer file, not content
			}
```

and in the `if isDir {` branch before `subdirs = append(...)`:

```go
				if _, err := os.Lstat(filepath.Join(abs, name, ".git")); err == nil {
					mu.Lock()
					found = append(found, checkout{Prefix: childRel, Abs: filepath.Join(abs, name)})
					mu.Unlock()
				}
```

Declare `var found []checkout` beside `files` in the `var (...)` block. After `wg.Wait()`:

```go
	cos := validCheckouts(ix.root, found)
```

and inside the final locked block of `Build`:

```go
	ix.workspace = len(cos) > 0
	ix.checkouts = cos
```

Create `workspace.go`:

```go
package main

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Workspace mode: the opened folder is not a git repo but holds many
// checkouts (repos and linked worktrees). Every git call routes to the
// checkout that owns the path, found by longest prefix.

type checkout struct {
	Prefix string // workspace-relative, slash-separated ("sontaran-x")
	Abs    string // absolute path
}

// validCheckouts keeps the folders git itself accepts as a working tree top
// level, probing in parallel. It returns nil when root is itself inside a repo
// or git is off: workspace mode is only for a plain folder of checkouts.
func validCheckouts(root string, found []checkout) []checkout {
	if gitDisabled || len(found) == 0 || gitAvailable(root) {
		return nil
	}
	ok := make([]bool, len(found))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, c := range found {
		wg.Add(1)
		go func(i int, c checkout) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info := gitProbe(c.Abs)
			ok[i] = info.ok && filepath.Clean(info.toplevel) == filepath.Clean(c.Abs)
		}(i, c)
	}
	wg.Wait()
	var out []checkout
	for i, c := range found {
		if ok[i] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

func (ix *Index) Workspace() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.workspace
}

func (ix *Index) Checkouts() []checkout {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]checkout, len(ix.checkouts))
	copy(out, ix.checkouts)
	return out
}

func (ix *Index) GitOn() bool {
	if gitDisabled {
		return false
	}
	if ix.Workspace() {
		return true
	}
	return gitAvailable(ix.root)
}

// RepoFor returns the checkout that owns rel (longest prefix wins, so a repo
// nested in another owns its own files) and rel relative to that checkout.
func (ix *Index) RepoFor(rel string) (checkout, string, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	best := -1
	for i, c := range ix.checkouts {
		if rel == c.Prefix || strings.HasPrefix(rel, c.Prefix+"/") {
			if best < 0 || len(c.Prefix) > len(ix.checkouts[best].Prefix) {
				best = i
			}
		}
	}
	if best < 0 {
		return checkout{}, "", false
	}
	c := ix.checkouts[best]
	return c, strings.TrimPrefix(strings.TrimPrefix(rel, c.Prefix), "/"), true
}

func (ix *Index) CheckoutByPrefix(p string) (checkout, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, c := range ix.checkouts {
		if c.Prefix == p {
			return c, true
		}
	}
	return checkout{}, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `GOWORK=off go test -count=1 -run 'TestWorkspace' . && GOWORK=off go test -count=1 .`
Expected: PASS, full suite still green.

- [ ] **Step 5: Commit**

```bash
git add workspace.go workspace_test.go index.go
git commit -m "Discover checkouts in workspace mode and route paths to them"
```

---

### Task 2: Merged git status across checkouts

**Files:**
- Modify: `index.go` (`Build` end, `UpdateGitStatus` :608-622), `workspace.go`
- Test: `workspace_test.go`

**Interfaces:**
- Consumes: Task 1 (`Checkouts`, `Workspace`, `GitOn`).
- Produces:
  - `func (ix *Index) UpdateGitStatusFor(prefixes []string) (...same 8 returns as UpdateGitStatus)` — nil means every checkout
  - `func (ix *Index) RepoBase(prefix string) string` — `"HEAD"` unless set
  - `func (ix *Index) CheckoutCounts(prefix string) (changes, staged int)`
  - `func (ix *Index) refreshCheckouts(prefixes []string)` and `func (ix *Index) mergedStatus() (map[string]string, map[string]bool)`

- [ ] **Step 1: Write the failing test**

```go
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
```

Add `"strings"` to the test imports.

- [ ] **Step 2: Run to verify it fails**

Run: `GOWORK=off go test -count=1 -run TestWorkspaceStatusMerged .`
Expected: FAIL — `ix.CheckoutCounts undefined`.

- [ ] **Step 3: Implement**

In `workspace.go`:

```go
// refreshCheckouts re-runs git status for the given checkouts (nil: all) in
// parallel and stores each result under its prefix, repo-relative.
func (ix *Index) refreshCheckouts(prefixes []string) {
	cos := ix.Checkouts()
	if prefixes != nil {
		want := map[string]bool{}
		for _, p := range prefixes {
			want[p] = true
		}
		kept := cos[:0]
		for _, c := range cos {
			if want[c.Prefix] {
				kept = append(kept, c)
			}
		}
		cos = kept
	}
	type result struct {
		gs map[string]string
		sg map[string]bool
	}
	out := make([]result, len(cos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for i, c := range cos {
		wg.Add(1)
		go func(i int, c checkout) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ix.resolveRepoBase(c) // Task 6; until then a no-op returning
			out[i] = result{gitStatusAgainst(c.Abs, ix.RepoBase(c.Prefix)), gitStagedPaths(c.Abs)}
		}(i, c)
	}
	wg.Wait()
	ix.mu.Lock()
	if ix.repoGS == nil {
		ix.repoGS, ix.repoSG = map[string]map[string]string{}, map[string]map[string]bool{}
	}
	for i, c := range cos {
		ix.repoGS[c.Prefix], ix.repoSG[c.Prefix] = out[i].gs, out[i].sg
	}
	ix.mu.Unlock()
}

// mergedStatus prefixes every checkout's status with its folder. A nested
// repo shows up in its parent's porcelain as an untracked "dir/": dropped.
func (ix *Index) mergedStatus() (map[string]string, map[string]bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	gs, sg := map[string]string{}, map[string]bool{}
	for _, c := range ix.checkouts {
		for k, v := range ix.repoGS[c.Prefix] {
			if !strings.HasSuffix(k, "/") {
				gs[c.Prefix+"/"+k] = v
			}
		}
		for k, v := range ix.repoSG[c.Prefix] {
			sg[c.Prefix+"/"+k] = v
		}
	}
	return gs, sg
}

func (ix *Index) RepoBase(prefix string) string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if b := ix.repoBase[prefix]; b != "" {
		return b
	}
	return "HEAD"
}

func (ix *Index) CheckoutCounts(prefix string) (changes, staged int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for k := range ix.repoGS[prefix] {
		if !strings.HasSuffix(k, "/") {
			changes++
		}
	}
	return changes, len(ix.repoSG[prefix])
}

// resolveRepoBase is replaced in Task 6.
func (ix *Index) resolveRepoBase(c checkout) {}
```

Add `"runtime"` to its imports.

In `index.go`, rename the body of `UpdateGitStatus` to `UpdateGitStatusFor(prefixes []string)` and keep:

```go
func (ix *Index) UpdateGitStatus() (int, []string, bool, map[string]string, map[string]bool, map[string]bool, map[string]string, map[string]bool) {
	return ix.UpdateGitStatusFor(nil)
}
```

Replace its opening lines (the guard and the `gs` / `sg` computation) with:

```go
	if !ix.Ready() || gitDisabled || !ix.GitOn() {
		return 0, nil, false, nil, nil, nil, nil, nil
	}
	var gs map[string]string
	var sg map[string]bool
	if ix.Workspace() {
		ix.refreshCheckouts(prefixes)
		gs, sg = ix.mergedStatus()
	} else {
		base := ix.DiffBase()
		gs = gitStatusAgainst(ix.root, base)
		sg = gitStagedPaths(ix.root)
	}
	if gs == nil {
		gs = map[string]string{}
	}
	if sg == nil {
		sg = map[string]bool{}
	}
```

At the very end of `Build`, after the locked block that closes `readyCh`:

```go
	if ix.Workspace() {
		ix.UpdateGitStatus() // badges arrive now that checkouts are known
	}
```

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 -run 'TestWorkspace' . && GOWORK=off go test -count=1 .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add index.go workspace.go workspace_test.go
git commit -m "Merge git status from every checkout in workspace mode"
```

---

### Task 3: Per-file git calls route to the owning checkout

**Files:**
- Modify: `server.go` (`handleMeta` :858, `handleFile` :1169-1252, `handleDiff` :1324, `handleGutter` :1375, `handleGitStage` :1515, `handleGitUnstage` :1535, `handleGitRefresh` :1483)
- Test: `workspace_test.go`

**Interfaces:**
- Consumes: `RepoFor`, `RepoBase`, `GitOn`, `Workspace`.
- Produces: `func (s *Server) gitFor(rel string) (root, repoRel, base string, ok bool)`; `/api/meta` gains `"workspace": bool`, and `"git"` becomes `s.ix.GitOn()`.

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `GOWORK=off go test -count=1 -run TestWorkspaceFileDiffGutterStage .`
Expected: FAIL — `meta: ... git:false`.

- [ ] **Step 3: Implement**

Add to `server.go`:

```go
// gitFor resolves the repo a per-file git call runs in. In a single repo that
// is the root, unchanged. In workspace mode it is the checkout owning rel; a
// path outside every checkout has no git (ok false).
func (s *Server) gitFor(rel string) (root, repoRel, base string, ok bool) {
	if !s.ix.Workspace() {
		return s.ix.Root(), rel, s.diffBase, gitAvailable(s.ix.Root())
	}
	c, rr, found := s.ix.RepoFor(rel)
	if !found {
		return "", "", "", false
	}
	return c.Abs, rr, s.ix.RepoBase(c.Prefix), true
}
```

In `handleMeta`: `"git": s.ix.GitOn(),` and add `"workspace": s.ix.Workspace(),`.

In `handleFile`, both diff-availability blocks become (shown for the normal branch; the deleted-file branch is the same with its `os.IsNotExist(err) &&` prefix):

```go
	diffAvail := false
	if groot, grel, gbase, gok := s.gitFor(rel); gok {
		if ref := q.Get("ref"); ref != "" {
			diffAvail = gitDiffCommit(groot, grel, ref) != ""
		} else if s.pr != nil {
			diffAvail = gitDiffAgainst(groot, grel, gbase) != "" ||
				gitDiffBetween(groot, grel, gbase, s.prHeadSHA) != "" ||
				gitDiffAgainst(groot, grel, s.prHeadSHA) != ""
		} else {
			diffAvail = gitDiffAgainst(groot, grel, gbase) != ""
		}
	}
```

The deleted-file branch's guard `if os.IsNotExist(err) && gitAvailable(s.ix.Root())` becomes `if os.IsNotExist(err)` with the same `gitFor` block inside.

In `handleDiff`, after resolving `rel`:

```go
	groot, grel, gbase, gok := s.gitFor(rel)
	if !gok {
		writeJSON(w, map[string]any{"path": rel, "diff": "", "hunks": highlightDiff(rel, ""), "available": false})
		return
	}
```

then replace `s.ix.Root(), rel` with `groot, grel` and `s.diffBase` with `gbase` in every call of the handler (the `ref` branch, the main diff, and the PR branch).

In `handleGutter`: same guard returning `available:false` with empty arrays, then `gitHunksCommit(groot, grel, ref)` / `gitHunksAgainst(groot, grel, gbase)`.

In `handleGitStage` / `handleGitUnstage`: whole-checkout `"."` is handled in Task 4; for a file path:

```go
	groot, grel, _, gok := s.gitFor(rel)
	if !gok {
		fail(w, http.StatusBadRequest, "not inside a git checkout")
		return
	}
	if err := gitStage(groot, grel); err != nil {
```

(`gitUnstage` likewise.) In `handleGitRefresh` replace `gitAvailable(s.ix.Root())` with `s.ix.GitOn()`.

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 .`
Expected: PASS (new test and full suite).

- [ ] **Step 5: Commit**

```bash
git add server.go workspace_test.go
git commit -m "Route per-file git calls to the owning checkout"
```

---

### Task 4: Whole-repo endpoints take a repo

**Files:**
- Modify: `server.go` (`handleGitStage`/`Unstage` "." case, `handleGitCommit` :1554, `handleGitCommitMessage` :1580, `handleGitPush` :1624, `handleGitPull` :1668, `handleGitLog` :1728, `handleUnpushed` :1754, `handleCommitFiles` :1791, `handleCommitDetail` :1806)
- Test: `workspace_test.go`

**Interfaces:**
- Consumes: `CheckoutByPrefix`, `Workspace`.
- Produces: `func (s *Server) repoRoot(w http.ResponseWriter, r *http.Request) (root, prefix string, ok bool)` — reads `?repo=`; single mode returns `(s.ix.Root(), "", true)` and ignores it; workspace mode requires a known checkout prefix or writes 400.

- [ ] **Step 1: Write the failing test**

```go
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
	sha := commits[0].(map[string]any)["hash"].(string)
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
```

Check `GitCommit`'s JSON field for the hash before running (`grep -n 'type GitCommit struct' -A6 git.go`); use that key.

- [ ] **Step 2: Run to verify it fails**

Run: `GOWORK=off go test -count=1 -run TestWorkspaceWholeRepoEndpoints .`
Expected: FAIL — stage-all without repo returns 200 or 502, not 400.

- [ ] **Step 3: Implement**

```go
// repoRoot names the repo a whole-repo action runs in. A single-repo px0 has
// one; a workspace requires ?repo=<checkout path>, so nothing ever runs in the
// workspace folder itself.
func (s *Server) repoRoot(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if !s.ix.Workspace() {
		return s.ix.Root(), "", true
	}
	c, ok := s.ix.CheckoutByPrefix(strings.Trim(r.URL.Query().Get("repo"), "/"))
	if !ok {
		fail(w, http.StatusBadRequest, "repo is required: name the checkout")
		return "", "", false
	}
	return c.Abs, c.Prefix, true
}
```

- Stage/unstage: before the Task 3 per-file block, `if rel == "." || rel == "" { root, _, ok := s.repoRoot(w, r); if !ok { return }; err := gitStage(root, "."); ... }`. Check `decodeGitPath`: `safePath(".")` may return `rel == ""`; handle both.
- Commit, commit message, push, pull (non-PR branch only), log, unpushed (non-PR branch), commit files, commit detail: replace `root := s.ix.Root()` / `s.ix.Root()` with `root, prefix, ok := s.repoRoot(w, r); if !ok { return }`.
- `handleCommitFiles`: after `files := gitCommitFiles(root, sha)`, when `prefix != ""` prefix each file:

```go
	for i := range files {
		if prefix != "" {
			files[i].Path = prefix + "/" + files[i].Path
			if files[i].From != "" {
				files[i].From = prefix + "/" + files[i].From
			}
		}
	}
```

- `handleDiff`'s `ref` branch already routes through `gitFor` (Task 3), so commit diffs open by prefixed path.

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server.go workspace_test.go
git commit -m "Name the checkout for whole-repo git actions in workspace mode"
```

---

### Task 5: Watcher per checkout, repo states in the payload

**Files:**
- Modify: `git.go` (add `gitCommonDir`), `git_watcher.go`, `workspace.go`, `server.go` (`/api/repos` route + handler)
- Test: `workspace_test.go`

**Interfaces:**
- Consumes: `UpdateGitStatusFor`, `CheckoutCounts`, `Checkouts`.
- Produces:
  - `func gitCommonDir(root string) string` (absolute)
  - `type RepoState struct { Path, Group string; Main bool; Branch, Head string; Ahead, Behind, Changes, Staged int; Mode, Parent string; Agent, InSet bool; Note string }` with JSON tags `path, group, main, branch, head, ahead, behind, changes, staged, mode, parent, agent, inSet, note` (omitempty on strings and bools)
  - `GitStatusPayload` gains `Workspace bool json:"workspace,omitempty"` and `Repos []RepoState json:"repos,omitempty"`
  - `func (gw *GitWatcher) RepoStates() []RepoState`
  - `GET /api/repos` → `{"repos": [...]}`

- [ ] **Step 1: Write the failing test**

```go
func TestWorkspaceRepoStatesAndWatcher(t *testing.T) {
	root := workspaceFixture(t)
	ix := NewIndex(root)
	ix.Build()
	gw := NewGitWatcher(ix)
	gw.Refresh()
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
	// A CLI commit in one checkout is seen through its metadata.
	os.WriteFile(filepath.Join(root, "beta/c.go"), []byte("c\n"), 0o644)
	gitTestRun(t, filepath.Join(root, "beta"), "add", "c.go")
	changed := gw.scanCheckoutMeta()
	if len(changed) != 1 || changed[0] != "beta" {
		t.Fatalf("scanCheckoutMeta = %v, want [beta]", changed)
	}
	gw.refreshFor(changed)
	for _, r := range gw.RepoStates() {
		if r.Path == "beta" && r.Staged != 1 {
			t.Fatalf("beta staged = %d, want 1", r.Staged)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `GOWORK=off go test -count=1 -run TestWorkspaceRepoStatesAndWatcher .`
Expected: FAIL — `RepoState` undefined.

- [ ] **Step 3: Implement**

`git.go`:

```go
// gitCommonDir is the .git directory shared by a repo and all its linked
// worktrees: the same value for every checkout of one repo.
func gitCommonDir(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}
```

`workspace.go`: the `RepoState` type, and

```go
// repoIdentity fills the parts of a RepoState that only change when refs or
// HEAD move: group, branch, head, ahead/behind.
func repoIdentity(wsRoot string, c checkout) RepoState {
	st := RepoState{Path: c.Prefix, Mode: "head"}
	if cd := gitCommonDir(c.Abs); cd != "" {
		main := filepath.Dir(cd)
		if rel, err := filepath.Rel(wsRoot, main); err == nil && !strings.HasPrefix(rel, "..") {
			st.Group = filepath.ToSlash(rel)
		} else {
			st.Group = main
		}
	}
	st.Main = st.Group == c.Prefix
	st.Branch = gitCurrentBranch(c.Abs)
	st.Head = gitHeadCommit(c.Abs)
	st.Ahead, st.Behind, _ = gitAheadBehind(c.Abs)
	return st
}
```

`git_watcher.go`:

- New fields: `metas map[string]checkoutMeta` (`type checkoutMeta struct{ index, head, refs, fetch time.Time; size int64 }`), `states map[string]RepoState`, guarded by `gw.mu`.
- `func (gw *GitWatcher) statCheckout(c checkout) checkoutMeta` stats `gitDir(c.Abs)/index` (mtime and size), `gitDir(c.Abs)/HEAD`, `gitCommonDir/packed-refs` and `gitCommonDir/refs/heads` (dir mtime), `gitCommonDir/FETCH_HEAD`. Cache each checkout's common dir in a `commonDirs map[string]string`.
- `func (gw *GitWatcher) scanCheckoutMeta() []string` compares with `gw.metas`, stores the new stamps and returns the changed prefixes in sorted order. The first scan records stamps and returns nil.
- `func (gw *GitWatcher) refreshFor(prefixes []string) GitStatusPayload`: the old `Refresh` body generalised. It calls `gw.ix.UpdateGitStatusFor(prefixes)`. In workspace mode it recomputes `repoIdentity` in parallel (16 at a time) for `prefixes`, or for all on nil, fills `Changes`/`Staged` from `ix.CheckoutCounts`, sets `payload.Workspace = true` and `payload.Repos = gw.RepoStates()`, leaves `Branch/Ahead/Behind/RecentCommits/CommitsURL` empty, and marks `changed` when any state differs from before. Single mode keeps today's body exactly.
- `Refresh()` becomes `return gw.refreshFor(nil)`.
- `RepoStates()` returns the states sorted by `Group`, then main first, then `Path`.
- `Start`: `if gitDisabled || !gw.ix.GitOn() { return }`. In workspace mode skip `gitDir(gw.root)`, call `gw.scanCheckoutMeta()` once to record, and `go gw.loop(ctx, "")`.
- `loop` meta tick: `if gw.ix.Workspace() { if ch := gw.scanCheckoutMeta(); len(ch) > 0 { gw.refreshFor(ch) } } else if gw.recordGitMeta(gitdir) { gw.checkAndBroadcast() }`.
- The worktree tick stays a full `checkAndBroadcast()`. Identity is cached, so only status reruns for every checkout, as today's ticker does for one repo.
- `Subscribe`: `inGit := gw.ix.GitOn()`.
- `NewGitWatcher`: only call `gitHeadCommit(ix.Root())` when `!ix.Workspace()`.

`server.go`: register `s.mux.HandleFunc(s.routePath("/api/repos"), s.handleRepos)`:

```go
func (s *Server) handleRepos(w http.ResponseWriter, r *http.Request) {
	repos := []RepoState{}
	if s.gitWatcher != nil {
		repos = append(repos, s.gitWatcher.RepoStates()...)
	}
	writeJSON(w, map[string]any{"repos": repos})
}
```

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 .`
Expected: PASS, including the existing `TestGitWatcher*` tests.

- [ ] **Step 5: Commit**

```bash
git add git.go git_watcher.go workspace.go server.go workspace_test.go
git commit -m "Watch every checkout and publish per-checkout repo state"
```

---

### Task 6: "This branch" base

**Files:**
- Modify: `workspace.go` (replace the `resolveRepoBase` stub; add trunk/parent), `server.go` (`POST /api/git/base`), `git_watcher.go` (copy mode/parent into states)
- Test: `workspace_test.go`

**Interfaces:**
- Produces:
  - `func trunkRef(root string) string` — first that resolves: `refs/remotes/upstream/HEAD`, `refs/remotes/origin/HEAD` (symbolic, short form), then `upstream/dev`, `upstream/main`, `origin/dev`, `origin/main`, `origin/master`, `dev`, `main`, `master`
  - `func parentRef(root, branch string) string` — rule 1: `branch.<b>.merge` names a branch that is neither `<b>` nor trunk's branch name, so return `<remote>/<that branch>` when the ref exists; rule 3: `trunkRef(root)`
  - `func (ix *Index) SetRepoMode(prefix, mode string)`; `func (ix *Index) RepoMode(prefix string) (mode, parent string)`
  - `POST /api/git/base?repo=<prefix>&mode=head|branch` → `{"ok":true,"base":<sha|HEAD>,"parent":<ref>}`; 409 when no merge base exists

- [ ] **Step 1: Write the failing test**

```go
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
	if code, m := agentPost(t, s, "/api/git/base?repo=alpha-wt&mode=branch"); code != 200 || m["parent"] == "" {
		t.Fatalf("base: %d %v", code, m)
	}
	ix.UpdateGitStatusFor([]string{"alpha-wt"})
	if ix.GitStatusMap()["alpha-wt/feat.go"] != "A" {
		t.Fatalf("this-branch status: %v", ix.GitStatusMap())
	}
	if _, m := get(t, s, "/api/diff?path=alpha-wt/feat.go"); m["available"] != true {
		t.Fatalf("this-branch diff: %v", m)
	}
	agentPost(t, s, "/api/git/base?repo=alpha-wt&mode=head")
	ix.UpdateGitStatusFor([]string{"alpha-wt"})
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
	if got := parentRef(a, "main"); got != "main" {
		t.Fatalf("trunk fallback = %q, want main", got)
	}
}
```

(With `remote = .`, the parent ref is the local branch itself, so `parentRef` returns `<branch>` for remote `.` and `<remote>/<branch>` otherwise.)

- [ ] **Step 2: Run to verify it fails**

Run: `GOWORK=off go test -count=1 -run 'TestWorkspaceThisBranchBase|TestParentRef' .`
Expected: FAIL — `parentRef` undefined.

- [ ] **Step 3: Implement**

```go
func gitRefExists(root, ref string) bool {
	return exec.Command("git", "-C", root, "rev-parse", "--verify", "-q", ref).Run() == nil
}

func trunkRef(root string) string {
	for _, remote := range []string{"upstream", "origin"} {
		if out, err := exec.Command("git", "-C", root, "symbolic-ref", "-q", "--short", "refs/remotes/"+remote+"/HEAD").Output(); err == nil {
			if r := strings.TrimSpace(string(out)); r != "" {
				return r
			}
		}
	}
	for _, r := range []string{"upstream/dev", "upstream/main", "origin/dev", "origin/main", "origin/master", "dev", "main", "master"} {
		if gitRefExists(root, r) {
			return r
		}
	}
	return ""
}

// parentRef is the branch a stack layer is measured against: the feature
// branch it tracks (rule 1), else trunk (rule 3). The nearest-ancestor rule
// is deferred.
func parentRef(root, branch string) string {
	trunk := trunkRef(root)
	trunkName := trunk[strings.LastIndexByte(trunk, '/')+1:]
	cfg := func(key string) string {
		out, _ := exec.Command("git", "-C", root, "config", "--get", key).Output()
		return strings.TrimSpace(string(out))
	}
	if merge := strings.TrimPrefix(cfg("branch."+branch+".merge"), "refs/heads/"); merge != "" && merge != branch && merge != trunkName {
		remote := cfg("branch." + branch + ".remote")
		ref := merge
		if remote != "" && remote != "." {
			ref = remote + "/" + merge
		}
		if gitRefExists(root, ref) {
			return ref
		}
	}
	return trunk
}
```

The real `resolveRepoBase(c)`: when `RepoMode(c.Prefix)` is `"branch"`, compute `parent := parentRef(c.Abs, gitCurrentBranch(c.Abs))` and `mb := gitMergeBase(c.Abs, parent, "HEAD")`, then store `repoBase[c.Prefix] = mb` and `repoParent[c.Prefix] = parent` (add `repoParent map[string]string` to Index). Otherwise delete both. This runs on every refresh, so the base follows HEAD as commits land.

`SetRepoMode` stores `repoMode[prefix]`. `RepoMode` returns `(mode or "head", repoParent[prefix])`.

`server.go` handler:

```go
func (s *Server) handleGitBase(w http.ResponseWriter, r *http.Request) {
	if !localPost(w, r) {
		return
	}
	_, prefix, ok := s.repoRoot(w, r)
	if !ok {
		return
	}
	if !s.ix.Workspace() {
		fail(w, http.StatusBadRequest, "only in workspace mode")
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode != "head" && mode != "branch" {
		fail(w, http.StatusBadRequest, "mode is head or branch")
		return
	}
	s.ix.SetRepoMode(prefix, mode)
	c, _ := s.ix.CheckoutByPrefix(prefix)
	s.ix.resolveRepoBase(c)
	if mode == "branch" && s.ix.RepoBase(prefix) == "HEAD" {
		s.ix.SetRepoMode(prefix, "head")
		fail(w, http.StatusConflict, "no common ancestor with the parent branch")
		return
	}
	if s.gitWatcher != nil {
		s.gitWatcher.refreshFor([]string{prefix})
	}
	_, parent := s.ix.RepoMode(prefix)
	writeJSON(w, map[string]any{"ok": true, "base": s.ix.RepoBase(prefix), "parent": parent})
}
```

Register `/api/git/base`. In `refreshFor`, set `Mode`/`Parent` from `ix.RepoMode` on every state.

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add workspace.go server.go git_watcher.go workspace_test.go
git commit -m "Add a per-checkout This branch base measured from the parent branch"
```

---

### Task 7: Working set file, CLI and agent detection

**Files:**
- Create: `workingset.go`
- Modify: `main.go` (dispatch before target handling, around :86), `git_watcher.go` (poll set file every 1 s, agents every 5 s while clients connected; fill `Agent`/`InSet`/`Note`)
- Test: `workspace_test.go`

**Interfaces:**
- Produces:
  - `type setItem struct { Path, Note, Added string }` (json `path`, `note`, `added`); `type workingSet struct { Items []setItem }` (json `items`)
  - `func setFilePath() string` — same directory rule as `settingsPath()`, file `set.json`
  - `func readSet() workingSet`, `func writeSet(ws workingSet) error` (temp file + rename)
  - `func runSetCmd(args []string, out io.Writer) error` — `add|remove|list|clear`
  - `func agentCheckouts(cos []checkout) map[string]bool` — prefixes with a running agent
  - `func agentNameFromCmdline(cmdline []byte) string` (pure, tested)

- [ ] **Step 1: Write the failing tests**

```go
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
	// Adding again updates the note, no duplicate.
	runSetCmd([]string{"add", filepath.Join(root, "alpha"), "-note", "v2"}, &buf)
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
		"/home/u/.local/bin/claude\x00-p\x00hi\x00": "claude",
		"node\x00/usr/lib/node_modules/@openai/codex/bin/codex.js\x00": "codex",
		"codex\x00exec\x00": "codex",
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
}
```

Add `"bytes"` and `"io"` to the test imports.

- [ ] **Step 2: Run to verify they fail**

Run: `GOWORK=off go test -count=1 -run 'TestSetCommand|TestAgentName|TestRepoStatesCarry' .`
Expected: FAIL — `runSetCmd` undefined.

- [ ] **Step 3: Implement**

`workingset.go`:

```go
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The working set: checkouts an agent or the user said matter now. Stored with
// px0's other per-user files, never in a workspace, keyed by absolute path so
// any px0 serving a folder that contains the checkout shows it.

type setItem struct {
	Path  string `json:"path"`
	Note  string `json:"note,omitempty"`
	Added string `json:"added"`
}

type workingSet struct {
	Items []setItem `json:"items"`
}

func setFilePath() string {
	p := settingsPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "set.json")
}

func readSet() workingSet {
	var ws workingSet
	if b, err := os.ReadFile(setFilePath()); err == nil {
		json.Unmarshal(b, &ws)
	}
	return ws
}

func writeSet(ws workingSet) error {
	p := setFilePath()
	if p == "" {
		return errors.New("no home directory for the working set")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(ws, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// checkoutTop resolves any path inside a checkout to the checkout's top level.
func checkoutTop(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git checkout", abs)
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

func runSetCmd(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: px0 set add|remove|list|clear [path] [-note text]")
	}
	fs := flag.NewFlagSet("set "+args[0], flag.ContinueOnError)
	note := fs.String("note", "", "what the checkout is for")
	fs.SetOutput(out)
	// Accept the path before or after -note.
	rest := args[1:]
	var pos []string
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	target := "."
	if len(pos) > 0 {
		target = pos[0]
	}
	ws := readSet()
	switch args[0] {
	case "add":
		top, err := checkoutTop(target)
		if err != nil {
			return err
		}
		for i := range ws.Items {
			if ws.Items[i].Path == top {
				ws.Items[i].Note = *note
				return writeSet(ws)
			}
		}
		ws.Items = append(ws.Items, setItem{Path: top, Note: *note, Added: time.Now().UTC().Format(time.RFC3339)})
		return writeSet(ws)
	case "remove":
		top, err := checkoutTop(target)
		if err != nil {
			top, _ = filepath.Abs(target) // a deleted worktree can still be removed
		}
		kept := ws.Items[:0]
		for _, it := range ws.Items {
			if it.Path != top {
				kept = append(kept, it)
			}
		}
		ws.Items = kept
		return writeSet(ws)
	case "list":
		for _, it := range ws.Items {
			fmt.Fprintf(out, "%s\t%s\n", it.Path, it.Note)
		}
		return nil
	case "clear":
		return writeSet(workingSet{})
	}
	return fmt.Errorf("unknown set command %q", args[0])
}

var agentNames = map[string]bool{"claude": true, "codex": true, "gemini": true, "cursor-agent": true, "opencode": true, "aider": true, "goose": true, "agy": true}

// agentNameFromCmdline names the coding agent a process is, from its argv:
// the program itself, or the script a node/python/bun interpreter runs.
func agentNameFromCmdline(cmdline []byte) string {
	argv := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	name := func(s string) string {
		b := filepath.Base(s)
		return strings.TrimSuffix(strings.TrimSuffix(b, ".js"), ".mjs")
	}
	if len(argv) == 0 {
		return ""
	}
	if n := name(argv[0]); agentNames[n] {
		return n
	}
	switch name(argv[0]) {
	case "node", "bun", "python", "python3", "deno":
		if len(argv) > 1 {
			if n := name(argv[1]); agentNames[n] {
				return n
			}
		}
	}
	return ""
}

// agentCheckouts returns the checkouts a coding agent process is working in,
// by each process's cwd. Linux only (/proc); elsewhere nothing is found.
func agentCheckouts(root string, cos []checkout) map[string]bool {
	found := map[string]bool{}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return found
	}
	for _, e := range ents {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || agentNameFromCmdline(cmd) == "" {
			continue
		}
		cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd"))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, cwd)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		best := ""
		for _, c := range cos {
			if (rel == c.Prefix || strings.HasPrefix(rel, c.Prefix+"/")) && len(c.Prefix) > len(best) {
				best = c.Prefix
			}
		}
		if best != "" {
			found[best] = true
		}
	}
	return found
}
```

`main.go`, right after the `doUpdate` block:

```go
	if flag.NArg() > 0 && flag.Arg(0) == "set" {
		if err := runSetCmd(flag.Args()[1:], os.Stdout); err != nil {
			fatal(err)
		}
		return
	}
```

Add `px0 set add|remove|list|clear [path] [-note text]` to `flag.Usage`.

`git_watcher.go`: fields `setMod time.Time`, `agents map[string]bool`. In `refreshFor`, for each state: `InSet`/`Note` come from `readSet()`, matching `filepath.Join(gw.root, state.Path) == item.Path`, and `Agent = gw.agents[state.Path]`. In `loop`, on the meta tick, stat `setFilePath()`; when its mtime changes, rebuild the states' set fields and broadcast. Add an `agentTicker` every 5 s that, while `active > 0` and in workspace mode, runs `agentCheckouts` and broadcasts when the map differs. Factor the "rebuild set/agent fields + broadcast if changed" step into `gw.overlayStates() bool`, used by `refreshFor` and both tickers.

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 .`
Expected: PASS.

- [ ] **Step 5: Manual check**

Run: `GOWORK=off go build -o /tmp/claude-1000/-home-lucky11-workspace-zenact/999a6e31-802e-4943-9ccd-ccf4fe43256b/scratchpad/px0-dev . && XDG_CONFIG_HOME=/tmp/claude-1000/-home-lucky11-workspace-zenact/999a6e31-802e-4943-9ccd-ccf4fe43256b/scratchpad/xdg /tmp/claude-1000/-home-lucky11-workspace-zenact/999a6e31-802e-4943-9ccd-ccf4fe43256b/scratchpad/px0-dev set add ~/workspace/zenact/sontaran -note test && XDG_CONFIG_HOME=... px0-dev set list`
Expected: one line `.../sontaran<TAB>test`.

- [ ] **Step 6: Commit**

```bash
git add workingset.go main.go git_watcher.go workspace_test.go
git commit -m "Add the agent-controlled working set and running-agent detection"
```

---

### Task 8: Scope for find and search

**Files:**
- Modify: `workspace.go` (`scopeFiles`), `server.go` (`handleFind` :1134, `handleSearch` :1820), `search.go` (`SearchOpts.Scope`, file selection :309)
- Test: `workspace_test.go`

**Interfaces:**
- Produces: `func scopeFiles(files []FileEntry, scope string) []FileEntry` (subslice of the sorted list, no copy); `?scope=<prefix>` on `/api/find` and `/api/search`.

- [ ] **Step 1: Write the failing test**

```go
func TestScopedFindAndSearch(t *testing.T) {
	root := workspaceFixture(t)
	ix := NewIndex(root)
	ix.Build()
	s := NewServer(ix, nil)
	_, m := get(t, s, "/api/find?q=go&scope=beta")
	for _, r := range m["results"].([]any) {
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
}
```

Check the JSON field names of `FuzzyResult` and `FileMatches` (`grep -n 'type FuzzyResult\|type FileMatches' -A4 *.go`) and adjust `"path"` if they differ.

- [ ] **Step 2: Run to verify it fails**

Run: `GOWORK=off go test -count=1 -run TestScopedFindAndSearch .`
Expected: FAIL — results include `alpha/` paths.

- [ ] **Step 3: Implement**

```go
// scopeFiles narrows the sorted file list to one checkout without copying.
func scopeFiles(files []FileEntry, scope string) []FileEntry {
	scope = strings.Trim(scope, "/")
	if scope == "" {
		return files
	}
	p := scope + "/"
	lo := sort.Search(len(files), func(i int) bool { return files[i].Path >= p })
	hi := sort.Search(len(files), func(i int) bool { return files[i].Path >= scope+"0" }) // '0' sorts right after '/'
	return files[lo:hi]
}
```

`handleFind`: `res := FuzzyFind(scopeFiles(s.ix.Files(), r.URL.Query().Get("scope")), q, limit)`.
`SearchOpts` gains `Scope string`; `handleSearch` sets `Scope: q.Get("scope")`; in `SearchContext`, `files := scopeFiles(ix.Files(), o.Scope)`.

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add workspace.go server.go search.go workspace_test.go
git commit -m "Scope find and search to one checkout"
```

---

### Task 9: Coding harness runs in the owning checkout

**Files:**
- Modify: `agent.go` (`Start` :445, `StartBatch` :456, `run` :642-680, `Tracked` :561/:620), `thread.go` (thread struct, `Create` :295, `runTurn` :538-613, `threadPrompt` path), `server.go` (`SetAgent` wiring)
- Test: `workspace_test.go`

**Interfaces:**
- Consumes: `RepoFor`, `CheckoutByPrefix`.
- Produces:
  - `agentManager.dirFor func(rel string) (dir, repoRel, prefix string)`, set by the server. When nil (single repo), it returns `(m.root, rel, "")`.
  - `thread.Dir string` (json `dir,omitempty`): the checkout prefix fixed at creation, so resumed harness sessions keep their cwd.
  - `func prefixPaths(prefix string, paths []string) []string`

- [ ] **Step 1: Write the failing test**

Mirror the existing fake-harness tests in `agent_test.go` (find the helper that installs a fake harness script: `grep -n 'fakeHarness\|writeHarness\|#!/bin/sh' agent_test.go`). The fake harness records `pwd` into a file and edits the file it is told about:

```go
func TestWorkspaceAgentRunsInCheckout(t *testing.T) {
	root := workspaceFixture(t)
	// <use agent_test.go's fake harness helper here: a script that runs
	//  `pwd > "$PWDLOG"` and appends a line to sub/s.go>
	ix := NewIndex(root)
	ix.Build()
	s := NewServer(ix, nil)
	// <attach the agent the way agent_test.go does, with the fake harness pinned>
	code, job := agentPostJSON(t, s, "/api/agent/edit", map[string]any{"path": "alpha/sub/s.go", "l1": 1, "l2": 1, "instruction": "x"})
	if code != 200 {
		t.Fatalf("edit: %d %v", code, job)
	}
	// <poll /api/agent/job until not running, as agent_test.go does>
	// pwd must be the checkout, and changed paths come back workspace-relative:
	// want PWDLOG == filepath.Join(root, "alpha") and job.changed contains "alpha/sub/s.go"
}
```

Before writing this step's real code, read `agent_test.go`'s existing edit test end to end and copy its harness, attach and poll code verbatim. The three placeholders above name exactly which parts to copy.

- [ ] **Step 2: Run to verify it fails**

Run: `GOWORK=off go test -count=1 -run TestWorkspaceAgentRunsInCheckout .`
Expected: FAIL — the harness pwd is the workspace root.

- [ ] **Step 3: Implement**

In `agent.go`, `Start(abs, rel, ...)`: `dir, repoRel, prefix := m.target(rel)` where

```go
func (m *agentManager) target(rel string) (string, string, string) {
	if m.dirFor != nil {
		if d, rr, p := m.dirFor(rel); d != "" {
			return d, rr, p
		}
	}
	return m.root, rel, ""
}
```

Pass `repoRel` into `agentPrompt`. Store `dir` and `prefix` on the job (unexported fields `dir`, `prefix`). In `run`: `Dir: job.dir`, `worktreeSnapshot(job.dir)`, `changed := prefixPaths(job.prefix, changedSince(job.dir, before))`. `Tracked: gitAvailable(job.dir)`. Batches use the first item's checkout; items in other checkouts keep workspace-relative paths in the prompt (`@alpha/...`) with `dir` set to the workspace root, which is today's behaviour. `StartPrompt` (commit message) takes an optional dir: the commit-message handler passes the `repoRoot` from Task 4.

```go
func prefixPaths(prefix string, paths []string) []string {
	if prefix == "" {
		return paths
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = prefix + "/" + p
	}
	return out
}
```

In `thread.go`: on `Create`, when the server's `dirFor` resolves the anchor path (or, for a workspace thread, the `scope` sent by the client, as `?scope=` on `/api/threads/create`), store `t.Dir = prefix` and `t.Path` relative to the checkout. In `runTurn`, `dir := tm.root; if t.Dir != "" { dir = filepath.Join(tm.root, t.Dir) }`, used for `Dir:`, `createCursorChat`, `worktreeSnapshot` and `changedSince`. Changed paths go through `prefixPaths(t.Dir, ...)`. The thread chip that opens the anchor uses `t.Dir + "/" + t.Path`. Expose a `"anchor"` field in the thread JSON with the workspace-relative path, and use it in `web/src/thread.js` where the anchor link opens a file (`grep -n 'path' web/src/thread.js | grep -i open`).

`server.go` `SetAgent`: after `s.agent = a`, set `a.dirFor = func(rel string) (string, string, string) { c, rr, ok := s.ix.RepoFor(rel); if !ok || !s.ix.Workspace() { return "", "", "" }; return c.Abs, rr, c.Prefix }`. Do the same for the thread manager.

- [ ] **Step 4: Run tests**

Run: `GOWORK=off go test -count=1 .`
Expected: PASS, including every existing `agent_test.go` / `thread_test.go` test.

- [ ] **Step 5: Commit**

```bash
git add agent.go thread.go server.go web/src/thread.js workspace_test.go
git commit -m "Run the coding harness in the checkout that owns the file"
```

---

### Task 10: Explorer — grouped root, working set view, scope

**Files:**
- Create: `web/src/workspace.js`
- Modify: `web/index.html` (sidebar toggle :22 gets a third button `#btn-set`; scope bar after `#root-name`), `web/src/tree.js` (`drawTree` root, `refreshTree`, `setSidebarMode`, tree menu items), `web/src/gitstream.js` (`handleGitStatus` → `onRepos`), `web/src/palette.js` (:162 find `scope`), `web/src/search.js` (:39 `scope`), `web/src/main.js` (:97 init scope from URL), `web/style.css`

**Interfaces:**
- Consumes: payload `workspace`, `repos[]`; `/api/repos`; `?scope=` on find/search.
- Produces (exports of `workspace.js`):
  - `isWorkspace()` → `!!S.meta?.workspace`
  - `repoOf(path)` → longest checkout prefix among `S.repos` or `''`
  - `activeRepo()` → `S.scope || repoOf(currentDoc()?.path || '')`
  - `repoQuery()` → `{ repo: activeRepo() }` in workspace mode, else `{}`
  - `setScope(prefix)` / `S.scope` (persisted as `?scope=` in `location`)
  - `onRepos(repos)` — store `S.repos`, re-render the root when the signature changes
  - `drawWorkspaceRoot(container)` — the grouped root for the current view
  - `S.wsView`: `'set' | 'changes' | 'all'` (default `'set'`, remembered in `localStorage` under `px0.wsView`)

- [ ] **Step 1: Write `workspace.js`**

```js
// web/src/workspace.js
// Workspace mode: the opened folder holds many checkouts. The explorer root
// lists checkouts grouped by repo instead of raw folders, filtered by view:
// the working set (agent running or added via `px0 set add`), changes, or all.
import { $, S, esc, api } from './state.js';

S.repos = [];
S.scope = new URLSearchParams(location.search).get('scope') || '';
S.wsView = (() => { try { return localStorage.getItem('px0.wsView') || 'set'; } catch { return 'set'; } })();

export const isWorkspace = () => !!S.meta?.workspace;

export function repoOf(path) {
  let best = '';
  for (const r of S.repos) {
    if ((path === r.path || path.startsWith(r.path + '/')) && r.path.length > best.length) best = r.path;
  }
  return best;
}

export function activeRepo() {
  const d = S.tabs[S.active];
  return S.scope || repoOf(d?.path || '');
}

export const repoQuery = () => (isWorkspace() ? { repo: activeRepo() } : {});

let sig = '';
let redraw = null; // tree.js hands its refresh in, to avoid an import cycle
export function setRedraw(fn) { redraw = fn; }

export function onRepos(repos) {
  if (!repos) return;
  S.repos = repos;
  const next = JSON.stringify(repos.map(r => [r.path, r.branch, r.changes, r.ahead, r.agent, r.inSet, r.note, r.mode]));
  if (next !== sig) {
    sig = next;
    if (!S.scope) redraw?.();
    renderScopeBar();
  }
}

export function setView(v) {
  S.wsView = v;
  try { localStorage.setItem('px0.wsView', v); } catch {}
  redraw?.();
}

export function setScope(prefix) {
  S.scope = prefix || '';
  const u = new URL(location.href);
  if (S.scope) u.searchParams.set('scope', S.scope); else u.searchParams.delete('scope');
  history.replaceState(null, '', u);
  renderScopeBar();
  redraw?.();
}

function visible(r) {
  const busy = r.changes > 0 || r.ahead > 0;
  if (S.wsView === 'all') return true;
  if (S.wsView === 'changes') return busy;
  const anySet = S.repos.some(x => x.agent || x.inSet);
  return anySet ? (r.agent || r.inSet) : busy;
}

function label(r) {
  const group = r.main ? '' : r.group;
  // Under its group, a worktree drops the repeated group prefix.
  let name = r.path;
  if (group && name.startsWith(group + '-')) name = name.slice(group.length + 1);
  return name;
}

export function checkoutRow(r, depth) {
  const pad = 8 + depth * 12;
  const ref = r.branch && r.branch !== 'HEAD' ? r.branch : (r.head ? '@' + r.head : '');
  const marks =
    (r.agent ? '<span class="ws-agent" title="A coding agent is working here">agent</span>' : '') +
    (r.changes ? '<span class="ws-n" title="Uncommitted changes">' + r.changes + '</span>' : '') +
    (r.ahead ? '<span class="ws-up" title="Commits not pushed">↑' + r.ahead + '</span>' : '');
  const dirty = r.changes ? ' dirty' : '';
  return '<div class="tw"><div class="tr dir ws-co' + dirty + '" data-dir="' + esc(r.path) + '" data-co="' + esc(r.path) + '" style="padding-left:' + pad + 'px" title="' + esc(r.path) + (r.note ? ' — ' + esc(r.note) : '') + '">' +
    '<span class="ar"></span><span class="nm">' + esc(label(r)) + '</span>' +
    '<span class="ws-br">' + esc(ref) + '</span>' +
    (r.note ? '<span class="ws-note">' + esc(r.note) + '</span>' : '') + marks +
    '<button class="ws-focus" data-focus="' + esc(r.path) + '" title="Scope px0 to this checkout">⌖</button></div>' +
    '<div class="kids" data-kids="' + esc(r.path) + '"></div></div>';
}

export async function drawWorkspaceRoot(container) {
  const shown = S.repos.filter(visible);
  const groups = new Map();
  for (const r of shown) {
    const g = r.main ? r.path : r.group;
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(r);
  }
  let html = '';
  for (const [g, list] of [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    list.sort((a, b) => (b.main - a.main) || a.path.localeCompare(b.path));
    const main = list.find(r => r.path === g);
    if (main) html += checkoutRow(main, 0);
    else html += '<div class="ws-group" title="' + esc(g) + '">' + esc(g.split('/').pop()) + '</div>';
    for (const r of list) if (r !== main) html += checkoutRow(r, 1);
  }
  if (S.wsView === 'all') {
    let j;
    try { j = await api('/api/tree', { dir: '' }); } catch { j = { children: [] }; }
    const inCheckout = p => S.repos.some(r => r.path === p);
    const others = j.children.filter(c => c.dir && !inCheckout(c.path));
    if (others.length) {
      html += '<div class="ws-group">Other folders</div>' + others.map(c =>
        '<div class="tw"><div class="tr dir" data-dir="' + esc(c.path) + '" style="padding-left:20px"><span class="ar"></span><span class="nm">' + esc(c.name) + '</span></div><div class="kids" data-kids="' + esc(c.path) + '"></div></div>').join('');
    }
  }
  if (!html) {
    html = '<div class="ws-empty">' + (S.wsView === 'set'
      ? 'No agent is working and the set is empty. Add a checkout with <code>px0 set add</code>.'
      : 'No checkouts with changes.') + '</div>';
  }
  container.innerHTML = html;
}

export function renderScopeBar() {
  const el = $('#ws-scope');
  if (!el) return;
  el.hidden = !isWorkspace() || !S.scope;
  if (!S.scope) return;
  const r = S.repos.find(x => x.path === S.scope);
  el.innerHTML = '<span class="ws-scope-name">' + esc(S.scope) + '</span>' +
    (r?.branch ? '<span class="ws-br">' + esc(r.branch) + '</span>' : '') +
    '<button id="ws-unscope" title="Back to all checkouts">✕</button>';
}
```

- [ ] **Step 2: Wire it**

- `web/index.html`: inside `.sidebar-toggle`, before `#btn-files`, add `<button id="btn-set" class="toggle-btn" title="Working set: checkouts an agent is in or that were added with px0 set add" aria-label="Working set" hidden>…</button>` with a small person/bolt SVG in the same style as its siblings. After the `<header class="panel-head">` line add `<div id="ws-scope" class="ws-scope" hidden></div>`.
- `tree.js` `refreshTree` (:127): when `isWorkspace() && !S.scope`, call `drawWorkspaceRoot(treeEl)` instead of `drawTree('', treeEl, 0)`. When `S.scope` is set, call `drawTree(S.scope, treeEl, 0)`. Call `setRedraw(refreshTree)` in `initTree`.
- `tree.js` `setSidebarMode(mode)`: in workspace mode the three buttons map to `S.wsView`: `#btn-set` → `'set'`, `#btn-changed` → `'changes'` (also adds `changed-only`, so clean files inside a checkout stay hidden, exactly as today), `#btn-files` → `'all'`. Show `#btn-set` only in workspace mode. Call `setView(...)` then `refreshTree()`.
- `tree.js` click handler in `initTree`: `.ws-focus` click → `setScope(btn.dataset.focus)`, and stop propagation so the row does not toggle.
- `#ws-unscope` click (delegated on `#ws-scope`) → `setScope('')`.
- `gitstream.js` `handleGitStatus`: first line `if (data.workspace) onRepos(data.repos);`.
- `main.js` after meta loads (:97): `if (S.meta.workspace) { onRepos((await api('/api/repos')).repos); }`.
- `palette.js:162`: `api('/api/find', { q, limit: 120, scope: S.scope })`. `search.js:39`: add `scope: S.scope` to `params`. The `request` helper drops empty values, so single-repo calls are unchanged.
- `style.css`, using the existing variables only: `.ws-br` muted monospace 11px, ellipsis, `margin-left:6px`; `.ws-note` muted italic, ellipsis; `.ws-n`, `.ws-up` like the existing `.gs` badge; `.ws-agent` a small pill with the accent colour; `.ws-group` an 11px uppercase-free muted label, `padding:4px 8px 2px`; `.ws-focus` hidden until row hover; `.ws-scope` a one-line bar under the header; `.ws-empty` muted 12px padded.

- [ ] **Step 3: Check and build**

Run: `make web-check && node scripts/build-web.js && GOWORK=off go build -o /tmp/claude-1000/-home-lucky11-workspace-zenact/999a6e31-802e-4943-9ccd-ccf4fe43256b/scratchpad/px0-dev .`
Expected: web-check passes, build succeeds.

- [ ] **Step 4: Manual verification in the browser pane**

Run the dev binary on `~/workspace/zenact` on port 7790 (`-no-open -no-telemetry -no-update`). Open `http://127.0.0.1:7790`, then:
- the Working set view lists the checkouts where an agent runs (this session's own `claude` process counts);
- `px0-dev set add ~/workspace/zenact/sontaran -note test` makes `sontaran` appear within about a second, with its note;
- Changes lists the dirty checkouts, and expanding one shows only changed files;
- All shows the groups and Other folders;
- ⌖ scopes the tree, and Ctrl+P / Ctrl+Shift+F then return only that checkout's files;
- ✕ unscopes.

Then `px0-dev set clear`.

- [ ] **Step 5: Commit**

```bash
git add web/src/workspace.js web/src/tree.js web/src/gitstream.js web/src/palette.js web/src/search.js web/src/main.js web/index.html web/style.css
git commit -m "Explorer: checkouts grouped by repo, working set view, scope to a checkout"
```

---

### Task 11: Git panel and commit lists follow the active checkout

**Files:**
- Modify: `web/src/gitpanel.js` (`updateGitPanel` :99, `stagePath` :170, `doStageAll`, `unstagePath`, `doCommit`, push :272, pull :302, commit-with-AI :334-355/:415, `/api/git/log` :483/:523), `web/src/unpushed.js` (:54-89, :127, :161), `web/src/commitfiles.js` (:15-23), `web/src/tabs.js` (tab switch hook)

**Interfaces:**
- Consumes: `isWorkspace`, `activeRepo`, `repoQuery` from `workspace.js`.

- [ ] **Step 1: Implement**

- Every whole-repo request adds `repoQuery()`. For `apiPost`/`api` calls, merge it into params: `apiPost('/api/git/push', { ...repoQuery() })`. For `apiPostJson` calls, put it on the path: `apiPostJson('/api/git/commit' + qs(repoQuery()), { message })`, with `const qs = o => (o.repo ? '?repo=' + encodeURIComponent(o.repo) : '');` local to `gitpanel.js`. Stage-all posts `{ path: '.' }` to `'/api/git/stage' + qs(repoQuery())`.
- `updateGitPanel(payload)`: when `payload?.workspace`, let `st = (payload.repos || []).find(r => r.path === activeRepo())`. If there is no `st`, show `#git-branch` as "Open a file in a checkout" and hide the commit section. Otherwise build a view payload `{ branch: st.branch, ahead: st.ahead, behind: st.behind, gitChanges: st.changes, staged: <count of payload.staged keys under st.path + '/'> }` and run the existing body with it. Prefix the branch text with `st.path + ' · '`. Recent commits come from `fetchRecentCommits()`, which now passes `repoQuery()`.
- On tab switch (wherever `tabs.js` sets `S.active` at :151 and emits; reuse that event) and on `setScope`, call `updateGitPanel(lastPayload)` and `fetchRecentCommits()`. Keep `lastPayload` in `gitstream.js` and export a getter.
- `unpushed.js`: requests pass `repoQuery()`. The signature becomes `activeRepo() + ':' + (st.head || '')` in workspace mode.
- `commitfiles.js`: the cache key becomes `(activeRepo() || '') + ':' + sha`, and requests pass `repoQuery()`. Returned paths are already prefixed (Task 4), so `openFile(path, {ref})` works unchanged.

- [ ] **Step 2: Check, build, verify**

Run: `make web-check && node scripts/build-web.js && GOWORK=off go build -o .../px0-dev .`

Manual: on a scratch checkout created for the test (`git -C ~/workspace/zenact/px0 worktree add /home/lucky11/workspace/zenact/px0-wt-scratch -b scratch/ws-test`):
- edit a file, open it in the dev px0, and see the git panel show `px0-wt-scratch · scratch/ws-test`;
- stage it with the tick, commit with a message, and check `git -C px0-wt-scratch log -1`;
- see the recent commits list update, and the commit's files open as diffs.

Clean up: `git -C ~/workspace/zenact/px0 worktree remove px0-wt-scratch && git -C ~/workspace/zenact/px0 branch -D scratch/ws-test`.

- [ ] **Step 3: Commit**

```bash
git add web/src/gitpanel.js web/src/unpushed.js web/src/commitfiles.js web/src/tabs.js web/src/gitstream.js
git commit -m "Git panel acts on the active checkout in workspace mode"
```

---

### Task 12: Diff menu — Uncommitted / This branch

**Files:**
- Modify: `web/index.html` (`#diff-menu` :167-184), `web/src/diff.js` (menu handling near `initDiff` :528), `web/src/status.js` (:45-60 tooltip)

- [ ] **Step 1: Implement**

- In `#diff-menu`, after the Unified item, add a second `.diff-menu-header` "Compare against" and two `.diff-menu-item`s in the existing markup shape: `data-diff-base="head"` ("Uncommitted", "Working tree against HEAD") and `data-diff-base="branch"` ("This branch", "Against its parent branch"). Both are `hidden` unless `S.meta.workspace`.
- `diff.js`: on menu open, set the check on the item matching `S.repos.find(r => r.path === repoOf(doc.path))?.mode || 'head'`, and set the "This branch" description to `'Against ' + (st.parent || 'its parent branch')`. On click: `await apiPost('/api/git/base', { repo: repoOf(doc.path), mode })`, then `triggerRefresh()`, then re-run `setDiffMode(d.diffMode || layoutPref())` to refetch. On 409, `showToast('!', e.message)`.
- `status.js`: the diff-switch tooltip says `against HEAD` or `against <parent>` from the same state.

- [ ] **Step 2: Check, build, verify**

Run: `make web-check && node scripts/build-web.js && GOWORK=off go build -o .../px0-dev .`

Manual, with the scratch worktree from Task 11 (re-create it, commit one change on its branch):
- "This branch" shows the committed file as changed and its diff opens;
- "Uncommitted" hides it again.

Clean up the worktree and branch.

- [ ] **Step 3: Commit**

```bash
git add web/index.html web/src/diff.js web/src/status.js
git commit -m "Diff menu: compare a checkout against HEAD or its parent branch"
```

---

### Task 13: Performance check and install

**Files:** none (measurement), `README.md` (one section)

- [ ] **Step 1: Measure on the real workspace**

Run the dev binary on `~/workspace/zenact` (port 7790). Then:
- `curl -s localhost:7790/api/meta`: `indexMs`, `rssBytes`;
- `time curl -s -X POST -H 'Origin: http://127.0.0.1:7790' localhost:7790/api/git/refresh -o /dev/null` for a full status;
- touch a file in one checkout, `git add` it, and time until `/api/repos` shows the new staged count, polling every 100 ms.

Targets (spec): first status for all checkouts < 3 s; single-checkout refresh < 300 ms. Record the numbers in the final report. If the first status misses the target, lower `refreshCheckouts` parallelism contention by raising its semaphore to `runtime.NumCPU()*2` and re-measure once. If it still misses, report the numbers instead of tuning further.

- [ ] **Step 2: Document**

Add a "Workspace mode" section to `README.md`: what turns it on, the three explorer views, ⌖ scope, Compare against, and the `px0 set` commands with one example (`px0 set add . -note "what this checkout is for"`).

- [ ] **Step 3: Full verification**

Run: `make check && GOWORK=off go test -count=1 .`
Expected: all pass.

- [ ] **Step 4: Install for daily use**

Run: `make build && install -m 755 px0 ~/.local/bin/px0-ws`. Installed as `px0-ws`, so the upstream `px0` stays untouched and `px0 --update` cannot overwrite the fork.

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "Document workspace mode"
```
