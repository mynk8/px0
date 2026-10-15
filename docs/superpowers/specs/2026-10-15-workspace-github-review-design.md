# Workspace mode: GitHub review (multi-repo awareness)

Date: 2026-10-15
Status: approved
Builds on: `2026-10-15-workspace-mode-git-design.md`, `2026-10-15-workspace-mode-ui-design.md`.
Supersedes the first draft of this spec (a new Review tab, PR chips and
batched GraphQL summaries), which was dropped: px0 already has the review UI.

## Intent

Use px0's existing pull-request review (the PR bar, line comments with Alt+R,
the Comments panel, replies, Submit / Approve / Request changes) across all
the repos of a workspace, instead of one PR per px0 process.

## 1. Opening a PR into the workspace

- The palette's existing "Open Pull Request…" command, in workspace mode,
  opens the PR inside the running px0 instead of starting a new process.
- px0 finds the checkout whose remote is the PR's repo: any remote whose URL
  is `github.com/<owner>/<repo>` (`upstream` in a fork setup), preferring a
  repo's main checkout.
- It fetches `refs/pull/<N>/head` from that remote and adds a git worktree
  beside the checkout, named `<checkout>-review-pr<N>`, detached at the PR
  head. An existing one is reused.
- px0 re-indexes and scopes to the new checkout. It is an ordinary checkout:
  explorer, diffs, "This branch" and the git panel all work on it.
- The PR bar of a review checkout has "Remove checkout". It asks first, then
  runs `git worktree remove` (never forced: a checkout with changes is kept
  and px0 says so).
- Single-PR mode (`px0 <pr-url>`) uses the same remote match, so a fork whose
  `origin` is the user's copy reuses the local repo instead of cloning.

## 2. The review UI follows the active checkout

- Every `/api/pr/*` route takes `?repo=<checkout>` in workspace mode, like the
  git routes. Single-PR mode is unchanged.
- A checkout's PR: for a `*-review-pr<N>` checkout, PR N of its repo;
  otherwise the open PR whose head is the checkout's branch on its push
  remote's owner (`GET /repos/o/r/pulls?head=owner:branch&state=open`).
  Looked up when first needed and cached per checkout and branch.
- Paths cross the boundary workspace-relative: the server strips the checkout
  prefix from draft comments and adds it to every path it returns.
- The frontend loads the active checkout's PR on tab switch and scope change.
  With a PR, the PR bar and Comments panel show it; without one, they hide.
  Drafts are kept per checkout in memory.
- px0 never writes to GitHub without an explicit click, and uses the existing
  token resolution; without a token the PR UI is read-only, as today.

## Non-goals

New review UI, PR status in the explorer, CI checks, resolving threads,
GitLab.

## Testing

Go tests with a fake GitHub (`githubAPIBase` made a variable): remote
matching (fork and plain), PR lookup by branch and by review-checkout name,
`/api/pr/*` with `?repo=` (paths prefixed and stripped, 404 without a PR,
400 for an unknown repo), opening a PR into a fixture workspace (worktree
created from a local bare "remote" with a `refs/pull/N/head` ref, reused on a
second open) and removing it. Frontend verified in the browser with a real PR,
read-only.
