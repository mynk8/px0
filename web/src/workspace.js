// web/src/workspace.js
// Workspace mode: the opened folder holds many checkouts (repos and linked
// worktrees). The explorer root lists checkouts grouped by repo instead of raw
// folders, filtered by view: the working set (a coding agent runs there, or it
// was added with `px0 set add`), checkouts with changes, or all of them.
import { $, S, esc, api } from './state.js';
import { visibleCheckouts, rootKey } from './workspace-view.js';
import { emit } from './bus.js';

S.repos = [];
S.scope = new URLSearchParams(location.search).get('scope') || '';
S.wsView = (() => { try { return localStorage.getItem('px0.wsView') || 'set'; } catch { return 'set'; } })();

export const isWorkspace = () => !!S.meta?.workspace;

// The checkout that owns path: the longest checkout prefix, or ''.
export function repoOf(path) {
  let best = '';
  for (const r of S.repos) {
    if ((path === r.path || path.startsWith(r.path + '/')) && r.path.length > best.length) best = r.path;
  }
  return best;
}

// The checkout whole-repo actions apply to: the scoped one, else the one
// owning the active tab.
export function activeRepo() {
  const d = S.tabs[S.active];
  return S.scope || repoOf(d?.path || '');
}

export const repoState = path => S.repos.find(r => r.path === path) || null;

export const repoQuery = () => (isWorkspace() ? { repo: activeRepo() } : {});

// For JSON posts, whose params travel in the body: the repo goes on the path.
export const withRepo = path => {
  const r = isWorkspace() ? activeRepo() : '';
  return r ? path + (path.includes('?') ? '&' : '?') + 'repo=' + encodeURIComponent(r) : path;
};

let layoutKey = '';
let redraw = null; // tree.js hands its refresh in, to avoid an import cycle
const listeners = [];
export function setRedraw(fn) { redraw = fn; }
export function onScopeChange(fn) { listeners.push(fn); }

export async function loadRepos() {
  if (!isWorkspace()) return;
  try { onRepos((await api('/api/repos')).repos); } catch {}
}

export function onRepos(repos) {
  if (!repos) return;
  S.repos = repos;
  renderScopeBar();
  emit('repos:changed', {}); // tab groups take their names and colours from here
  if (S.scope) return;
  const next = rootKey(repos, S.wsView);
  if (next !== layoutKey) redraw?.(); // drawWorkspaceRoot records the new key
  else patchCheckoutRows();
}

// Updates the branch, note and counts of listed checkout rows in place,
// leaving their open folders, selection and the scroll position alone.
function patchCheckoutRows() {
  const tree = $('#tree');
  if (!tree) return;
  const tmp = document.createElement('div');
  for (const r of S.repos) {
    const row = tree.querySelector('.tr.ws-co[data-dir="' + CSS.escape(r.path) + '"]');
    if (!row) continue;
    tmp.innerHTML = checkoutRow(r, 0);
    const fresh = tmp.querySelector('.tr');
    row.innerHTML = fresh.innerHTML;
    row.title = fresh.title;
    row.className = fresh.className + (row.classList.contains('open') ? ' open' : '') + (row.classList.contains('sel') ? ' sel' : '');
  }
}

export function setView(v) {
  S.wsView = v;
  try { localStorage.setItem('px0.wsView', v); } catch {}
}

export function setScope(prefix) {
  S.scope = prefix || '';
  const u = new URL(location.href);
  if (S.scope) u.searchParams.set('scope', S.scope); else u.searchParams.delete('scope');
  history.replaceState(null, '', u);
  renderScopeBar();
  redraw?.();
  for (const fn of listeners) fn(S.scope);
}

// Under its group, a worktree drops the repeated group name ("sontaran-x" -> "x").
function label(r) {
  if (r.main) return r.path.split('/').pop();
  const g = (r.group || '').split('/').pop();
  const name = r.path.split('/').pop();
  return g && name.startsWith(g + '-') ? name.slice(g.length + 1) : name;
}

// One theme colour per repo, so a repo's worktrees read as a family. Repos
// take colours in alphabetical turn rather than by hash, so neighbours never
// share one; the same mapping colours explorer dots and tab groups.
const REPO_HUES = ['--k', '--kt', '--nc', '--s', '--m', '--err', '--nf', '--nt', '--o'];
export function repoHue(group) {
  const groups = [...new Set(S.repos.map(r => (r.main ? r.path : r.group) || r.path))].sort();
  const i = Math.max(0, groups.indexOf(group));
  return 'var(' + REPO_HUES[i % REPO_HUES.length] + ')';
}

