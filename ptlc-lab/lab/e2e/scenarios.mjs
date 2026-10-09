#!/usr/bin/env node
// Runs every scenario on the scenarios page the way a person would: opens the
// page in a headless Chrome, presses Run on a card, waits for the card to say
// Passed or Failed, and reads the flow back off the page.
//
//   node lab/e2e/scenarios.mjs [--lab http://localhost:8470] [--only secret,swap] [--map "MAP ptlc.zenon.foo 127.0.0.1"] [--shots <dir>]
//
// Nothing is signed here or in the page. The button names a scenario and the
// lab's back end signs every step, so what this checks is the whole path: the
// page, labd, the flows program, the node and the contract, and for the two
// Bitcoin scenarios bitcoind as well.
//
// A scenario passes here when the page says it passed and every step on the
// card is settled. The checks of what the contract did are the runner's own
// (scenarios/nom-to-btc/cmd/flows); this script does not repeat them, it makes
// sure the page shows them.
//
// It needs the lab running, Chrome (CHROME_PATH to say where), and nothing
// else signing as Alice or Bob while it runs. About 40 minutes for all
// twenty-one, most of it the four scenarios that wait out a deadline. --shots
// saves a picture of each card when it finishes. --map is Chrome's
// --host-resolver-rules, as in benches.mjs.

import { spawn } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

const args = process.argv.slice(2)
const flag = (name, fallback) => {
  const at = args.indexOf(`--${name}`)
  return at === -1 ? fallback : args[at + 1]
}
const lab = flag('lab', 'http://localhost:8470')
const only = flag('only', '')
const map = flag('map', '')
const shots = flag('shots', '')

const chrome = [
  process.env.CHROME_PATH,
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
].find((candidate) => candidate && existsSync(candidate))
if (!chrome) {
  console.error('No Chrome found. Point CHROME_PATH at one.')
  process.exit(2)
}

const stamp = () => new Date().toISOString().slice(11, 19)
const say = (text) => console.log(`${stamp()} ${text}`)
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

let failures = 0
function check(what, ok, detail = '') {
  console.log(`${stamp()}   ${ok ? 'ok  ' : 'FAIL'} ${what}${detail ? `: ${detail}` : ''}`)
  if (!ok) failures++
  return ok
}

// ---- a browser ---------------------------------------------------------------

const profile = mkdtempSync(path.join(tmpdir(), 'ptlc-lab-scenarios-'))
const port = 9400 + Math.floor(Math.random() * 400)
const browser = spawn(
  chrome,
  ['--headless=new', '--disable-gpu', '--no-first-run', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, '--window-size=1440,1100', ...(map ? [`--host-resolver-rules=${map}`, '--ignore-certificate-errors'] : []), 'about:blank'],
  { stdio: 'ignore' },
)

async function connect() {
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json()
      const tab = targets.find((t) => t.type === 'page')
      if (tab) return new WebSocket(tab.webSocketDebuggerUrl)
    } catch {
      /* not up yet */
    }
    await sleep(250)
  }
  throw new Error('Chrome did not come up')
}

let socket
let nextId = 1
const pending = new Map()

function send(method, params = {}) {
  const id = nextId++
  socket.send(JSON.stringify({ id, method, params }))
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }))
}

async function page(expression) {
  const out = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true, userGesture: true })
  if (out.exceptionDetails) throw new Error(out.exceptionDetails.exception?.description ?? out.exceptionDetails.text)
  return out.result.value
}

