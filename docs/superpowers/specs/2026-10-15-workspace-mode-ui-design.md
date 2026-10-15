# Workspace mode: explorer, checkouts, stacks

Date: 2026-10-15
Status: draft for review
Part 2 of 2. Builds on part 1 (`2026-10-15-workspace-mode-git-design.md`).
GitHub integration is out of scope.

## Intent

Review and iterate in one place: see what changed across every repo and
worktree, read it as stacked layers, and hand fixes to the coding agent,
without switching between the PR page, an editor, the terminal and the agent.

## v1 scope: one tool that works well enough

Build first, in this order; everything else waits until v1 is in daily use:

1. Part 1 git core.
2. Explorer grouped by repo and worktree, with the Changes / All toggle (§2).
3. Scope to a checkout by clicking its row: tree, file search, text search, git
   panel (§3, without the palette prefix).
4. "This branch" diff toggle (§4), with parent inference rules 1 and 3 only:
   the tracked feature branch, else trunk.
5. Agent runs in the owning checkout (§5).

6. Working set (§6): the explorer shows only the checkouts an agent is running
   in or that were added to the set, and agents control the set from the CLI.

Deferred: the stack section in the git panel, `needs restack`, the
nearest-ancestor parent rule, the `#` palette prefix, and the `+ N clean`
collapse row.

## Principles

- Stay inside px0's existing design. Reuse its tree rows, badges, colours,
  git panel sections, the command palette and the changes scope. No new kinds
  of screens or dialogs.
- Read-only for history. Nothing here creates, rebases or pushes branches.
- Plain git only. No Graphite or other stack metadata; stacks are inferred.

## Terms

- **Checkout**: one working directory with its own `.git` (a repo's main
  worktree or a linked worktree). Part 1 calls these repos.
- **Repo group**: all checkouts sharing one git common dir. Named after the main
  worktree's folder (`sontaran`), and contains its linked worktrees
  (`sontaran-tech1627-session`), whatever their folder names.
- **Trunk**: the repo's default branch, read from the remote's `HEAD`
  (`upstream/HEAD`, else `origin/HEAD`, else `main`/`master`/`dev` as found).

## 1. Checkout model (server)

Part 1's per-repo payload entry gains:
`group` (main worktree path), `main` (bool), `branch` or `detached` (short
SHA), `changes`, `ahead`/`behind` against upstream.

Grouping uses `git rev-parse --git-common-dir`, so it is correct even when
folder names do not match. Linked worktrees whose path is outside the opened
folder are not shown.

## 2. Explorer

The explorer header gets the existing px0 scope toggle with two values:

- **Changes** (default): only checkouts with uncommitted changes or unpushed
  commits. Under each checkout, its changed files, flat, with px0's existing
  status letters. Clicking a file opens its diff. This is the workspace-level
  diff list.
- **All**: every repo group, each expanding into its checkouts, each expanding
  into its real directory tree (existing `/api/tree?dir=`).

Rows:

- Repo group row: folder name, main worktree's branch, change count.
- Checkout row under a group: name without the group prefix
  (`tech1627-session`), branch or `@sha`, change count, `↑n` unpushed.
- Clean checkouts in a group collapse into one dimmed row, `+ 31 clean`, which
  expands in place.
- Repo groups with no changes collapse into one dimmed row at the end.
- Folders that are not repos go in one `Other folders` row at the bottom.

Styling reuses `.tr` rows, `.gs` badges and the existing dirty colours. The
branch label is a muted span after the name, as part 1's tree hook allows.

## 3. Scope to one checkout

Picking a checkout scopes px0 to it: the tree shows only that checkout, fuzzy
file search (`/api/find`) and text search (`/api/search`) take a `scope`
prefix, and the git panel acts on it. The explorer header shows the scope
(`zenact / sontaran-tech1627-session`), with one click back to all
checkouts. Scope is client state, kept in the URL so reloads keep it.

Ways to pick a checkout:

- Click its row in the explorer.
- Command palette, new `#` prefix: fuzzy-match any checkout by name or branch.
  Added to `PAL_MODES` beside `:`, `@` and `>`.

## 4. Stacks

### Parent inference

For a branch `X` in a checkout:

1. If `X` tracks another branch that is not trunk and not `X`'s own remote copy,
   that branch is the parent.
