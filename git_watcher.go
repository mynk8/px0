package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// GitStatusPayload contains the complete serialized git status event sent to the browser
// over Server-Sent Events (SSE). It includes working tree status, staged files, dirty directories,
// current branch, commit ahead/behind counts, and recent commits.
type GitStatusPayload struct {
	Git           bool              `json:"git"`                     // True if root is inside a git repository
	GitChanges    int               `json:"gitChanges"`              // Total count of modified, added, deleted, or untracked files
	GitFiles      []string          `json:"gitFiles"`                // List of modified file paths relative to root
	Statuses      map[string]string `json:"statuses"`                // Relative path -> status code (e.g. "M", "A", "D", "U")
	DirtyDirs     map[string]bool   `json:"dirtyDirs,omitempty"`     // Relative directory path -> true if any descendant is modified
	Staged        map[string]bool   `json:"staged,omitempty"`        // Relative path -> true if file has staged changes
	YourStatuses  map[string]string `json:"yourStatuses,omitempty"`  // PR mode: relative path -> reviewer modification status
	YourDirtyDirs map[string]bool   `json:"yourDirtyDirs,omitempty"` // PR mode: directory path -> contains reviewer changes
	Branch        string            `json:"branch,omitempty"`        // Active branch name
	RecentCommits []GitCommit       `json:"recentCommits,omitempty"` // Recent git commits for commit list UI
	CommitsURL    string            `json:"commitsUrl,omitempty"`    // Web URL to view commits on GitHub/forge
	Ahead         int               `json:"ahead"`                   // Commits ahead of upstream tracking branch
	Behind        int               `json:"behind"`                  // Commits behind upstream tracking branch
	Workspace     bool              `json:"workspace,omitempty"`     // Serving a folder of checkouts (workspace mode)
	Repos         []RepoState       `json:"repos,omitempty"`         // Workspace mode: one entry per checkout
}

// GitWatcher monitors git state changes in the background and broadcasts updates
// to connected browser clients over SSE (/api/stream). It uses a hybrid detection strategy:
//  1. Fast path: 1-second polling of .git metadata files (HEAD, index, packed-refs) to instantly
//     detect CLI operations (git commit, git checkout, git add).
//  2. Worktree path: adaptive polling of working tree file modifications.
//  3. Manual triggers: immediate re-evaluations triggered on user UI actions.
type GitWatcher struct {
	ix   *Index
	root string

	mu      sync.Mutex
	clients map[chan []byte]struct{}
	active  atomic.Int32

	triggerCh chan struct{}
	stopCh    chan struct{}

	lastIndexMod   time.Time
	lastIndexSize  int64
	lastHeadMod    time.Time
	lastPackedMod  time.Time
	lastHeadCommit string
	lastAhead      int
	lastBehind     int

	// Workspace mode: one metadata stamp and one state per checkout.
	stMu       sync.Mutex
	metas      map[string]checkoutMeta
	commonDirs map[string]string
	states     map[string]RepoState
}

// checkoutMeta is what a checkout's git metadata looked like at the last poll.
type checkoutMeta struct {
	index, head, packed, heads, fetch time.Time
	size                              int64
}

// NewGitWatcher constructs a new GitWatcher attached to the given workspace index.
func NewGitWatcher(ix *Index) *GitWatcher {
	gw := &GitWatcher{
		ix:         ix,
		root:       ix.Root(),
		clients:    make(map[chan []byte]struct{}),
		triggerCh:  make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		metas:      map[string]checkoutMeta{},
		commonDirs: map[string]string{},
		states:     map[string]RepoState{},
	}
	if !ix.Workspace() {
		gw.lastHeadCommit = gitHeadCommit(ix.Root())
	}
	return gw
}

// Start begins the background monitoring goroutine.
func (gw *GitWatcher) Start(ctx context.Context) {
	if gitDisabled || !gw.ix.GitOn() {
		return
	}
	if gw.ix.Workspace() {
		gw.scanCheckoutMeta()
		go gw.loop(ctx, "")
		return
	}

	gitdir := gitDir(gw.root)
	gw.recordGitMeta(gitdir)

	go gw.loop(ctx, gitdir)
}