// The page's cards, found by their titles, and read as a person reads them.
const helpers = `window.__s = {
  card(title) { return Array.from(document.querySelectorAll('article.card')).find((c) => c.querySelector('h3').textContent.trim() === title) },
  titles() { return Array.from(document.querySelectorAll('article.card h3')).map((h) => h.textContent.trim()) },
  run(title) {
    const card = this.card(title)
    if (!card) return 'no card: ' + title
    const b = card.querySelector('.card-foot .btn-primary')
    if (!b) return 'no Run button'
    if (b.disabled) return 'held: ' + card.querySelector('.eta').textContent.trim()
    b.click()
    return 'ok'
  },
  read(title) {
    const card = this.card(title)
    if (!card) return null
    const state = ['running', 'pass', 'fail'].find((s) => card.classList.contains(s)) || 'none'
    return {
      state,
      badge: card.querySelector('.badge').textContent.trim(),
      open: card.classList.contains('open'),
      error: (card.querySelector('.alert') || {}).textContent || '',
      verdict: ((card.querySelector('.verdict') || {}).innerText || '').replace(/\\n+/g, ' | '),
      steps: Array.from(card.querySelectorAll('.steps > li')).map((li) => ({
        state: ['pending', 'active', 'sent', 'confirmed', 'done', 'failed'].find((s) => li.classList.contains(s)),
        answer: ['accepted', 'refused', 'rejected'].find((s) => li.classList.contains(s)) || '',
        label: li.querySelector('.label').childNodes[0].textContent.trim(),
        detail: (li.querySelector('.detail') || {}).textContent || '',
      })),
    }
  },
  tally() { return Array.from(document.querySelectorAll('.tally dd')).map((d) => d.textContent.trim()).join(' / ') },
  // The newest run of a scenario the lab knows, by its number; 0 for none.
  async lastRun(flow) {
    const list = await (await fetch('/api/runs')).json()
    return Math.max(0, ...list.runs.filter((r) => r.flow === flow).map((r) => Number(r.id)))
  },
  box(title) { const r = this.card(title).getBoundingClientRect(); return { x: r.x, y: r.y + window.scrollY, width: r.width, height: r.height } },
}; 'ok'`

