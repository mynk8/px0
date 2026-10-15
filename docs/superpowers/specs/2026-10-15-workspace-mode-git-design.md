# Workspace mode: git across many repos

Date: 2026-10-15
Status: draft for review
Part 1 of 3 (2: workspace UI, 3: GitHub PR client — separate specs)

## Problem

px0 opens one folder and treats it as one git repo. Every git call is
`git -C <workspace root>`. When the folder holds many independent repos and
linked worktrees (for example `~/workspace/zenact`: ~25 repos, ~100 worktrees,
~175k files), the root is not a repo, `gitProbe` fails, and git is off
everywhere: no badges, no diff view, no gutter, no git panel.

Making the workspace an outer repo does not help: git reports each nested repo
as one untracked `name/` entry with no file-level status.

## Goal

Open a folder of repos and get, for every file in every nested repo, what px0
gives a single repo today: status badges, dirty folders, side-by-side diff,
gutter markers, commit history and commit diffs, stage/unstage.

## Non-goals (v1)

- PR review mode stays single-repo.
- No outer-repo case: if the opened folder is itself inside a repo, px0 behaves
  as today.
- No new UI beyond what is needed for whole-repo actions to name their repo
  (repo grouping and switching is part 2).
- No GitHub features (part 3).

## Activation

Workspace mode turns on when the opened folder is a directory, is not inside a
git repo, and contains at least one nested repo. No flag. `-no-git` still turns
all git off. Single-repo behaviour is unchanged, and existing tests must pass
untouched.

## Design

### Repo discovery (index.go)

- During the walk, a directory with a `.git` entry (directory, or file for a
  linked worktree or submodule) is a repo root. Record its workspace-relative
  path on the Index.
- Skip `.git` files as well as `.git` directories, so worktree pointers are not
  indexed as ordinary files.
- Keep walking inside a repo root. A repo nested inside another repo is its own
  repo; the longest matching prefix owns a path.
- Discovery result: a sorted list of repo prefixes. `ix.RepoFor(rel)` returns
  `(repoAbs, repoRel, prefix, ok)` by longest prefix.

### Status (index.go Build / UpdateGitStatus)

- Run `gitStatusAgainst(repoAbs, "HEAD")` and `gitStagedPaths(repoAbs)` for
  every repo in parallel, bounded by `runtime.NumCPU()`.
- Prefix each key with the repo's workspace path and merge into the existing
  `gitStatusMap` / `gitStagedMap`. `dirtyDirs`, `gitFiles` and the tree overlay
  then work unchanged, since they already expect workspace-relative keys.
- Build cannot know repos before the walk, so in workspace mode status runs
  after the walk and is applied with the same in-place patch
  `UpdateGitStatus` uses. The early root snapshot ships without badges; they
  arrive with the first status event.
- Per-repo results are cached; a refresh re-runs only repos flagged as changed.

### Per-file git calls (server.go)

Every call that has a path resolves it first: `RepoFor(rel)` → run against
`repoAbs` with `repoRel`. This covers `/api/diff`, `/api/gutter`,
`diffAvailable` on `/api/file` (both branches), stage, unstage, and diffs with
`ref=<sha>`. In single-repo mode `RepoFor` returns the root and the path
unchanged, so call sites change in one way only.

`gitAvailable(s.ix.Root())` gates become "git is on": true in workspace mode
when at least one repo was found. `/api/meta` `git` follows the same rule.

### Whole-repo calls

Commit, commit message, push, pull, `/api/git/log`, `/api/unpushed`,
`/api/commitfiles`, `/api/commitdetail` take a `repo` parameter (the repo's
workspace-relative path). It is required in workspace mode and ignored in
single-repo mode. Stage-all (`path: "."`) stages within that repo.
`CommitFile.Path` values are returned with the repo prefix so
`openFile(path, {ref})` keeps working, and `/api/file?ref=` resolves the repo
from the path.

### Status payload

`GitStatusPayload` gains `repos: [{path, branch, head, ahead, behind, changes,
staged}]`. In workspace mode the single-value fields (`branch`, `ahead`,
`behind`, `recentCommits`, `commitsUrl`) are left empty: the client reads the
active repo's entry from `repos` and fetches its commits with
`/api/git/log?repo=`. The stream stays one connection with no per-client state.
In single-repo mode `repos` is omitted and nothing else changes.

### Watcher (git_watcher.go)

- One meta record per repo: `index`, `HEAD`, and for linked worktrees the
  common dir's `packed-refs` and `refs/heads` (today's watcher misses these for
  worktrees). Stat-polling ~100 repos × 4 files every second is cheap.
- A meta change marks that repo dirty and triggers a refresh of only that repo.
- The adaptive working-tree ticker (5× last run, 1–10 s, only with clients
  connected) runs the parallel status across all repos, as today's ticker does
  for one.

### Frontend (minimal for v1)

- Git panel: acts on the repo of the active tab, shows that repo's path next to
  the branch, and sends `repo` with every whole-repo request.
- Commit caches keyed by `repo + sha` instead of `sha`.
- Everything that reads `statuses[path]` is unchanged.

### Agent and threads

`worktreeSnapshot` / `changedSince` use the merged status in workspace mode, and
`Tracked` is true when the edited file is inside a repo.

## Performance budget

On `~/workspace/zenact`: first status for all repos under 3 s, a refresh after a
change in one repo under 300 ms, idle CPU no higher than single-repo px0 with
the same number of open clients. Measure with the existing metrics endpoint.

## Testing

New `workspace_test.go` using the existing `gitRepo` helpers:

- Workspace of two repos plus a linked worktree: status keys are prefixed, the
  worktree's `.git` file is not indexed, and dirty folders propagate to the repo
  folder.
- Per-file diff, gutter and stage/unstage route to the right repo.
- `/api/commitfiles?repo=` returns prefixed paths; commit diffs open.
- Watcher: a commit made in one repo by the CLI refreshes only that repo.
- Nested repo inside a repo: longest prefix wins.
- The full existing suite passes unchanged (single-repo behaviour).

## Open questions

None blocking. Upstreaming to px0 can be decided after v1 works.