func (gw *GitWatcher) Stop() {
	select {
	case <-gw.stopCh:
	default:
		close(gw.stopCh)
	}
}

// Trigger requests an immediate status check and broadcast.
func (gw *GitWatcher) Trigger() {
	select {
	case gw.triggerCh <- struct{}{}:
	default:
	}
}

func (gw *GitWatcher) recordGitMeta(gitdir string) bool {
	if gitdir == "" {
		return false
	}
	changed := false

	indexPath := filepath.Join(gitdir, "index")
	if fi, err := os.Stat(indexPath); err == nil {
		if fi.ModTime() != gw.lastIndexMod || fi.Size() != gw.lastIndexSize {
			gw.lastIndexMod = fi.ModTime()
			gw.lastIndexSize = fi.Size()
			changed = true
		}
	}

	headPath := filepath.Join(gitdir, "HEAD")
	if fi, err := os.Stat(headPath); err == nil {
		if fi.ModTime() != gw.lastHeadMod {
			gw.lastHeadMod = fi.ModTime()
			changed = true
		}
	}

	packedPath := filepath.Join(gitdir, "packed-refs")
	if fi, err := os.Stat(packedPath); err == nil {
		if fi.ModTime() != gw.lastPackedMod {
			gw.lastPackedMod = fi.ModTime()
			changed = true
		}
	}

	return changed
}

func (gw *GitWatcher) loop(ctx context.Context, gitdir string) {
	metaTicker := time.NewTicker(1 * time.Second)
	defer metaTicker.Stop()

	adaptiveInterval := 1 * time.Second
	worktreeTicker := time.NewTicker(adaptiveInterval)
	defer worktreeTicker.Stop()

	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-gw.stopCh:
			return

		case <-metaTicker.C:
			// Fast path: check .git metadata (HEAD, index, packed-refs).
			// If touched by git CLI (commit, checkout, add, etc.), refresh immediately.
			if gw.ix.Workspace() {
				if changed := gw.scanCheckoutMeta(); len(changed) > 0 {
					gw.refreshFor(changed)
				}
			} else if gw.recordGitMeta(gitdir) {
				gw.checkAndBroadcast()
			}

		case <-worktreeTicker.C:
			// Worktree path: check for modifications in files outside git CLI.
			// Only run when at least one client is actively listening.
			if gw.active.Load() > 0 {
				dur := gw.checkAndBroadcast()
				// Adapt interval based on execution speed: min 1s, max 10s.
				newInterval := dur * 5
				if newInterval < 1*time.Second {
					newInterval = 1 * time.Second
				} else if newInterval > 10*time.Second {
					newInterval = 10 * time.Second
				}
				if newInterval != adaptiveInterval {
					adaptiveInterval = newInterval
					worktreeTicker.Reset(adaptiveInterval)
				}
			}

		case <-gw.triggerCh:
			// Debounce rapid triggers
			time.Sleep(50 * time.Millisecond)
			// Drain any pending triggers queued during debounce
			select {
			case <-gw.triggerCh:
			default:
			}
			gw.checkAndBroadcast()

		case <-heartbeatTicker.C:
			gw.broadcast([]byte(": ping\n\n"))
		}
	}
}

// Refresh runs an immediate UpdateGitStatus, broadcasts to subscribers if changed,
// and returns the latest git status payload.
func (gw *GitWatcher) Refresh() GitStatusPayload {
	return gw.refreshFor(nil)
}

