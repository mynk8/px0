# Workspace GitHub Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** px0's existing PR review works across a workspace: open a PR as a worktree beside its repo, and the PR bar / Comments panel follow the active checkout's PR.

**Architecture:** `githubreview.go` gains remote matching (`prRemoteName`), PR lookup for a checkout, the per-checkout `prSession` binding cache, and the open/remove handlers. Every `/api/pr/*` handler swaps `prOrFail` + `s.pr` for `s.prFor(w, r)`, which returns the single-PR session or the active checkout's binding, plus the prefix used to map paths. `pr.js` loads the active checkout's PR and sends `?repo=` on every call.

**Spec:** `docs/superpowers/specs/2026-10-15-workspace-github-review-design.md`.

## Global Constraints
- Single-PR mode behaviour unchanged; full suite stays green.
- No GitHub write without an explicit POST; `git worktree remove` never forced.
- Run Go with `GOWORK=off`; build `web/app.js` first. Commits end with the Co-Authored-By line.

## Review Focus
1. A draft whose path is outside the bound checkout is refused (400), never posted with a stray path. Task 3.
2. A checkout whose branch has no open PR returns 404 and the UI hides the bar, never shows the previous checkout's PR. Tasks 3, 5.
3. Opening the same PR twice reuses the worktree. Task 4.
4. Removing a review checkout with uncommitted changes fails with 409 and leaves it. Task 4.
5. Single-PR mode with a fork (`origin` = own copy, `upstream` = PR repo) uses a worktree of the local repo. Task 1.

### Task 1: Remote matching; single-PR mode uses it
- `prRemoteName(root, owner, repo string) string` — first remote (upstream, origin, then others) whose URL is github.com/owner/repo.
- `checkoutPR`: replace the loose origin substring match with `prRemoteName(info.toplevel, ...)`; fetch the PR head from that remote.
- Test: fixture repo with `origin` = me/x and `upstream` = o/x → `prRemoteName(.., "o","x") == "upstream"`, `(.., "z","x") == ""`.

### Task 2: PR lookup for a checkout
- `githubAPIBase` const → var (tests point it at a fake).
- `prNumberFor(ctx, token, c checkout) (owner, repo string, number int, err error)`: `*-review-pr<N>` → N; else `GET /repos/o/r/pulls?head=<headOwner>:<branch>&state=open&per_page=5` → first number; none → `errNoPR`.
- Tests with `httptest`: review-checkout name; branch lookup sends `head=me:feat`; empty list → `errNoPR`.

### Task 3: `/api/pr/*` per checkout
- `Server.prs map[string]*prSession` + mutex; `newCheckoutPR(ctx, c)` builds a session: target, meta (`FetchPR`), token, writeAccess, `diffBase` = merge-base of HEAD and `<prRemote>/<baseRef>` (else "HEAD"), `worktree` = checkout path. Cached by prefix+branch.
- `s.prFor(w, r) (*prSession, string, bool)`; all handlers use it; meta `files` = `gitFilesBetween(c.Abs, diffBase, headSHA)` prefixed; existing comments and drafts returned with prefixed paths; POST draft strips the prefix (400 if outside); session-file draft persistence only in single-PR mode.
- Tests: fake GitHub serving pulls list, PR meta, comments; `/api/pr/meta?repo=alpha-wt` has number and prefixed files; `/api/pr/existing-comments?repo=` paths prefixed; draft POST with `alpha-wt/sub/s.go` stored as `sub/s.go`, returned prefixed; outside path → 400; checkout without PR → 404; unknown repo → 400; no `repo` in workspace → 400.

### Task 4: Open a PR into the workspace; remove it
- `POST /api/pr/open {target}` (workspace only): parse URL, find the matching checkout (main first), fetch `refs/pull/N/head:refs/px0/pr/N` from the matched remote, `git worktree add --detach <dir>/<name>-review-pr<N>`, reindex, return `{checkout}`. Reuse if the folder exists.
- `POST /api/pr/close-checkout?repo=` (only `*-review-pr<N>`): `git worktree remove` (409 with git's message on failure), drop the binding, reindex.
- Tests: fixture with a bare "remote" holding `refs/pull/5/head`; open → checkout `alpha-review-pr5` exists in `Checkouts()` at that commit; open again → same, no error; dirty → close 409; clean → removed.

### Task 5: Frontend
- `pr.js`: every call through `withRepo(...)`; `initPR` in workspace mode wires the UI once and follows the active checkout (`tab:activated`, `onScopeChange`): loads `/api/pr/meta`, shows bar + panel + comments, or hides them on 404. Keeps `S.meta.pr` untouched in workspace mode; exports `hasActivePR()`.
- `linecomment.js:51`: `S.meta?.pr` → `hasActivePR()`.
- `launchPR` in workspace mode → `/api/pr/open`, then `loadRepos()`, `setScope(checkout)`.
- PR bar "Remove checkout" button for `*-review-pr<N>` checkouts → `confirm()` → close-checkout → `setScope('')`.
- Browser check with PR #1039 (read-only: no posting).

### Task 6: Docs, build, install.