async function until(what, read, done, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  let last
  for (;;) {
    last = await read()
    if (done(last)) return last
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}; last saw ${JSON.stringify(last)}`)
    await sleep(1500)
  }
}

async function picture(title, name) {
  if (!shots) return
  const box = await page(`window.__s.box(${JSON.stringify(title)})`)
  const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true, clip: { ...box, x: Math.max(0, box.x - 12), y: Math.max(0, box.y - 12), width: box.width + 24, height: box.height + 24, scale: 1 } })
  mkdirSync(shots, { recursive: true })
  writeFileSync(path.join(shots, `${name}.png`), Buffer.from(shot.data, 'base64'))
}

async function main() {
  socket = await connect()
  await new Promise((resolve, reject) => {
    socket.onopen = resolve
    socket.onerror = () => reject(new Error('could not open the debugging socket'))
  })
  socket.onmessage = (event) => {
    const message = JSON.parse(event.data)
    const waiting = pending.get(message.id)
    if (!waiting) return
    pending.delete(message.id)
    if (message.error) waiting.reject(new Error(message.error.message))
    else waiting.resolve(message.result)
  }
  await send('Page.enable')
  await send('Page.navigate', { url: `${lab}/scenarios/` })
  await until('the cards', () => page(`document.querySelectorAll('article.card').length`).catch(() => 0), (n) => n > 0, 30_000)
  await page(helpers)

  // The catalogue is asked of the lab, not written here: a scenario added to
  // the runner is walked without this file knowing its name.
  const catalogue = await page(`fetch('/api/flows').then((r) => r.json())`)
  const wanted = only ? only.split(',') : catalogue.scenarios.map((s) => s.id)
  const list = catalogue.scenarios.filter((s) => wanted.includes(s.id))
  const shown = await page('window.__s.titles()')
  check(`the page shows a card for each of the lab's ${catalogue.scenarios.length} scenarios`, catalogue.scenarios.every((s) => shown.includes(s.title)), `${shown.length} cards`)
  const chain = await until('the chain to be read', () => page(`document.querySelector('.chain').innerText`), (t) => /block/.test(t), 20_000)
  check('the page shows the chain is live', /Chain live/.test(chain), chain.replace(/\s+/g, ' '))

  for (const sc of list) {
    say(`${sc.title} (${sc.id}, ${sc.steps.length} steps)`)
    // Another scenario may hold the accounts: this one's own predecessor is
    // over by now, but the lab is shared.
    const before = await page(`window.__s.lastRun(${JSON.stringify(sc.id)})`)
    await until('the Run button to be free', () => page(`window.__s.run(${JSON.stringify(sc.title)})`), (r) => r === 'ok' || !String(r).startsWith('held'), 600_000).then((r) => {
      if (r !== 'ok') throw new Error(`could not press Run: ${r}`)
    })
    const started = Date.now()
    // This press's run, not merely "has a result": a card that was run before
    // still shows that result until the lab answers with the new run, and
    // reading it then would report the old run as this one. The lab numbering
    // a newer run of this scenario is the sign; the card's running state alone
    // was, until a scenario (swap-btc-bad-terms) finished in one second, before
    // the first look.
    await until('the lab to start the run', () => page(`window.__s.lastRun(${JSON.stringify(sc.id)})`), (n) => n > before, 20_000)
    const running = await until('the card to show the run', () => page(`window.__s.read(${JSON.stringify(sc.title)})`), (c) => c.open && c.state !== 'none', 20_000)

    if (running.error) say(`  the page says: ${running.error}`)
    const end = await until('the result', () => page(`window.__s.read(${JSON.stringify(sc.title)})`), (c) => c.state === 'pass' || c.state === 'fail', (sc.seconds * 3 + 180) * 1000)
    // The page shows a live run's lines a beat apart; give the last ones time
    // to land before reading the steps.
    await sleep(2500)
    const card = await page(`window.__s.read(${JSON.stringify(sc.title)})`)
    for (const step of card.steps) {
      console.log(`${stamp()}     [${step.state}${step.answer ? `, ${step.answer}` : ''}] ${step.label}${step.detail ? `: ${step.detail}` : ''}`)
    }
    check(`${sc.id}: the page says it passed`, end.state === 'pass', `${card.verdict || card.badge}; ${Math.round((Date.now() - started) / 1000)} s here`)
    check(`${sc.id}: all ${sc.steps.length} steps are drawn settled`, card.steps.length === sc.steps.length && card.steps.every((s) => s.state === 'done'), card.steps.map((s) => s.state).join(' '))
    const expected = sc.steps.filter((s) => s.kind === 'call').map((s) => s.expect)
    const drawn = card.steps.filter((_, i) => sc.steps[i].kind === 'call').map((s) => s.answer)
    check(`${sc.id}: each call is drawn with the outcome it was meant to meet`, JSON.stringify(expected) === JSON.stringify(drawn), drawn.join(' '))
    await picture(sc.title, sc.id)
  }

  say(`tally on the page (passed / failed / not run): ${await page('window.__s.tally()')}`)

  // A reload must show the same results: they are the lab's, not this tab's.
  await send('Page.navigate', { url: `${lab}/scenarios/` })
  await until('the cards again', () => page(`document.querySelectorAll('article.card').length`).catch(() => 0), (n) => n > 0, 30_000)
  await page(helpers)
  const last = list[list.length - 1]
  const again = await until('the results to be read back', () => page(`window.__s.read(${JSON.stringify(last.title)})`), (c) => c && c.state !== 'none', 30_000)
  check('after a reload the last result is still shown, read back from the lab', again.state === 'pass' || again.state === 'fail', again.badge)

  console.log(`\n${failures === 0 ? 'All checks passed' : `${failures} check${failures === 1 ? '' : 's'} FAILED`}`)
}

let code = 0
try {
  await main()
  code = failures === 0 ? 0 : 1
} catch (err) {
  console.error(`${stamp()} stopped: ${err.message}`)
  code = 1
} finally {
  try {
    socket?.close()
  } catch {
    /* already closed */
  }
  browser.kill()
  await sleep(500)
  try {
    rmSync(profile, { recursive: true, force: true })
  } catch {
    /* Chrome may still hold a file; the directory is in the system's temp */
  }
}
process.exit(code)
