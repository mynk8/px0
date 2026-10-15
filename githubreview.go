package main

import (
	"os/exec"
	"strings"
)

// GitHub review in workspace mode: each checkout's branch is matched to its
// pull request, whose review state, checks and threads px0 shows and answers.

// gitConfig reads one git config value in root ("" when unset).
func gitConfig(root, key string) string {
	out, err := exec.Command("git", "-C", root, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// githubOwnerRepo parses a remote URL into a GitHub owner and repo.
func githubOwnerRepo(remoteURL string) (owner, repo string, ok bool) {
	host, path, ok := remoteHostPath(remoteURL)
	if !ok || host != "github.com" {
		return "", "", false
	}
	owner, repo, found := strings.Cut(path, "/")
	if !found || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return owner, repo, true
}

// prRepoFor names where a checkout's pull request lives and who owns its head
// branch. The PR repo is the upstream remote when there is one (a fork's PRs
// are opened against it), else origin. The head owner is the owner of the
// remote the branch is pushed to: its tracking remote, else origin.
func prRepoFor(root, branch string) (owner, repo, headOwner string, ok bool) {
	prRemote := "upstream"
	u := gitConfig(root, "remote.upstream.url")
	if u == "" {
		prRemote, u = "origin", gitConfig(root, "remote.origin.url")
	}
	owner, repo, ok = githubOwnerRepo(u)
	if !ok {
		return "", "", "", false
	}
	pushRemote := gitConfig(root, "branch."+branch+".remote")
	if pushRemote == "" || pushRemote == "." {
		pushRemote = "origin"
	}
	headOwner = owner
	if pushRemote != prRemote {
		if ho, _, hok := githubOwnerRepo(gitConfig(root, "remote."+pushRemote+".url")); hok {
			headOwner = ho
		}
	}
	return owner, repo, headOwner, true
}