// refreshFor is Refresh limited, in workspace mode, to the named checkouts
// (nil: all). Other checkouts keep their cached status and identity.
func (gw *GitWatcher) refreshFor(prefixes []string) GitStatusPayload {
	count, files, changed, statuses, dirtyDirs, staged, yourStatuses, yourDirtyDirs := gw.ix.UpdateGitStatusFor(prefixes)
	if gw.ix.Workspace() {
		// git status refreshes a checkout's index file; re-stamp so the
		// watcher does not take its own write for a CLI change.
		gw.restamp(prefixes)
		if gw.updateStates(prefixes) {
			changed = true
		}
		payload := GitStatusPayload{
			Git: true, GitChanges: count, GitFiles: files, Statuses: statuses,
			DirtyDirs: dirtyDirs, Staged: staged, Workspace: true, Repos: gw.RepoStates(),
		}
		if changed {
			gw.broadcastPayload(payload)
		}
		return payload
	}
	var recentCommits []GitCommit
	var ahead, behind int
	if gitAvailable(gw.root) {
		recentCommits = gitRecentCommits(gw.root, 5)
		headCommit := ""
		if len(recentCommits) > 0 {
			headCommit = recentCommits[0].Hash
		}
		ahead, behind = gw.aheadBehind()
		gw.mu.Lock()
		if headCommit != gw.lastHeadCommit || ahead != gw.lastAhead || behind != gw.lastBehind {
			gw.lastHeadCommit = headCommit
			gw.lastAhead = ahead
			gw.lastBehind = behind
			changed = true
		}
		gw.mu.Unlock()
	}

	branch := gitCurrentBranch(gw.root)
	payload := GitStatusPayload{
		Git:           gitAvailable(gw.root),
		GitChanges:    count,
		GitFiles:      files,
		Statuses:      statuses,
		DirtyDirs:     dirtyDirs,
		Staged:        staged,
		YourStatuses:  yourStatuses,
		YourDirtyDirs: yourDirtyDirs,
		Branch:        branch,
		RecentCommits: recentCommits,
		CommitsURL:    gitCommitsWebURL(gw.root, branch),
		Ahead:         ahead,
		Behind:        behind,
	}
	if changed {
		gw.broadcastPayload(payload)
	}
	return payload
}

func (gw *GitWatcher) broadcastPayload(payload GitStatusPayload) {
	data, err := json.Marshal(payload)
	if err == nil {
		gw.broadcast([]byte(fmt.Sprintf("event: git-status\ndata: %s\n\n", data)))
	}
}

// updateStates recomputes the identity (group, branch, head, ahead/behind) of
// the named checkouts (nil: all) and every checkout's counts. Reports whether
// any state changed.
func (gw *GitWatcher) updateStates(prefixes []string) bool {
	cos := gw.ix.Checkouts()
	todo := cos
	if prefixes != nil {
		want := map[string]bool{}
		for _, p := range prefixes {
			want[p] = true
		}
		todo = nil
		for _, c := range cos {
			gw.stMu.Lock()
			_, known := gw.states[c.Prefix]
			gw.stMu.Unlock()
			if want[c.Prefix] || !known {
				todo = append(todo, c)
			}
		}
	}
	ids := make([]RepoState, len(todo))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, c := range todo {
		wg.Add(1)
		go func(i int, c checkout) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ids[i] = repoIdentity(gw.root, c)
		}(i, c)
	}
	wg.Wait()

	gw.stMu.Lock()
	defer gw.stMu.Unlock()
	changed := false
	next := make(map[string]RepoState, len(cos))
	for _, c := range cos {
		next[c.Prefix] = gw.states[c.Prefix]
	}
	for _, id := range ids {
		next[id.Path] = id
	}
	for p, st := range next {
		st.Changes, st.Staged = gw.ix.CheckoutCounts(p)
		st.Mode, st.Parent = gw.ix.RepoMode(p)
		next[p] = st
		if gw.states[p] != st {
			changed = true
		}
	}
	if len(next) != len(gw.states) {
		changed = true
	}
	gw.states = next
	return changed
}

