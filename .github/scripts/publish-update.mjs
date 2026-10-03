// Publishes what a push to main changed: a row per commit on
// kitcommerce.store/updates/, and one short post per push on Discord, X and
// Instagram. Run by .github/workflows/updates.yml in two steps:
//
//   node publish-update.mjs record    commits -> <landing>/src/data/updates.json
//   node publish-update.mjs announce  the rows record added -> the channels
//
// Plain Node with no packages, so the workflow needs no install step.
//
// Each commit is filed as a feature or a bug fix from its subject (see
// classify). A trailer overrides the guess, and `Update: skip` keeps a commit
// off every channel:
//
//   Update: fix | feature | skip
//
// A channel whose secret is not set is skipped, so the workflow is green
// before anything is configured and each channel turns on on its own.
import { execFileSync } from 'node:child_process'
import { createHmac, randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const env = process.env
const REPO = env.GITHUB_REPOSITORY || 'itswadesh/gocommerce'
const PAGE = env.UPDATES_PAGE_URL || 'https://kitcommerce.store/updates/'
// What `record` hands `announce`: only the rows it added, so a re-run of the
// workflow finds them already on the page and posts nothing twice.
const HANDOFF = path.join(env.RUNNER_TEMP || '.', 'new-updates.json')

const SKIP_PREFIX = /^(merge\b|release\b|revert "|(chore|docs|ci|test|build|style|refactor)(\(.+\))?!?:)/i
const FIX_PREFIX = /^fix(\(.+\))?!?:\s*/i
const FEAT_PREFIX = /^feat(\(.+\))?!?:\s*/i
// This repository's subjects are sentences, not conventional commits, so a
// fix is recognised by how its sentence starts or by a word only a fix uses.
const FIX_WORDS = /^(fix|fixes|fixed|stop|stops|correct|repair|restore|prevent|guard|unbreak|no longer|don't|do not)\b|\b(bug|broken|regression|crash|crashes|wrong|incorrect)\b/i

export function classify(subject, body) {
  const trailer = (body.match(/^Update:\s*(feature|fix|skip)\s*$/im) || [])[1]
  if (trailer) return { type: trailer.toLowerCase(), title: subject.replace(FIX_PREFIX, '').replace(FEAT_PREFIX, '') }
  if (FIX_PREFIX.test(subject)) return { type: 'fix', title: subject.replace(FIX_PREFIX, '') }
  if (FEAT_PREFIX.test(subject)) return { type: 'feature', title: subject.replace(FEAT_PREFIX, '') }
  if (SKIP_PREFIX.test(subject)) return { type: 'skip', title: subject }
  return { type: FIX_WORDS.test(subject) ? 'fix' : 'feature', title: subject }
}

function commits() {
  const zero = /^0+$/
  const before = env.BEFORE && !zero.test(env.BEFORE) ? env.BEFORE : ''
  const after = env.AFTER || 'HEAD'
  // A first push, or a force-push whose old tip is gone, has no range: take
  // the tip alone rather than every commit in history.
  let range = [after, '-1']
  if (before) {
    try { execFileSync('git', ['cat-file', '-e', before + '^{commit}'], { stdio: 'ignore' }); range = [`${before}..${after}`] } catch {}
  }
  const out = execFileSync('git', ['log', '--no-merges', '--reverse', '--format=%H%x1f%cI%x1f%s%x1f%b%x1e', ...range], { encoding: 'utf8' })
  return out.split('\x1e').map((r) => r.trim()).filter(Boolean).map((r) => {
    const [sha, date, subject, body = ''] = r.split('\x1f')
    return { sha, date: date.slice(0, 10), ...classify(subject, body), url: `https://github.com/${REPO}/commit/${sha}` }
  }).filter((c) => c.type !== 'skip')
}

function record() {
  const found = commits()
  let added = found
  if (env.LANDING_DIR) {
    const file = path.join(env.LANDING_DIR, 'src', 'data', 'updates.json')
    const rows = existsSync(file) ? JSON.parse(readFileSync(file, 'utf8')) : []
    const seen = new Set(rows.map((r) => r.sha))
    added = found.filter((c) => !seen.has(c.sha))
    // Newest first, and within a day in the order they were committed.
    const merged = [...added.slice().reverse(), ...rows]
    mkdirSync(path.dirname(file), { recursive: true })
    writeFileSync(file, JSON.stringify(merged, null, 2) + '\n')
  }
  writeFileSync(HANDOFF, JSON.stringify(added))
  console.log(`${found.length} change(s) in the push, ${added.length} new.`)
}

// ── the channels ───────────────────────────────────────────────────────────

const count = (n, one, many) => `${n} ${n === 1 ? one : many}`
const clip = (s, n) => (s.length > n ? s.slice(0, n - 1).trimEnd() + '…' : s)
const headline = (feats, fixes) =>
  [feats.length && count(feats.length, 'feature', 'features'), fixes.length && count(fixes.length, 'fix', 'fixes')].filter(Boolean).join(', ')

async function discord(feats, fixes) {
  const lines = (rows) => {
    // A field holds 1024 characters; what does not fit is counted, not cut.
    let out = ''
    for (const [i, r] of rows.entries()) {
      const line = `[\`${r.sha.slice(0, 7)}\`](${r.url}) ${clip(r.title, 90)}\n`
      if (out.length + line.length > 980) { out += `…and ${rows.length - i} more`; break }
      out += line
    }
    return out.trim()
  }
  const fields = []
  if (feats.length) fields.push({ name: '✨ Features', value: lines(feats) })
  if (fixes.length) fields.push({ name: '🐛 Bug fixes', value: lines(fixes) })
  await send('Discord', env.DISCORD_WEBHOOK_URL, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username: 'GoCommerce',
      embeds: [{ title: `GoCommerce update — ${headline(feats, fixes)}`, url: PAGE, color: 0x00add8, fields, timestamp: new Date().toISOString() }],
    }),
  })
}

