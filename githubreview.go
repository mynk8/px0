package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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

// errNoPR: the checkout's branch has no open pull request.
var errNoPR = errors.New("no pull request for this checkout")

// reviewCheckoutRe matches the worktrees px0 creates to review a PR.
var reviewCheckoutRe = regexp.MustCompile(`-review-pr(\d+)$`)

// prNumberFor finds a checkout's pull request: a review checkout names it;
// otherwise it is the open PR whose head is the checkout's branch on the
// remote the branch is pushed to.
func prNumberFor(ctx context.Context, token string, c checkout) (owner, repo string, number int, err error) {
	branch := gitCurrentBranch(c.Abs)
	owner, repo, headOwner, ok := prRepoFor(c.Abs, branch)
	if !ok {
		return "", "", 0, errNoPR
	}
	if m := reviewCheckoutRe.FindStringSubmatch(filepath.Base(c.Abs)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return owner, repo, n, nil
	}
	if branch == "" || branch == "HEAD" {
		return "", "", 0, errNoPR
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=5&head=%s", owner, repo, url.QueryEscape(headOwner+":"+branch))
	resp, err := githubRequest(ctx, "GET", path, token, nil)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", 0, fmt.Errorf("github: %s", resp.Status)
	}
	var prs []struct {
		Number int `json:"number"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return "", "", 0, err
	}
	if len(prs) == 0 {
		return "", "", 0, errNoPR
	}
	return owner, repo, prs[0].Number, nil
}

// checkoutPRBinding is a checkout's pull request session, valid while the
// checkout stays on the branch it was looked up for. A miss (no PR) is kept
// briefly so switching files does not ask GitHub again and again.
type checkoutPRBinding struct {
	branch string
	p      *prSession // nil: no PR for this branch
	err    error
	at     time.Time
}

const noPRRetry = time.Minute

// prFor returns the review session a /api/pr/* request is about, and the
// checkout prefix that maps its paths ("" in single-PR mode). In workspace
// mode that is the PR of the checkout named by ?repo=.
func (s *Server) prFor(w http.ResponseWriter, r *http.Request) (*prSession, string, bool) {
	if s.pr != nil {
		return s.pr, "", true
	}
	if !s.ix.Workspace() {
		fail(w, http.StatusNotFound, "not a PR review session")
		return nil, "", false
	}
	_, prefix, ok := s.repoRoot(w, r)
	if !ok {
		return nil, "", false
	}
	c, _ := s.ix.CheckoutByPrefix(prefix)
	p, err := s.checkoutPR(r.Context(), c)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, errNoPR) {
			code = http.StatusNotFound
		}
		fail(w, code, err.Error())
		return nil, "", false
	}
	return p, prefix, true
}

// checkoutPR binds a checkout to its pull request: metadata, token, write
// access, and the merge base with the PR's base branch for its file set.
func (s *Server) checkoutPR(ctx context.Context, c checkout) (*prSession, error) {
	branch := gitCurrentBranch(c.Abs)
	s.prsMu.Lock()
	b := s.prs[c.Prefix]
	s.prsMu.Unlock()
	if b != nil && b.branch == branch && (b.p != nil || time.Since(b.at) < noPRRetry) {
		return b.p, b.err
	}
	p, err := newCheckoutPR(ctx, c)
	s.prsMu.Lock()
	if s.prs == nil {
		s.prs = map[string]*checkoutPRBinding{}
	}
	s.prs[c.Prefix] = &checkoutPRBinding{branch: branch, p: p, err: err, at: time.Now()}
	s.prsMu.Unlock()
	return p, err
}

func newCheckoutPR(ctx context.Context, c checkout) (*prSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	provider := &GitHubProvider{}
	token, _ := provider.ResolveToken(readSettings())
	owner, repo, num, err := prNumberFor(ctx, token, c)
	if err != nil {
		return nil, err
	}
	target := PRTarget{Provider: "github", Owner: owner, Repo: repo, Number: num, URL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, num)}
	meta, err := provider.FetchPR(ctx, target, token)
	if err != nil {
		return nil, err
	}
	diffBase, warning := "HEAD", ""
	if remote := prRemoteName(c.Abs, owner, repo); remote != "" {
		if mb := gitMergeBase(c.Abs, "HEAD", remote+"/"+meta.BaseRef); mb != "" {
			diffBase = mb
		}
	}
	if diffBase == "HEAD" {
		warning = "could not resolve a merge-base with " + meta.BaseRef + "; fetch the base branch"
	}
	return &prSession{
		provider: provider, target: target, meta: meta, token: token,
		writeAccess: provider.CheckPushAccess(ctx, target, token),
		diffBase:    diffBase, diffBaseWarning: warning, worktree: c.Abs,
	}, nil
}

// prFiles is the PR's own file set (merge base..PR head) with status letters,
// keyed by workspace path in workspace mode.
func (s *Server) prFiles(p *prSession, prefix string) map[string]string {
	if prefix == "" {
		return s.ix.PRFiles()
	}
	out := map[string]string{}
	for k, v := range gitFilesBetween(p.worktree, p.diffBase, p.meta.HeadSHA) {
		out[prefix+"/"+k] = v
	}
	return out
}

// withPrefix makes a checkout-relative path workspace-relative.
func withPrefix(prefix, path string) string {
	if prefix == "" || path == "" {
		return path
	}
	return prefix + "/" + path
}

// handlePROpen opens a pull request inside the workspace (POST {target}): a
// detached worktree at the PR head, beside the checkout whose remote is the
// PR's repo, named <checkout>-review-pr<N>. An existing one is reused.
func (s *Server) handlePROpen(w http.ResponseWriter, r *http.Request) {
	if !localPost(w, r) {
		return
	}
	if !s.ix.Workspace() {
		fail(w, http.StatusBadRequest, "only in workspace mode")
		return
	}
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "target is required")
		return
	}
	_, target, ok := DetectPRURL(body.Target)
	if !ok || target.Provider != "github" {
		fail(w, http.StatusBadRequest, "target must be a GitHub pull request URL")
		return
	}
	// The repo's main checkout first: its folder names the review checkout.
	var src checkout
	var remote string
	for _, mainOnly := range []bool{true, false} {
		for _, c := range s.ix.Checkouts() {
			if mainOnly && gitCommonDir(c.Abs) != filepath.Join(c.Abs, ".git") {
				continue
			}
			if rem := prRemoteName(c.Abs, target.Owner, target.Repo); rem != "" && remote == "" {
				src, remote = c, rem
			}
		}
	}
	if remote == "" {
		fail(w, http.StatusNotFound, fmt.Sprintf("no checkout in this workspace has %s/%s as a remote", target.Owner, target.Repo))
		return
	}
	dir := filepath.Join(filepath.Dir(src.Abs), fmt.Sprintf("%s-review-pr%d", filepath.Base(src.Abs), target.Number))
	if _, err := os.Stat(dir); err != nil {
		token, _ := resolveGitHubToken(readSettings())
		ref := fmt.Sprintf("refs/px0/pr/%d", target.Number)
		if out, err := gitAuthCmd(token, "-C", src.Abs, "fetch", "--no-tags", remote, fmt.Sprintf("+refs/pull/%d/head:%s", target.Number, ref)).CombinedOutput(); err != nil {
			fail(w, http.StatusBadGateway, "git fetch PR head: "+strings.TrimSpace(string(out)))
			return
		}
		if out, err := exec.Command("git", "-C", src.Abs, "worktree", "add", "--detach", dir, ref).CombinedOutput(); err != nil {
			fail(w, http.StatusBadGateway, "git worktree add: "+strings.TrimSpace(string(out)))
			return
		}
		s.reindex()
	}
	rel, err := filepath.Rel(s.ix.Root(), dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		fail(w, http.StatusInternalServerError, "review checkout is outside the workspace")
		return
	}
	writeJSON(w, map[string]any{"checkout": filepath.ToSlash(rel)})
}

// handlePRCloseCheckout removes a review checkout (POST ?repo=). Never forced:
// a checkout with changes stays, and git's reason is returned.
func (s *Server) handlePRCloseCheckout(w http.ResponseWriter, r *http.Request) {
	if !localPost(w, r) {
		return
	}
	root, prefix, ok := s.repoRoot(w, r)
	if !ok {
		return
	}
	if !reviewCheckoutRe.MatchString(filepath.Base(root)) {
		fail(w, http.StatusBadRequest, "only review checkouts (*-review-pr<N>) are removed here")
		return
	}
	if out, err := exec.Command("git", "-C", root, "worktree", "remove", root).CombinedOutput(); err != nil {
		fail(w, http.StatusConflict, strings.TrimSpace(string(out)))
		return
	}
	s.prsMu.Lock()
	delete(s.prs, prefix)
	s.prsMu.Unlock()
	s.reindex()
	writeJSON(w, map[string]any{"ok": true})
}

// reindex rebuilds the index after checkouts appeared or went, as /api/reindex does.
func (s *Server) reindex() {
	EvictAll()
	s.ix.Build()
	if s.gitWatcher != nil {
		s.gitWatcher.Trigger()
	}
}
