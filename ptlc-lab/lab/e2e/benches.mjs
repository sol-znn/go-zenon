#!/usr/bin/env node
// Walks the lab's benches in a headless Chrome, signing with the lab's own
// keys: no wallet extension, nothing to approve.
//
//   node lab/e2e/benches.mjs [--lab http://localhost:8470] [--skip-refund] [--map "MAP ptlc.zenon.foo 127.0.0.1"]
//
// --map is Chrome's --host-resolver-rules, and with it certificate errors are
// ignored. It is how the page is walked as the public name before that name
// resolves, or without leaving the machine. Plain HTTP will not do for it:
// .foo is on the HSTS preload list, so Chrome speaks only TLS to the name. A
// throwaway TLS front on the gateway, with a certificate nobody trusts, is
// enough (gateway/README.md has the command), and then
// --lab https://ptlc.zenon.foo:8472 with the rule above gets the page labd
// gives a stranger.
//
// It covers what syrius.mjs does not: the swap bench from the first step to
// the last, the same swap abandoned and unwound by two reclaims, a key lock
// opened by a signature, the CLI run from the Programs bench, and a raw RPC
// call. Each step is made by pressing the page's own buttons and is checked
// by reading the page, so it fails where a person would have been stuck.
//
// It needs the lab running, Chrome (CHROME_PATH to say where), and nothing
// else signing as Alice or Bob while it runs. About eight minutes, most of it
// waiting for the two entries of the abandoned swap to expire.

import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'

const args = process.argv.slice(2)
const flag = (name, fallback) => {
  const at = args.indexOf(`--${name}`)
  return at === -1 ? fallback : args[at + 1]
}
const lab = flag('lab', 'http://localhost:8470')
const skipRefund = args.includes('--skip-refund')
const map = flag('map', '')

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

const profile = mkdtempSync(path.join(tmpdir(), 'ptlc-lab-benches-'))
const port = 9400 + Math.floor(Math.random() * 400)
const browser = spawn(
  chrome,
  ['--headless=new', '--disable-gpu', '--no-first-run', `--remote-debugging-port=${port}`, `--user-data-dir=${profile}`, '--window-size=1440,1000', ...(map ? [`--host-resolver-rules=${map}`, '--ignore-certificate-errors'] : []), 'about:blank'],
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

/** Evaluates an expression in the page and returns its value. */
async function page(expression) {
  const out = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true, userGesture: true })
  if (out.exceptionDetails) {
    throw new Error(out.exceptionDetails.exception?.description ?? out.exceptionDetails.text)
  }
  return out.result.value
}

// The page's controls, found by what they say.
const helpers = `window.__lab = {
  button(text, scope) { return Array.from((scope || document).querySelectorAll('button')).find((b) => b.textContent.trim().startsWith(text)) },
  click(text, scopeSelector) {
    const b = this.button(text, scopeSelector ? document.querySelector(scopeSelector) : null)
    if (!b) return 'no button: ' + text
    if (b.disabled) return 'disabled: ' + text
    b.click()
    return 'ok'
  },
  set(el, value) {
    const proto = el.tagName === 'SELECT' ? HTMLSelectElement.prototype : el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, value)
    el.dispatchEvent(new Event(el.tagName === 'SELECT' ? 'change' : 'input', { bubbles: true }))
  },
  field(label) {
    const l = Array.from(document.querySelectorAll('label.field')).find((x) => (x.querySelector('span') || {}).textContent.trim().startsWith(label))
    return l ? l.querySelector('input,select,textarea') : null
  },
  fill(label, value) { const el = this.field(label); if (!el) return 'no field: ' + label; this.set(el, value); return 'ok' },
  choose(label, optionText) {
    const select = this.field(label)
    if (!select) return 'no field: ' + label
    const option = Array.from(select.options).find((o) => o.textContent.trim().startsWith(optionText))
    if (!option) return 'no option ' + optionText + ' in ' + label
    this.set(select, option.value)
    return 'ok'
  },
  radio(name, value) { const r = document.querySelector('input[type=radio][name="' + name + '"][value="' + value + '"]'); if (!r) return 'no radio'; r.click(); return 'ok' },
  signer(name) { const b = Array.from(document.querySelectorAll('.signer button')).find((x) => x.textContent.trim() === name); if (!b) return 'no signer ' + name; b.click(); return 'ok' },
  steps() { return Array.from(document.querySelectorAll('.main ol.steps > li')).map((li) => ['done', 'now', 'later'].find((s) => li.classList.contains(s))) },
  top() {
    const li = document.querySelector('.feed > ol > li')
    if (!li) return null
    const hex = li.querySelector('.hex')
    return { label: li.querySelector('strong').textContent, stage: ['signing', 'published', 'confirmed', 'accepted', 'refused', 'declined', 'failed'].find((s) => li.classList.contains(s)), hash: hex ? hex.title.replace('Copy ', '') : '', text: li.innerText.replace(/\\n+/g, ' | ') }
  },
  alert() { return Array.from(document.querySelectorAll('.main [role=alert]')).map((x) => x.innerText).join(' || ') },
  text(selector) { const el = document.querySelector(selector); return el ? el.innerText : '' },
}; 'ok'`