// RepoStates lists every checkout's state: grouped by repo, the main
// checkout first, then by path.
func (gw *GitWatcher) RepoStates() []RepoState {
	gw.stMu.Lock()
	out := make([]RepoState, 0, len(gw.states))
	for _, st := range gw.states {
		out = append(out, st)
	}
	gw.stMu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		if out[i].Main != out[j].Main {
			return out[i].Main
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// statCheckout stamps the git metadata a CLI command touches: the index and
// HEAD of this worktree, and the refs shared by every worktree of the repo.
func (gw *GitWatcher) statCheckout(c checkout) checkoutMeta {
	gw.stMu.Lock()
	cd, ok := gw.commonDirs[c.Prefix]
	gw.stMu.Unlock()
	if !ok {
		cd = gitCommonDir(c.Abs)
		gw.stMu.Lock()
		gw.commonDirs[c.Prefix] = cd
		gw.stMu.Unlock()
	}
	var m checkoutMeta
	gd := gitDir(c.Abs)
	if fi, err := os.Stat(filepath.Join(gd, "index")); err == nil {
		m.index, m.size = fi.ModTime(), fi.Size()
	}
	if fi, err := os.Stat(filepath.Join(gd, "HEAD")); err == nil {
		m.head = fi.ModTime()
	}
	if cd != "" {
		if fi, err := os.Stat(filepath.Join(cd, "packed-refs")); err == nil {
			m.packed = fi.ModTime()
		}
		if fi, err := os.Stat(filepath.Join(cd, "refs", "heads")); err == nil {
			m.heads = fi.ModTime()
		}
		if fi, err := os.Stat(filepath.Join(cd, "FETCH_HEAD")); err == nil {
			m.fetch = fi.ModTime()
		}
	}
	return m
}

// restamp records the current metadata of the named checkouts (nil: all)
// without reporting them as changed.
func (gw *GitWatcher) restamp(prefixes []string) {
	want := map[string]bool{}
	for _, p := range prefixes {
		want[p] = true
	}
	for _, c := range gw.ix.Checkouts() {
		if prefixes == nil || want[c.Prefix] {
			m := gw.statCheckout(c)
			gw.stMu.Lock()
			gw.metas[c.Prefix] = m
			gw.stMu.Unlock()
		}
	}
}

// scanCheckoutMeta stamps every checkout and returns the ones whose metadata
// moved since the last scan, sorted. The first scan only records.
func (gw *GitWatcher) scanCheckoutMeta() []string {
	var changed []string
	for _, c := range gw.ix.Checkouts() {
		m := gw.statCheckout(c)
		gw.stMu.Lock()
		old, seen := gw.metas[c.Prefix]
		gw.metas[c.Prefix] = m
		gw.stMu.Unlock()
		if seen && old != m {
			changed = append(changed, c.Prefix)
		}
	}
	sort.Strings(changed)
	return changed
}

// aheadBehind counts local commits not yet on the remote. In a PR review the
// checkout is a detached HEAD with no upstream, so "ahead" is measured from the
// PR head it was checked out at (or last pushed to) -- exactly the reviewer's
// own commits. Behind stays 0 there: knowing it needs a fetch, which Pull does.
func (gw *GitWatcher) aheadBehind() (ahead, behind int) {
	if head := gw.ix.PushedHead(); head != "" {
		return gitCountSince(gw.root, head), 0
	}
	ahead, behind, _ = gitAheadBehind(gw.root)
	return ahead, behind
}

// checkAndBroadcast runs UpdateGitStatus and broadcasts to subscribers if changed.
// Returns duration of the status check.
func (gw *GitWatcher) checkAndBroadcast() time.Duration {
	start := time.Now()
	gw.Refresh()
	return time.Since(start)
}

func (gw *GitWatcher) broadcast(msg []byte) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	for ch := range gw.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

// Subscribe registers a new SSE listener channel and returns an unregister cancel function.
func (gw *GitWatcher) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 16)

	gw.mu.Lock()
	gw.clients[ch] = struct{}{}
	gw.active.Add(1)
	gw.mu.Unlock()

	// Immediately send current state to newly connected client
	inGit := gw.ix.GitOn()
	var initialMsg []byte
	if inGit {
		payload := gw.Refresh()
		if data, err := json.Marshal(payload); err == nil {
			initialMsg = []byte(fmt.Sprintf("event: git-status\ndata: %s\n\n", data))
		}
	} else {
		payload := GitStatusPayload{
			Git:        false,
			GitChanges: 0,
			GitFiles:   []string{},
			Statuses:   map[string]string{},
		}
		if data, err := json.Marshal(payload); err == nil {
			initialMsg = []byte(fmt.Sprintf("event: git-status\ndata: %s\n\n", data))
		}
	}

	if len(initialMsg) > 0 {
		ch <- initialMsg
	}

	cancel := func() {
		gw.mu.Lock()
		if _, ok := gw.clients[ch]; ok {
			delete(gw.clients, ch)
			gw.active.Add(-1)
			close(ch)
		}
		gw.mu.Unlock()
	}

	return ch, cancel
}