// X counts every link as 23 characters and an emoji as two; the budget below
// leaves room for both, so a post is never refused for length.
function shortPost(feats, fixes, budget) {
  const head = `GoCommerce update: ${headline(feats, fixes)}`
  const all = [...feats.map((r) => `✨ ${r.title}`), ...fixes.map((r) => `🐛 ${r.title}`)]
  const body = []
  let used = head.length + 2
  for (const [i, l] of all.entries()) {
    const line = clip(l, 90)
    const more = all.length - i - 1 ? 12 : 0
    if (used + line.length + 2 + more > budget) { body.push(`+${all.length - i} more`); break }
    body.push(line)
    used += line.length + 2
  }
  return [head, '', ...body].join('\n')
}

async function x(feats, fixes) {
  const keys = [env.X_API_KEY, env.X_API_SECRET, env.X_ACCESS_TOKEN, env.X_ACCESS_SECRET]
  if (keys.some((k) => !k)) return skipped('X')
  const text = shortPost(feats, fixes, 245) + `\n\n${PAGE}`
  const url = 'https://api.x.com/2/tweets'
  await send('X', url, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: oauth1('POST', url, ...keys) },
    body: JSON.stringify({ text }),
  })
}

// OAuth 1.0a user context, which is what posting as an account needs. A JSON
// body is not part of the signature, so only the oauth_ parameters are signed.
function oauth1(method, url, consumerKey, consumerSecret, token, tokenSecret) {
  const enc = (s) => encodeURIComponent(s).replace(/[!'()*]/g, (c) => '%' + c.charCodeAt(0).toString(16).toUpperCase())
  const p = {
    oauth_consumer_key: consumerKey,
    oauth_nonce: randomBytes(16).toString('hex'),
    oauth_signature_method: 'HMAC-SHA1',
    oauth_timestamp: Math.floor(Date.now() / 1000).toString(),
    oauth_token: token,
    oauth_version: '1.0',
  }
  const params = Object.keys(p).sort().map((k) => `${enc(k)}=${enc(p[k])}`).join('&')
  const base = [method, enc(url), enc(params)].join('&')
  p.oauth_signature = createHmac('sha1', `${enc(consumerSecret)}&${enc(tokenSecret)}`).update(base).digest('base64')
  return 'OAuth ' + Object.keys(p).sort().map((k) => `${enc(k)}="${enc(p[k])}"`).join(', ')
}

// Instagram publishes only images, from a public URL, and only from a
// business or creator account: create a media container, wait for it to be
// ready, publish it. A caption cannot carry a link, so it names the page.
async function instagram(feats, fixes) {
  const user = env.INSTAGRAM_USER_ID
  const token = env.INSTAGRAM_ACCESS_TOKEN
  if (!user || !token) return skipped('Instagram')
  const graph = 'https://graph.facebook.com/v21.0'
  const image = env.INSTAGRAM_IMAGE_URL || 'https://kitcommerce.store/og/gocommerce.jpg'
  const caption = shortPost(feats, fixes, 1800) + `\n\nFull log: ${PAGE.replace(/^https:\/\//, '')}\n\n#golang #ecommerce #opensource #gocommerce`
  const made = await send('Instagram', `${graph}/${user}/media`, form({ image_url: image, caption, access_token: token }))
  if (!made?.id) return
  for (let i = 0; i < 10; i++) {
    const r = await fetch(`${graph}/${made.id}?fields=status_code&access_token=${encodeURIComponent(token)}`).then((r) => r.json())
    if (r.status_code === 'FINISHED') break
    if (r.status_code === 'ERROR') return failed('Instagram', `media container ${made.id} failed`)
    await new Promise((res) => setTimeout(res, 3000))
  }
  await send('Instagram', `${graph}/${user}/media_publish`, form({ creation_id: made.id, access_token: token }))
}

const form = (o) => ({ method: 'POST', headers: { 'content-type': 'application/x-www-form-urlencoded' }, body: new URLSearchParams(o).toString() })

let problems = 0
const skipped = (name) => console.log(`${name}: not configured, skipped.`)
const failed = (name, why) => { problems++; console.log(`::error::${name}: ${why}`) }

async function send(name, url, init) {
  if (!url) return skipped(name)
  try {
    const res = await fetch(url, init)
    const text = await res.text()
    if (!res.ok) return failed(name, `${res.status} ${clip(text, 300)}`)
    console.log(`${name}: posted.`)
    try { return JSON.parse(text) } catch { return {} }
  } catch (e) {
    failed(name, e.message)
  }
}

async function announce() {
  const rows = existsSync(HANDOFF) ? JSON.parse(readFileSync(HANDOFF, 'utf8')) : []
  if (!rows.length) return console.log('Nothing new to announce.')
  const feats = rows.filter((r) => r.type === 'feature')
  const fixes = rows.filter((r) => r.type === 'fix')
  // One channel failing must not keep the others quiet; the run still fails
  // at the end so it is noticed.
  await discord(feats, fixes)
  await x(feats, fixes)
  await instagram(feats, fixes)
  if (problems) process.exit(1)
}

const mode = process.argv[2]
if (mode === 'record') record()
else if (mode === 'announce') await announce()
else if (mode === 'preview') {
  // Prints what each channel would get for a range, posting nothing:
  //   BEFORE=<sha> AFTER=<sha> node publish-update.mjs preview
  const rows = commits()
  const feats = rows.filter((r) => r.type === 'feature')
  const fixes = rows.filter((r) => r.type === 'fix')
  for (const r of rows) console.log(`${r.date}  ${r.type.padEnd(7)}  ${r.title}`)
  if (rows.length) console.log('\n── X ──\n' + shortPost(feats, fixes, 245) + `\n\n${PAGE}`)
} else if (mode) {
  console.error(`unknown mode ${mode}; want record, announce or preview`)
  process.exit(2)
}
