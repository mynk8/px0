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
