// node --test .github/scripts/publish-update.test.mjs
import assert from 'node:assert/strict'
import test from 'node:test'
import { pullRequestOf, updateFor } from './publish-update.mjs'

const rows = [
  { sha: 'a1', type: 'feature', title: 'Switch stores from the account menu' },
  { sha: 'b2', type: 'fix', title: 'Stop the store list going stale after sign-out' },
]

test('a merged pull request is filed under its number, title and description', () => {
  const pr = {
    number: 27, title: 'Store switcher',
    body: '## What this does\n\nOwners pick a store from the menu.\n\n## Testing\n\n- 137/137 tests\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)',
  }
  const u = updateFor(rows, pr, 'abc1234def', 'https://kitcommerce.store/updates/')
  assert.equal(u.ref, 'gocommerce#27')
  assert.equal(u.title, 'Store switcher')
  // Readers get what changed, not how it was tested or who wrote it.
  assert.equal(u.body, 'Owners pick a store from the menu.')
  assert.deepEqual(u.points, ['Switch stores from the account menu', 'Fix: Stop the store list going stale after sign-out'])
  assert.equal(u.link, 'https://kitcommerce.store/updates/')
})

test('a description written on Windows reads the same', () => {
  const u = updateFor(rows, { number: 1, title: 't', body: 'One.\r\n\r\nTwo.\r\n\r\n## Testing\r\n- ok' }, 'abc1234', 'https://k.test/')
  assert.equal(u.body, 'One.\n\nTwo.')
})

test('a push with no pull request is filed under its commit', () => {
  const u = updateFor(rows, null, 'abc1234def', 'https://kitcommerce.store/updates/')
  assert.equal(u.ref, 'gocommerce@abc1234')
  assert.equal(u.title, 'GoCommerce update: 1 feature, 1 fix')
  assert.match(u.body, /Switch stores/)
})

// The ref is the pull request's number, so the lookup must not quietly fall
// back to the commit when GitHub is unreachable: a re-run that then found the
// PR would file the same change twice under two refs.
test('a lookup GitHub cannot answer fails, rather than changing the ref', async () => {
  const down = async () => ({ ok: false, status: 502, json: async () => ({}) })
  await assert.rejects(pullRequestOf('abc', down), /502/)
  const offline = async () => { throw new Error('fetch failed') }
  await assert.rejects(pullRequestOf('abc', offline), /fetch failed/)
})

test('a direct push has no pull request; a merged one is found', async () => {
  const answer = (prs) => async () => ({ ok: true, status: 200, json: async () => prs })
  assert.equal(await pullRequestOf('abc', answer([])), null)
  assert.equal(await pullRequestOf('abc', answer([{ number: 3, merged_at: null }])), null)
  assert.equal((await pullRequestOf('abc', answer([{ number: 7, merged_at: '2026-10-16' }]))).number, 7)
})
