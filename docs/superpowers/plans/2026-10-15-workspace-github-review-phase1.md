# Workspace GitHub Review — Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Each checkout shows its PR's review state and checks; a right-sidebar Review tab lists the active checkout's review threads with reply and resolve.

**Architecture:** A new `githubreview.go` holds the remote/owner resolution, a small GraphQL client, the batched summary query, the detail query and the two mutations. `git_watcher.go` keeps a per-checkout PR summary cache, refreshed on a 5-minute ticker while clients are connected and when a checkout's identity moves; summaries ride on `RepoState.pr`. Three handlers in `server.go` serve detail, reply and resolve. The frontend adds `web/src/review.js` (the tab) and a chip in `workspace.js` checkout rows.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`), GitHub REST + GraphQL v4, plain ES modules.

**Spec:** `docs/superpowers/specs/2026-10-15-workspace-github-review-design.md` (Phase 1).

## Global Constraints

- No GitHub write without an explicit POST from the page; each POST sends once.
- No token: summaries still load for public repos only if the API allows, every write route returns 403, write controls hidden.
- At most 20 aliases per GraphQL request; summaries refresh at most every 5 minutes per repo unless a checkout's branch or head moved.
- Run Go with `GOWORK=off`; build `web/app.js` first.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. A checkout whose remote is not GitHub (or has no remote) gets no lookup and no error toast. Task 1.
2. A fork: PR repo is `upstream`, head owner is `origin`'s owner; a same-named branch from someone else's fork must not match. Task 2.
3. A GraphQL error response (HTTP 200 with `errors`) is surfaced as an error, not as "no PR". Task 2.
4. Reply/resolve without a token return 403 and never call GitHub. Task 4.
5. Thread paths come back workspace-relative (checkout prefix added) so clicking opens the right file. Task 3.

---

### Task 1: Remote → GitHub repo and head owner

**Files:** Create `githubreview.go`, `githubreview_test.go`.

**Produces:** `func prRepoFor(root, branch string) (owner, repo, headOwner string, ok bool)`.

- [ ] Test: fixture repo with `origin` = `git@github.com:me/sontaran.git` and `upstream` = `https://github.com/zenact/sontaran`: returns `zenact, sontaran, me, true`. Only `origin` = `https://github.com/zenact/x.git`: `zenact, x, zenact, true`. Remote on `gitlab.com` or none: `ok=false`. Branch tracking `upstream/feat`: head owner `zenact`.
- [ ] Run, see FAIL (undefined).
- [ ] Implement with `git config --get remote.<name>.url`, `remoteHostPath`, `branch.<b>.remote`.
- [ ] Run, PASS; full suite; commit "Resolve a checkout's GitHub PR repo and head owner".

### Task 2: GraphQL client and batched PR summaries

**Files:** Modify `github.go` (`githubAPIBase` const → var; add `githubGraphQLURL` var), `githubreview.go`, test.

**Produces:**
- `func githubGraphQL(ctx, token, query string, vars map[string]any, out any) error` — POST `{query, variables}`; non-2xx → error; `errors[]` non-empty → error with the first message.
- `type PRSummary struct { Number int; Title, URL, State string; Draft bool; Review, Checks string; Unresolved int; HeadOID string }` (json lower-camel).
- `func fetchPRSummaries(ctx, token, owner, repo, headOwner string, branches []string) (map[string]PRSummary, error)` — aliases `b0..bN`, chunks of 20.

- [ ] Test with an `httptest` server standing in for `githubGraphQLURL`: two branches; branch `feat` has two PRs, the newer from `someone-else` (fork) and an older from `me` → picks `me`'s; rollup `FAILURE`; 3 threads, 1 resolved, 1 outdated → `Unresolved == 1`; branch `none` → absent from the map. A response with `{"errors":[{"message":"bad"}]}` → error containing "bad". Request body uses ≤20 aliases for 25 branches (two requests).
- [ ] FAIL, implement, PASS, suite, commit "Batched GitHub PR summaries per checkout branch".

### Task 3: Summaries on RepoState, refresh policy, detail endpoint

**Files:** `git_watcher.go`, `workspace.go` (`RepoState.PR *PRSummary \`json:"pr,omitempty"\``), `githubreview.go` (`fetchPRDetail`), `server.go` (`GET /api/review`), tests.

