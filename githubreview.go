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

// prRemoteName returns the remote of root whose URL is github.com/owner/repo
// (upstream first, then origin, then any other), or "" when none is. In a fork
// setup origin is one's own copy and upstream is where the PRs live.
func prRemoteName(root, owner, repo string) string {
	out, err := exec.Command("git", "-C", root, "remote").Output()
	if err != nil {
		return ""
	}
	names := strings.Fields(string(out))
	order := []string{"upstream", "origin"}
	for _, n := range names {
		if n != "upstream" && n != "origin" {
			order = append(order, n)
		}
	}
	for _, n := range order {
		o, r, ok := githubOwnerRepo(gitConfig(root, "remote."+n+".url"))
		if ok && strings.EqualFold(o, owner) && strings.EqualFold(r, repo) {
			return n
		}
	}
	return ""
}

// srcRemoteFor names the remote of a reused local repo that holds the PR's
// repo, keeping origin when nothing matches (the historical behaviour).
func srcRemoteFor(srcRepo string, target PRTarget) string {
	if srcRepo != "" {
		if r := prRemoteName(srcRepo, target.Owner, target.Repo); r != "" {
			return r
		}
	}
	return "origin"
}