2. Else, among local branches whose tip is an ancestor of `X` and not already
   contained in trunk, the one with the fewest commits to `X` is the parent.
3. Else, trunk.

Repeat from the parent until trunk to get the stack. Detached checkouts have no
stack. Results are cached by branch tip and recomputed only when a tip moves
(the part 1 watcher already sees ref changes).

### Stack section in the git panel

A new section above "Recent commits", in the same style, for the active
checkout (like Graphite's `gt log`, trunk at the bottom):

```
  stack/trace-coverage-on-970   3 commits  ↑1   ← current
  obs/zsbxctrl-client-spans     7 commits       needs restack
  dev
```

Each layer shows its commit count against its parent, unpushed count, and a
`needs restack` marker when its parent's tip is not an ancestor of it. Clicking
a layer that is checked out in a worktree scopes to that worktree; clicking any
layer lists its commits.

### Diff base toggle

The diff view gets a toggle next to its existing whitespace option:

- **Uncommitted**: against `HEAD`, as px0 does today.
- **This branch**: against `merge-base(parent, HEAD)`. Shows only this layer's
  changes, committed and uncommitted, even when the stack is not restacked.

The toggle is per checkout. In "This branch" mode, status badges for that
checkout use the same base, through part 1's per-repo
`gitStatusAgainst(repo, base)`. Nothing else changes, so gutter and diff
availability follow automatically.

## 5. Agent in the right checkout

In workspace mode the coding harness runs with its working directory set to
the checkout that owns the file, so it loads that repo's `AGENTS.md` /
`CLAUDE.md`, git and tooling:

- Inline edit (`Alt+E`): the selected file's checkout.
- Thread anchored to a file: that file's checkout.
- Workspace thread: the scoped checkout, else the workspace root.

`worktreeSnapshot` / `changedSince` run on that checkout, and changed paths are
reported with its prefix so open tabs reload as today.

## 6. Working set

The main purpose is working with agents without leaving the review loop, so the
explorer's default view shows only the checkouts that matter now.

- **Agent running**: a checkout is active when a coding-agent process
  (`claude`, `codex`, `gemini`, `cursor-agent`, `opencode`, `aider`, `goose`,
  `agy`) has its working directory inside it. Read from `/proc/*/cwd` and
  `/proc/*/cmdline` on Linux; elsewhere nothing is detected. Rescanned every
  5 s while a client is connected. The row shows an `agent` marker.
- **Added to the set**: `~/.px0/set.json` (or `$XDG_CONFIG_HOME/px0/set.json`)
  holds `{"items":[{"path":"<absolute checkout path>","note":"...","added":"<RFC3339>"}]}`.
  It lives with px0's other per-user files, never in a workspace. The running
  server stat-polls it each second and broadcasts changes.
- **CLI**, usable by any agent or script, server running or not:
  - `px0 set add [path] [-note text]`: path defaults to `.`; it is resolved to
    its checkout's top level (`git rev-parse --show-toplevel`), so `px0 set add`
    from inside a worktree adds that worktree. Adding again updates the note.
  - `px0 set remove [path]`
  - `px0 set list`: one line per item, `path<TAB>note`.
  - `px0 set clear`
- Views in the explorer toggle: **Working set** (default; falls back to the
  checkouts with changes when the set is empty and no agent runs), **Changes**,
  **All**.
- A row's note shows as muted text after the branch.

## Non-goals

- Creating, restacking, submitting or reordering stacks.
- GitHub PRs, checks or comments in workspace mode. `px0 <pr-url>` still works
  as a separate single-repo session.
- Cleanup actions (removing worktrees). The explorer only shows state.

## Testing

Extends `workspace_test.go` from part 1:

- Grouping: a repo with two linked worktrees whose folder names do not share a
  prefix still forms one group.
- Parent inference: tracked parent wins; nearest-ancestor fallback; merged
  branches skipped; trunk from the remote's HEAD.
- `needs restack` when the parent moved; cleared after rebase.
- "This branch" diff and status equal `git diff merge-base(parent, HEAD)`.
- `/api/find` and `/api/search` with `scope` return only that checkout.
- Agent job run for a file in checkout B uses B as its directory; changed paths
  come back prefixed.

Frontend checks via the existing `make web-check`, plus a manual pass on
`~/workspace/zenact`.