**Produces:**
- `func (gw *GitWatcher) refreshPRs(prefixes []string)` — groups checkouts (non-detached, branch ≠ trunk short name) by PR repo, calls `fetchPRSummaries`, stores into `gw.prs[prefix]`, then `overlayChanged()`. Errors are logged once per repo, not broadcast.
- Loop: `prTicker` 5 min, only when `active > 0` and a token resolves; `refreshFor(changed)` also triggers `go gw.refreshPRs(changed)` for checkouts whose identity changed.
- `updateStates` copies `gw.prs[p]` into `st.PR`.
- `type PRDetail struct { PR PRSummary; Checks []PRCheck; Threads []PRThread }`, `PRCheck{Name, State, URL}`, `PRThread{ID string; Resolved, Outdated bool; Path string; Line int; Side string; Comments []PRThreadComment}`, `PRThreadComment{ID int64; Author, Body, CreatedAt, URL string}`.
- `func fetchPRDetail(ctx, token, owner, repo string, number int) (PRDetail, error)`.
- `GET /api/review?repo=` → `PRDetail` with thread paths prefixed by the checkout; 404 `{error:"no pull request for this branch"}` when the checkout has no PR; 400 for unknown repo.

- [ ] Tests: fake GitHub serving summary + detail; `/api/review?repo=alpha-wt` returns threads with `path` = `alpha-wt/sub/s.go`; a checkout with no PR → 404; RepoStates carry `pr.number` after `refreshPRs(nil)`.
- [ ] FAIL, implement, PASS, suite (+ `-race` on new tests), commit "PR summaries on checkouts and a review detail endpoint".

### Task 4: Reply and resolve

**Files:** `githubreview.go` (`resolveThread`), `server.go` (`POST /api/review/reply`, `POST /api/review/resolve`), tests.

- [ ] Tests: no token (isolated settings, env cleared) → both 403 and the fake server saw no request. With token: reply posts to `/repos/o/r/pulls/N/comments/{id}/replies` with the body; resolve sends `resolveReviewThread` with the thread id; `resolved:false` sends `unresolveReviewThread`. Both refresh the checkout summary.
- [ ] FAIL, implement, PASS, suite, commit "Reply to and resolve review threads".

### Task 5: Explorer PR chip

**Files:** `web/src/workspace-view.js` (`prChip(pr)` pure: returns `{label, tone, checks}`), test in `workspace-view.test.js`, `web/src/workspace.js` (`checkoutRow` appends chip), `web/style.css`.

- [ ] Test `prChip`: approved → tone `ok`; changes requested → `bad`; merged → `dim`; checks FAILURE/ERROR → `bad`, PENDING → `wait`, SUCCESS → `ok`; null → null.
- [ ] FAIL, implement, PASS, web-check; browser check on the real workspace; commit "Explorer: PR chip with review and checks state".

### Task 6: Review tab

**Files:** Create `web/src/review.js`; modify `web/index.html` (inspector tab + pane), `web/src/inspector.js` (pane toggle + `emit('review:shown')`), `web/src/main.js` (`initReview()`), `web/src/workspace-view.js` (`groupThreads(threads)` pure + test), `web/style.css`.

- [ ] Test `groupThreads`: unresolved non-outdated grouped by path in path order; resolved and outdated go to a separate folded list.
- [ ] Implement the tab: loads `/api/review?repo=activeRepo()` on show, on checkout change, and when `repos:changed` carries a different `pr.unresolved`/`headOID` for the active checkout. Renders header, checks, threads; thread click → `openFile(path, {view: 'diff'})` then `scrollDiffToLine(line)`; reply box → POST reply, then reload; Resolve → POST resolve, then reload. Write controls hidden when `S.meta.githubToken` is false.
- [ ] Gutter markers: register `setPRSyncHandler` in workspace mode to mark diff rows of the active doc whose new-file line has an unresolved thread (class `rv-mark` on the line-number cell).
- [ ] web-check; browser check against a real PR in the workspace (read-only: no reply/resolve on real PRs during verification); commit "Review tab: threads, checks, reply and resolve".

### Task 7: Docs, install

- [ ] README "Workspace mode" gains a "Review" paragraph. `make check`, `make build`, install `px0-ws`, commit "Document workspace review".
