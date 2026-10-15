package main

import (
	"path/filepath"
	"runtime"
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

// Workspace reports whether the index serves a folder of checkouts.
func (ix *Index) Workspace() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.workspace
}

// Checkouts returns the discovered checkouts, sorted by prefix.
func (ix *Index) Checkouts() []checkout {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]checkout, len(ix.checkouts))
	copy(out, ix.checkouts)
	return out
}

// GitOn reports whether git features are available: the served root is a
// repo, or (workspace mode) it holds at least one checkout.
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

// CheckoutByPrefix looks a checkout up by its workspace-relative path.
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
			ix.resolveRepoBase(c)
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

// RepoBase is the ref a checkout's status and diffs are measured against:
// HEAD, or in "This branch" mode the merge base with its parent branch.
func (ix *Index) RepoBase(prefix string) string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if b := ix.repoBase[prefix]; b != "" {
		return b
	}
	return "HEAD"
}

// CheckoutCounts returns a checkout's changed and staged file counts from the
// last refresh.
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

// resolveRepoBase recomputes a checkout's base for its mode (Task 6).
func (ix *Index) resolveRepoBase(c checkout) {}
