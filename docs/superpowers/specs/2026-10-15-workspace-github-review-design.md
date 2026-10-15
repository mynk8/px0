# Workspace mode: GitHub review

Date: 2026-10-15
Status: approved (phase 1 in build; phase 2 after phase 1 is tried)
Builds on: `2026-10-15-workspace-mode-git-design.md`, `2026-10-15-workspace-mode-ui-design.md`.

## Intent

Review and respond to GitHub review without leaving px0: see each checkout's
PR state at a glance, read and answer review threads next to the diff, and
(phase 2) review other people's PRs in the same place.

## Principles

- One new right-sidebar tab, "Review", plus one chip on explorer rows. No new
  bars or dialogs.
- px0 never writes to GitHub without an explicit click; Reply, Resolve and
  Submit each send exactly once.
- Uses the existing token resolution (`github.token` setting, `GITHUB_TOKEN`,
  `GH_TOKEN`, `gh auth token`). Without a token everything is read-only and
  the write controls are hidden.
- Light on API budget: one batched GraphQL query per repo for summaries, every
  5 minutes while a client is connected and on demand when a branch moves;
  full detail only for the checkout the Review tab shows.

## Phase 1: your own PRs

### Finding a checkout's PR

- PR repo: the `upstream` remote if present, else `origin`, read with
  `git config --get remote.<name>.url` and `remoteHostPath`. Only `github.com`.
- Head owner: the owner of `origin`, or of the branch's upstream remote when
  that is not the PR repo, for forks.
- Query: `repository(owner, name) { pullRequests(headRefName: <branch>,
  first: 5, orderBy: UPDATED_AT DESC) }`, keeping the most recent PR whose
  `headRepositoryOwner` is the head owner. Many branches of one repo share one
  query through GraphQL aliases, at most 20 per request.
- Detached checkouts and trunk branches have no PR lookup.

### Summary (all checkouts)

`RepoState.pr`: `{number, title, url, state (OPEN|CLOSED|MERGED), draft,
review (APPROVED|CHANGES_REQUESTED|REVIEW_REQUIRED|""), checks
(SUCCESS|FAILURE|PENDING|ERROR|""), unresolved (count of unresolved, not
outdated threads)}`.

Explorer row: a `#123` chip after the branch. Its colour shows the review
decision: approved uses the added colour, changes requested the deleted colour,
anything else is muted. A dot before it shows checks: green success, red
failure or error, amber pending. Merged and closed PRs are dimmed.

### Detail (the Review tab's checkout)

`GET /api/review?repo=<prefix>` returns:

- the summary;
- failing or pending checks: `{name, state, url}`, from the latest commit's
  `statusCheckRollup` contexts (CheckRun and StatusContext);
- threads: `{id, resolved, outdated, path (workspace-relative), line, side,
  comments: [{id, author, body, createdAt, url}]}`.

The tab shows, top down:

1. The PR title linking to GitHub, its state, and the review decision.
2. Checks: a one-line summary, with each failing check linking to its run.
3. Unresolved threads grouped by file. Each shows its comments, a reply box,
   and Resolve.
4. Resolved and outdated threads, folded.

Clicking a thread opens the file's diff at that line. Diff rows that hold an
unresolved thread get a marker in the line-number gutter.

### Actions

- `POST /api/review/reply?repo=` `{commentId, body}`: the REST reply to a
  review comment (existing `replyToReviewComment`).
- `POST /api/review/resolve?repo=` `{threadId, resolved}`: GraphQL
  `resolveReviewThread` / `unresolveReviewThread`.

Both are local POSTs, need a token (403 without), and refresh the checkout's
summary afterwards.

## Phase 2: reviewing other people's PRs (next)

- "Open PR…" in the Review tab: paste a URL, or pick from review requests
  (`search: is:pr is:open review-requested:@me`).
- Creates a git worktree from the local checkout of that repo, beside it,
  named `<repo>-review-pr<N>`, detached at the PR head. It is an ordinary
  checkout in the explorer.
- Drafts use px0's existing line-comment composer and collect in the tab;
  Submit as Comment, Approve or Request changes in one review.
- "Remove review checkout" removes the worktree after a confirmation.

## Non-goals

Merging, editing or deleting posted comments, reactions, multi-line comment
ranges, GitLab.

## Testing

Go tests against a fake GitHub (`httptest`), with the API base URLs made
variables: remote and owner resolution, batched summary parsing, detail
parsing (paths prefixed with the checkout), reply and resolve requests, and
403 without a token. Frontend pure logic (chip state, thread grouping) in
`workspace-view.test.js`. UI verified in the browser against real PRs in the
workspace, read-only.