function checkoutRow(r, depth) {
  const pad = 8 + depth * 12;
  const detached = !(r.branch && r.branch !== 'HEAD');
  const ref = detached ? (r.head ? '@' + r.head : '') : r.branch;
  // State drives colour: agent working > uncommitted changes > unpushed only > clean.
  const state = r.agent ? ' ws-agent-on' : r.changes ? ' ws-dirty' : r.ahead ? ' ws-ahead' : ' ws-clean';
  const marks =
    (r.agent ? '<span class="ws-agent" title="A coding agent is working here">agent</span>' : '') +
    (r.changes ? '<span class="ws-n" title="Uncommitted changes">' + r.changes + '</span>' : '') +
    (r.ahead ? '<span class="ws-up" title="Commits not pushed">↑' + r.ahead + '</span>' : '');
  const dirty = r.changes ? ' dirty' : '';
  const title = r.path + (ref ? ' · ' + ref : '') + (r.note ? ' — ' + r.note : '');
  return '<div class="tw"><div class="tr dir ws-co' + dirty + state + (detached ? ' ws-detached' : '') + '" data-dir="' + esc(r.path) + '" style="padding-left:' + pad + 'px" title="' + esc(title) + '">' +
    '<span class="ar"></span><span class="ws-dot" style="background:' + repoHue(r.main ? r.path : r.group) + '"></span><span class="nm">' + esc(label(r)) + '</span>' +
    '<span class="ws-br">' + esc(ref) + '</span>' +
    (r.note ? '<span class="ws-note">' + esc(r.note) + '</span>' : '') + marks +
    '<button class="ws-focus" data-focus="' + esc(r.path) + '" title="Scope px0 to this checkout" aria-label="Scope to this checkout"><svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="7"/><circle cx="12" cy="12" r="2"/></svg></button></div>' +
    '<div class="kids" data-kids="' + esc(r.path) + '"></div></div>';
}

export async function drawWorkspaceRoot(container) {
  layoutKey = rootKey(S.repos, S.wsView);
  const shown = visibleCheckouts(S.repos, S.wsView);
  const groups = new Map();
  for (const r of shown) {
    const g = r.main ? r.path : (r.group || r.path);
    if (!groups.has(g)) groups.set(g, []);
    groups.get(g).push(r);
  }
  let html = '';
  for (const [g, list] of [...groups.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    list.sort((a, b) => (b.main - a.main) || a.path.localeCompare(b.path));
    const main = list.find(r => r.path === g);
    if (main) html += checkoutRow(main, 0);
    else html += '<div class="ws-group" title="' + esc(g) + '"><span class="ws-dot" style="background:' + repoHue(g) + '"></span>' + esc(g.split('/').pop()) + '</div>';
    for (const r of list) if (r !== main) html += checkoutRow(r, 1);
  }
  if (S.wsView === 'all') {
    let j;
    try { j = await api('/api/tree', { dir: '' }); } catch { j = { children: [] }; }
    const isCheckout = p => S.repos.some(r => r.path === p);
    const others = (j.children || []).filter(c => c.dir && !c.ignored && !isCheckout(c.path));
    if (others.length) {
      html += '<div class="ws-group">Other folders</div>' + others.map(c =>
        '<div class="tw"><div class="tr dir" data-dir="' + esc(c.path) + '" style="padding-left:8px" title="Folder: ' + esc(c.path) + '"><span class="ar"></span><span class="nm">' + esc(c.name) + '</span></div><div class="kids" data-kids="' + esc(c.path) + '"></div></div>').join('');
    }
  }
  if (!html) {
    html = '<div class="ws-empty">' + (S.wsView === 'set'
      ? 'No agent is working and nothing has been added. Add a checkout with <code>px0 set add</code>.'
      : 'No checkouts with changes.') + '</div>';
  }
  container.innerHTML = html;
}

export function renderScopeBar() {
  const el = $('#ws-scope');
  if (!el) return;
  el.hidden = !isWorkspace() || !S.scope;
  if (el.hidden) return;
  const r = repoState(S.scope);
  const ref = r ? (r.branch && r.branch !== 'HEAD' ? r.branch : (r.head ? '@' + r.head : '')) : '';
  el.innerHTML = '<span class="ws-scope-name" title="' + esc(S.scope) + '">' + esc(S.scope) + '</span>' +
    (ref ? '<span class="ws-br">' + esc(ref) + '</span>' : '') +
    '<button id="ws-unscope" title="Back to all checkouts" aria-label="Back to all checkouts">✕</button>';
}

export function initWorkspace() {
  $('#ws-scope')?.addEventListener('click', e => {
    if (e.target.closest('#ws-unscope')) setScope('');
  });
}
