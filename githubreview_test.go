package main

import (
	"path/filepath"
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
