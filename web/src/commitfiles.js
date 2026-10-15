// web/src/commitfiles.js
// A commit's changed files as sidebar rows, shared by Recent Commits
// (gitpanel.js) and the Unpushed section (unpushed.js), styled by the .up-file
// rules. Rows are built COMMIT_FILES_PAGE at a time behind a "Show N more" row,
// so a commit of thousands of files costs no more DOM than one of a hundred
// until someone asks for more.
import { activeRepo, repoQuery } from './workspace.js';
import { esc, api } from './state.js';

export const COMMIT_FILES_PAGE = 100;

const fileLists = new Map(); // SHA -> CommitFile[] once loaded, or the pending Promise

// A commit's files never change, so each list is fetched once and shared by the
// sidebar rows and the diff strip.
// Keyed by checkout too: in workspace mode two repos can share no SHA in
// practice, but the request must name the repo the commit lives in.
const cacheKey = sha => activeRepo() + ':' + sha;

export function loadCommitFiles(sha) {
  const key = cacheKey(sha);
  const have = fileLists.get(key);
  if (have) return Promise.resolve(have);
  const req = api('/api/commitfiles', { sha, ...repoQuery() }).then(
    j => { const files = j.files || []; fileLists.set(key, files); return files; },
    e => { fileLists.delete(key); throw e; });
  fileLists.set(key, req);
  return req;
}

export function cachedCommitFiles(sha) {
  const have = fileLists.get(cacheKey(sha));
  return Array.isArray(have) ? have : null;
}

/* git's name-status letter -> the badge classes the file tree already uses, so
   a commit's file list reads the same as the working-tree one. */
export const COMMIT_STATUS = {
  M: ['git-M', 'modified'], A: ['git-A', 'added'], D: ['git-D', 'deleted'],
  R: ['git-R', 'renamed'], C: ['git-A', 'copied'], T: ['git-M', 'type changed'],
  U: ['git-untracked', 'unmerged'],
};

// Rows files[from, to), then a "Show more" row if any are left. active is the
// path to mark selected.
export function commitFileRows(files, from, to, active = '') {
  to = Math.min(files.length, to);
  let html = '';
  for (let i = from; i < to; i++) html += commitFileRow(files[i], files[i].path === active);
  const left = files.length - to;
  if (left > 0) {
    html += '<div class="up-more" role="button" tabindex="0">Show ' + Math.min(left, COMMIT_FILES_PAGE) +
      ' more · ' + left.toLocaleString() + ' remaining</div>';
  }
  return html;
}

function commitFileRow(f, sel) {
  const g = COMMIT_STATUS[f.status] || ['git-M', f.status];
  const name = f.path.split('/').pop();
  const dir = f.path.slice(0, f.path.length - name.length).replace(/\/$/, '');
  const from = f.from ? ' (was ' + f.from + ')' : '';
  const stat = f.binary ? '<span class="up-file-bin">bin</span>'
    : (f.add ? '<span class="up-file-add">+' + f.add + '</span>' : '') +
      (f.del ? '<span class="up-file-del">&minus;' + f.del + '</span>' : '');
  return '<div class="up-file' + (sel ? ' sel' : '') + '" data-path="' + esc(f.path) + '" tabindex="0" role="button" ' +
    'title="' + esc(f.path + from) + ' — ' + esc(g[1]) + ' in this commit">' +
    '<span class="up-file-name">' + esc(name) + '</span>' +
    (dir ? '<span class="up-file-dir">' + esc(dir) + '</span>' : '') +
    (stat ? '<span class="up-file-stat">' + stat + '</span>' : '') +
    '<span class="gs ' + g[0] + '" title="' + esc(g[1]) + '">' + esc(f.status) + '</span>' +
  '</div>';
}