function must(result, what) {
  if (result !== 'ok') throw new Error(`${what}: ${result}`)
}

async function goto(route) {
  await page(`(location.hash = ${JSON.stringify(route)}, 'ok')`)
  await sleep(600)
}

async function until(what, read, done, timeoutMs = 240_000) {
  const deadline = Date.now() + timeoutMs
  let last
  for (;;) {
    last = await read()
    if (done(last)) return last
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}; last saw ${JSON.stringify(last)}`)
    await sleep(2000)
  }
}

/** Presses a button once it is there and enabled. */
async function press(text, timeoutMs = 240_000) {
  await until(`the button "${text}"`, () => page(`window.__lab.click(${JSON.stringify(text)})`), (r) => r === 'ok', timeoutMs)
}

function settled(label, timeoutMs) {
  return until(
    `"${label}" to settle`,
    () => page('window.__lab.top()'),
    (a) => a && a.label.startsWith(label) && ['accepted', 'refused', 'declined', 'failed'].includes(a.stage),
    timeoutMs,
  )
}

// Alice holds t, so she locks first and her entry outlasts Bob's; Bob locks
// only once his six checks of hers pass, the last that it outlasts his.
async function startSwap(aliceMinutes, bobMinutes) {
  await goto('#/swap')
  if ((await page(`window.__lab.click('Start over')`)) === 'ok') await sleep(300)
  must(await page(`window.__lab.fill("Alice's entry expires", ${JSON.stringify(String(aliceMinutes))})`), 'Alice expiry')
  must(await page(`window.__lab.fill("Bob's entry expires", ${JSON.stringify(String(bobMinutes))})`), 'Bob expiry')
  await press('Make t and d', 10_000)
  await press('Lock 100 QSR as Alice', 20_000)
  check("Alice's Create accepted", (await settled('Swap: Alice locks')).stage === 'accepted')
  await press('Lock 10 ZNN as Bob', 60_000)
  check("Bob's Create accepted, after his six checks of Alice's entry passed", (await settled('Swap: Bob locks')).stage === 'accepted')
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
  await send('Page.navigate', { url: `${lab}/#/` })
  await until('the page', () => page(`document.readyState === 'complete' && !!document.querySelector('.clock .height')`).catch(() => false), Boolean, 30_000)
  await page(helpers)

  // ---- what is running ---------------------------------------------------------
  say('start here')
  const running = await until('the status panel', () => page(`window.__lab.text('.main .panel')`), (t) => /spork active/.test(t), 30_000)
  check('the node answers and the spork is active', /answering/.test(running) && /spork active/.test(running))
  check('the RPC node has all four peers', /4 of 4/.test(running), (running.match(/\d of 4/) || ['not shown'])[0])

  // ---- the swap, to the end ----------------------------------------------------
  say('swap, step by step: Alice and Bob as lab keys, expiries 12 and 6 minutes')
  await startSwap(12, 6)
  await press('Unlock as Alice, showing t', 30_000)
  const waitingNote = await until('the step to say it is waiting', () => page(`window.__lab.text('.main ol.steps > li.now .note')`), (t) => /Published/.test(t), 15_000)
  check('while her unlock is on its way the button is gone, so it cannot be sent twice', /Published/.test(waitingNote) && (await page(`window.__lab.click('Unlock as Alice, showing t')`)) !== 'ok')
  check("Alice's Unlock accepted", (await settled('Swap: Alice unlocks')).stage === 'accepted')
  await until('step 5 to open', () => page('window.__lab.steps()'), (s) => s[3] === 'done' && s[4] === 'now', 60_000)
  const revealed = await page(`window.__lab.text('.main ol.steps > li.now')`)
  check('Bob reads t off the chain and it is the scalar of T', /t·G is T/.test(revealed))
  await press('Unlock as Bob with t + d', 30_000)
  check("Bob's Unlock with t + d accepted", (await settled('Swap: Bob unlocks')).stage === 'accepted')
  const done = await until('the swap to read as done', () => page(`window.__lab.text('.main')`), (t) => /Swapped/.test(t), 60_000)
  check('both entries are paid and the bench says so', /Swapped/.test(done) && (await page('window.__lab.steps()')).every((s) => s === 'done'))

  // ---- a key lock, opened by a signature ----------------------------------------
  say('a BIP-340 key lock with an open destination, opened by Bob with a signature')
  await goto('#/secrets')
  must(await page(`window.__lab.radio('kind', '1')`), 'choose the BIP-340 kind')
  await press('Make a secret', 10_000)
  await goto('#/lock')
  must(await page(`window.__lab.signer('Alice')`), 'sign as Alice')
  must(await page(`window.__lab.radio('pointType', '1')`), 'point type 1')
  await sleep(300)
  must(await page(`window.__lab.fill('Expires after', '10')`), 'expiry')
  must(await page(`window.__lab.choose('Destination', 'any address')`), 'open destination')
  await press('Lock the funds as Alice', 20_000)
  const keyLock = await settled('Lock 1 ZNN to a BIP-340 key')
  check('Create with a BIP-340 lock and no fixed destination accepted', keyLock.stage === 'accepted', keyLock.text.slice(0, 80))
  await goto(`#/entries/${keyLock.hash}`)
  must(await page(`window.__lab.signer('Bob')`), 'sign as Bob')
  await sleep(500)
  const verdicts = await page(`window.__lab.text('.verdicts')`)
  check('the bench predicts the unlock will be accepted', /signature verifies/.test(verdicts) && /leaves the destination open/.test(verdicts))
  await press('Unlock as Bob', 20_000)
  check('Unlock by signature accepted: Bob was not named by the entry, the signature named him', (await settled('Unlock entry')).stage === 'accepted')

  // ---- a refusal on purpose ------------------------------------------------------
  say('a wrong witness, sent anyway')
  await goto('#/lock')
  must(await page(`window.__lab.signer('Alice')`), 'sign as Alice')
  must(await page(`window.__lab.radio('pointType', '2')`), 'point type 2')
  await sleep(300)
  must(await page(`window.__lab.choose('Lock to', 'a lock someone gave me')`), 'paste a lock')
  await sleep(200)
  // The generator point: its scalar is 1, which this test then deliberately does not use.
  must(await page(`window.__lab.fill('The lock,', '0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798')`), 'the lock')
  must(await page(`window.__lab.fill('Expires after', '10')`), 'expiry')
  must(await page(`window.__lab.choose('Destination', 'Bob')`), 'destination')
  await press('Lock the funds as Alice', 20_000)
  const pointLock = await settled('Lock 1 ZNN to a secp256k1 point')
  check('Create to a pasted point accepted', pointLock.stage === 'accepted')
  await goto(`#/entries/${pointLock.hash}`)
  must(await page(`window.__lab.signer('Bob')`), 'sign as Bob')
  await sleep(400)
  must(await page(`window.__lab.fill('The witness, in hex', '${'00'.repeat(31)}02')`), 'a wrong scalar')
  await sleep(300)
  check('the bench predicts the refusal', /should refuse/.test(await page(`window.__lab.text('.main')`)))
  await press('Unlock as Bob', 20_000)
  const refused = await settled('Unlock entry')
  check('the contract refuses the wrong scalar and the page says why', refused.stage === 'refused' && /witness does not open/.test(refused.text), refused.text.slice(-120))
  must(await page(`window.__lab.fill('The witness, in hex', '${'00'.repeat(31)}01')`), 'the right scalar')
  await press('Unlock as Bob', 30_000)
  check('the right scalar, 1, opens it', (await settled('Unlock entry')).stage === 'accepted')

  // ---- the CLI, from the page ------------------------------------------------------
  say('znn-cli from the Programs bench')
  await goto('#/programs')
  must(await page(`window.__lab.fill('Command', 'ptlc.point 0000000000000000000000000000000000000000000000000000000000000001')`), 'command')
  await press('Run it', 10_000)
  const output = await until('the CLI output', () => page(`window.__lab.text('.run .out')`), (t) => /Point/.test(t), 60_000)
  check('the CLI ran in the lab container and its output streamed to the page', /0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798/.test(output))

  // ---- raw RPC ---------------------------------------------------------------------
  say('raw RPC')
  await goto('#/rpc')
  must(await page(`window.__lab.fill('Method', 'embedded.ptlc.getById')`), 'method')
  must(await page(`window.__lab.fill('Params', ${JSON.stringify(JSON.stringify([pointLock.hash]))})`), 'params')
  await press('Call the node', 10_000)
  const answer = await until('the answer', () => page(`window.__lab.text('pre.answer')`), (t) => t.length > 0, 20_000)
  check('getById on an entry that was paid says only "data non existent"', /data non existent/.test(answer), answer.slice(0, 60))

  // ---- the swap, abandoned ---------------------------------------------------------
  if (!skipRefund) {
    say('the swap again, abandoned after both have locked: expiries 6 and 2 minutes')
    await startSwap(6, 2)
    await press('Walk away instead', 30_000)
    await sleep(500)
    await press('Try to reclaim early', 20_000)
    const early = await settled('Swap: Bob reclaims')
    check('a reclaim before the expiry is refused', early.stage === 'refused' && /not expired/.test(early.text))
    say('waiting for Bob\'s entry to expire')
    await press('Reclaim as Bob', 300_000)
    check("Bob's Reclaim accepted after his expiry", (await settled('Swap: Bob reclaims')).stage === 'accepted')
    say('waiting for Alice\'s entry to expire')
    await press('Reclaim as Alice', 400_000)
    check("Alice's Reclaim accepted after hers, which is later because she holds the secret", (await settled('Swap: Alice reclaims')).stage === 'accepted')
    const unwound = await until('the bench to read as unwound', () => page(`window.__lab.text('.main')`), (t) => /Unwound/.test(t), 60_000)
    check('both entries are returned and the bench says so', /Unwound/.test(unwound))
  }

  console.log(`\n${failures === 0 ? 'All checks passed' : `${failures} check${failures === 1 ? '' : 's'} FAILED`}`)
}

let code = 0
try {
  await main()
  code = failures === 0 ? 0 : 1
} catch (err) {
  console.error(`${stamp()} stopped: ${err.message}`)
  try {
    console.error(`${stamp()} the page says: ${await page('window.__lab.alert()')}`)
  } catch {
    /* the page is gone */
  }
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
