// web/src/workspace-view.js
// Pure rules for the workspace explorer, kept free of the DOM so they test
// without a browser.

// Which checkouts the explorer root lists for a view: the working set (a
// coding agent runs there, or it was added with `px0 set add`), checkouts with
// changes, or all. An empty working set falls back to uncommitted changes:
// "ahead" alone is too common (branches tracking their own remote copy).
export function visibleCheckouts(repos, view) {
  if (view === 'all') return repos;
  if (view === 'changes') return repos.filter(r => r.changes > 0 || r.ahead > 0);
  const anySet = repos.some(r => r.agent || r.inSet);
  return repos.filter(r => (anySet ? (r.agent || r.inSet) : r.changes > 0));
}

// What decides the root's layout: the listed checkouts and their grouping.
// Counts, heads and branches change while an agent works; those rows are
// patched in place instead, so open folders and scroll position survive.
export function rootKey(repos, view) {
  return view + '|' + visibleCheckouts(repos, view).map(r => r.path + '<' + (r.main ? '' : r.group)).join('|');
}

// A commit's cached file list belongs to the checkout it was read from: two
// worktrees of one repo share SHAs, but their file paths carry their own prefix.
export const commitKey = (repo, sha) => (repo || '') + ':' + sha;
