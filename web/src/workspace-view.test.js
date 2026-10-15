import test from 'node:test';
import assert from 'node:assert/strict';

import { visibleCheckouts, rootKey, commitKey, groupTabs } from './workspace-view.js';

const repo = (path, extra = {}) => ({ path, group: path, main: true, branch: 'main', head: 'abc', changes: 0, ahead: 0, ...extra });

test('working set shows agent and set checkouts only when any exist', () => {
  const repos = [repo('a', { agent: true }), repo('b', { inSet: true }), repo('c', { changes: 3 })];
  assert.deepEqual(visibleCheckouts(repos, 'set').map(r => r.path), ['a', 'b']);
});

test('empty working set falls back to uncommitted changes, not ahead-only', () => {
  const repos = [repo('a', { changes: 2 }), repo('b', { ahead: 5 }), repo('c')];
  assert.deepEqual(visibleCheckouts(repos, 'set').map(r => r.path), ['a']);
  assert.deepEqual(visibleCheckouts(repos, 'changes').map(r => r.path), ['a', 'b']);
  assert.equal(visibleCheckouts(repos, 'all').length, 3);
});

test('root key ignores counts and heads, so an agent editing does not redraw the tree', () => {
  const before = [repo('a', { agent: true, changes: 3, head: 'abc' })];
  const after = [repo('a', { agent: true, changes: 4, head: 'def', ahead: 1 })];
  assert.equal(rootKey(before, 'set'), rootKey(after, 'set'));
});

test('root key changes when the listed checkouts change', () => {
  const before = [repo('a', { agent: true }), repo('b')];
  const after = [repo('a', { agent: true }), repo('b', { inSet: true })];
  assert.notEqual(rootKey(before, 'set'), rootKey(after, 'set'));
  assert.notEqual(rootKey(before, 'set'), rootKey(before, 'all'));
});

test('commit cache key names the checkout', () => {
  assert.notEqual(commitKey('sontaran', 'abc'), commitKey('sontaran-x', 'abc'));
});

test('tabs group by checkout in order of first appearance, keeping their indexes', () => {
  const owner = p => p.split('/')[0] === 'docs' ? '' : p.split('/')[0];
  const groups = groupTabs(['a/x.go', 'b/y.go', 'a/z.go', 'docs/n.md', 'b/w.go'], owner);
  assert.deepEqual(groups, [
    { repo: 'a', tabs: [0, 2] },
    { repo: 'b', tabs: [1, 4] },
    { repo: '', tabs: [3] },
  ]);
});
