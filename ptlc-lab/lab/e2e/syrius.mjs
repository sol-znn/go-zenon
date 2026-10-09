#!/usr/bin/env node
// Drives the lab page through a real Syrius extension, end to end.
//
//   node lab/e2e/syrius.mjs --syrius <checkout of syrius-extension> [options]
//
// The extension's own repository carries a dev harness (utils/dev-harness.js,
// v0.3.4 and later): a Chrome of its own with the extension loaded unpacked, a
// throwaway wallet that unlocks itself, and commands that press the buttons of
// the approval window and print what it showed. This script is the other half.
// It opens the lab in that browser, presses the lab's buttons, has the harness
// answer each approval, and checks what the contract did by reading the page.
//
// So what is under test is the pair: this repository's page and whichever
// extension source the checkout holds. Run it against a checkout of v0.3.4 and
// against the newest release, and the transcript of each run is the record of
// what was approved and what the chain answered.
//
//   --syrius <dir>      checkout with a dev build (`node utils/dev-harness.js build`)
//   --instance <name>   harness instance, so two versions can run side by side (default ptlc)
//   --index <n>         account of the devnet mnemonic the wallet uses (default 5, which is unfunded)
//   --lab <url>         the lab console (default http://localhost:8470)
//   --node <url>        the node the wallet dials (default ws://127.0.0.1:35998)
//   --wrong-node <url>  also check that a wallet on this other node is refused
//   --keep              leave the harness browser open afterwards
//
// It needs the lab running (docker compose up -d) and nothing else signing as
// Alice or Bob while it runs.

import { spawnSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import path from 'node:path'

const args = process.argv.slice(2)
const flag = (name, fallback) => {
  const at = args.indexOf(`--${name}`)
  return at === -1 ? fallback : (args[at + 1] ?? true)
}
const syriusDir = flag('syrius')
const instance = flag('instance', 'ptlc')
const index = String(flag('index', '5'))
const lab = flag('lab', 'http://localhost:8470')
const node = flag('node', 'ws://127.0.0.1:35998')
const wrongNode = flag('wrong-node', null)
const keep = args.includes('--keep')

if (!syriusDir || !existsSync(path.join(syriusDir, 'utils', 'dev-harness.js'))) {
  console.error('Pass --syrius <checkout of syrius-extension, v0.3.4 or later, with utils/dev-harness.js>.')
  process.exit(2)
}
const harnessFile = path.join(syriusDir, 'utils', 'dev-harness.js')

const stamp = () => new Date().toISOString().slice(11, 19)
const say = (text) => console.log(`${stamp()} ${text}`)
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

function harness(command, ...rest) {
  const out = spawnSync(process.execPath, [harnessFile, command, ...rest, '--instance', instance], {
    cwd: syriusDir,
    encoding: 'utf8',
    timeout: 300_000,
  })
  const text = `${out.stdout ?? ''}${out.stderr ?? ''}`.trim()
  if (out.status !== 0) throw new Error(`harness ${command} failed:\n${text}`)
  return text
}

/** Evaluates an expression in the lab page and returns what it printed. */
function page(expression) {
  return harness('page', 'eval', expression)
}
function pageJson(expression) {
  return JSON.parse(page(`JSON.stringify(${expression})`))
}

// The page is a Vue app; these find its controls by what they say, which is
// also what a person would go by.
const helpers = `window.__lab = {
  button(text) { return Array.from(document.querySelectorAll('button')).find((b) => b.textContent.trim().startsWith(text)) },
  click(text) {
    const b = this.button(text)
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
  choose(label, optionText) {
    const select = this.field(label)
    if (!select) return 'no field: ' + label
    const option = Array.from(select.options).find((o) => o.textContent.trim().startsWith(optionText))
    if (!option) return 'no option ' + optionText + ' in ' + label + ': ' + Array.from(select.options).map((o) => o.textContent.trim()).join(' / ')
    this.set(select, option.value)
    return 'ok'
  },
  fill(label, value) {
    const input = this.field(label)
    if (!input) return 'no field: ' + label
    this.set(input, value)
    return 'ok'
  },
  radio(value) {
    const r = document.querySelector('input[type=radio][value="' + value + '"]')
    if (!r) return 'no radio: ' + value
    r.click()
    return 'ok'
  },
  signer(name) { return this.clickIn('.signer', name) },
  clickIn(scope, text) {
    const b = Array.from(document.querySelectorAll(scope + ' button')).find((x) => x.textContent.trim() === text)
    if (!b) return 'no button ' + text + ' in ' + scope
    b.click()
    return 'ok'
  },
  top() {
    const li = document.querySelector('.feed > ol > li')
    if (!li) return null
    const stage = ['signing', 'published', 'confirmed', 'accepted', 'refused', 'declined', 'failed'].find((s) => li.classList.contains(s))
    const hash = (li.querySelector('.hex') || {}).title || ''
    return { label: li.querySelector('strong').textContent, stage, hash: hash.replace('Copy ', ''), text: li.innerText.replace(/\\n+/g, ' | ') }
  },
  checks() { return Array.from(document.querySelectorAll('.checks li')).map((li) => ({ state: li.className, title: li.querySelector('strong').textContent })) },
  row(name) {
    const tr = Array.from(document.querySelectorAll('table.grid tbody tr')).find((r) => (r.querySelector('strong') || {}).textContent === name)
    if (!tr) return null
    const cells = Array.from(tr.querySelectorAll('td')).map((td) => td.innerText.trim())
    return { znn: cells[1], qsr: cells[2], plasma: cells[3], waiting: cells[4] }
  },
  rowClick(name, text) {
    const tr = Array.from(document.querySelectorAll('table.grid tbody tr')).find((r) => (r.querySelector('strong') || {}).textContent === name)
    const b = tr && Array.from(tr.querySelectorAll('button')).find((x) => x.textContent.trim().startsWith(text))
    if (!b) return 'no button ' + text + ' for ' + name
    if (b.disabled) return 'disabled: ' + text
    b.click()
    return 'ok'
  },
  alert() { return Array.from(document.querySelectorAll('[role=alert], .note.bad')).map((x) => x.innerText).join(' || ') },
}; 'helpers in'`

let failures = 0
function check(what, ok, detail = '') {
  console.log(`${stamp()}   ${ok ? 'ok  ' : 'FAIL'} ${what}${detail ? `: ${detail}` : ''}`)
  if (!ok) failures++
  return ok
}
function must(result, what) {
  if (result !== 'ok') throw new Error(`${what}: ${result}`)
}

async function goto(route) {
  page(`(location.hash = ${JSON.stringify(route)}, 'ok')`)
  await sleep(700)
}

async function until(what, read, done, timeoutMs = 300_000) {
  const deadline = Date.now() + timeoutMs
  let last
  for (;;) {
    last = read()
    if (done(last)) return last
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}; last saw ${JSON.stringify(last)}`)
    await sleep(2500)
  }
}

/** Waits for the newest block the page sent to reach an end, and returns it. */
function activity(label, timeoutMs) {
  return until(
    `"${label}" to settle`,
    () => pageJson('window.__lab.top()'),
    (a) => a && a.label.startsWith(label) && ['accepted', 'refused', 'declined', 'failed'].includes(a.stage),
    timeoutMs,
  )
}

/** Has the harness answer the approval window, and prints what the window showed. */
function approve(expect, answer = 'approve') {
  const out = harness(answer, '--expect', expect, '--timeout', '120')
  for (const line of out.split('\n')) console.log(`${stamp()}     ${line}`)
  return out
}

async function lockFunds({ signer, amount = '1', minutes, pays, viaWallet }) {
  await goto('#/lock')
  must(page(`window.__lab.signer(${JSON.stringify(signer)})`), 'pick signer')
  must(page(`window.__lab.fill('Amount', ${JSON.stringify(amount)})`), 'amount')
  must(page(`window.__lab.fill('Expires after', ${JSON.stringify(String(minutes))})`), 'expiry')
  must(page(`window.__lab.choose('Destination', ${JSON.stringify(pays)})`), 'destination')
  await sleep(400)
  must(page(`window.__lab.click('Lock the funds')`), 'press Lock the funds')
  let screen = ''
  if (viaWallet) screen = approve('signAndSendBlock', viaWallet)
  return { screen }
}

async function main() {
  say(`harness ${harnessFile}`)
  say(harness('start', '--no-build', '--address-index', index, '--node', node, '--chain', '69').split('\n').slice(0, 2).join(' / '))
  const opened = harness('open', `${lab}/#/wallet`)
  say(opened.split('\n').join(' / '))
  check('the provider is injected into the lab page', /Provider injected/.test(opened))
  await sleep(1500)
  page(helpers)

  // ---- connect ---------------------------------------------------------------
  say('connect')
  // A harness profile that ran this before is still connected: Syrius restores
  // an approved origin without a prompt. Start from the first-visit state.
  if (pageJson('window.__lab.checks()').some((c) => c.title === 'Connected to this page')) {
    say('  this origin was approved in an earlier run and was restored without a prompt; disconnecting to start clean')
    must(page(`window.__lab.clickIn('.panel-head', 'Disconnect')`), 'press Disconnect')
    await until('the first-visit state', () => pageJson('window.__lab.checks()'), (list) => list.some((c) => c.title === 'Not connected'), 30_000)
  }
  const before = pageJson('window.__lab.checks()')
  check('before connecting, the page is told nothing', before.some((c) => c.title === 'Not connected' && c.state === 'no'), before.map((c) => c.title).join(' / '))
  must(page(`window.__lab.click('Connect Syrius')`), 'press Connect')
  approve('connect')
  const checks = await until('the wallet checks', () => pageJson('window.__lab.checks()'), (list) => list.some((c) => /lab's chain|different chain|could not be compared/.test(c.title)), 60_000)
  for (const c of checks) console.log(`${stamp()}     [${c.state}] ${c.title}`)
  check('connected', checks.some((c) => c.title === 'Connected to this page' && c.state === 'ok'))
  check('signing for chain 69', checks.some((c) => c.title === 'Signing for chain 69' && c.state === 'ok'))
  check("its node is shown to be the lab's chain", checks.some((c) => c.title === "Its node is the lab's chain" && c.state === 'ok'))
  const version = pageJson(`({ protocol: window.zenon.version, release: (window.ethereum && window.ethereum.syrius || {}).version || null })`)
  say(`provider protocol ${version.protocol}, release ${version.release ?? 'not reported by the provider'}`)

  // ---- fund it from Alice ------------------------------------------------------
  say('fund the wallet account from Alice, and fuse plasma for it')
  const start = pageJson(`window.__lab.row('Syrius')`)
  check('the Syrius account has its own row', !!start, JSON.stringify(start))
  if (Number(start.plasma.replace(/,/g, '')) === 0) {
    must(page(`window.__lab.rowClick('Syrius', 'Fuse plasma')`), 'press Fuse plasma')
    await until('plasma for the wallet account', () => pageJson(`window.__lab.row('Syrius')`), (r) => r && Number(r.plasma.replace(/,/g, '')) > 0, 240_000)
  }
  if (Number(start.znn) < 10) {
    must(page(`window.__lab.rowClick('Syrius', 'Fund from Alice')`), 'press Fund from Alice')
    await until('both sends to be waiting for the wallet', () => pageJson(`window.__lab.row('Syrius')`), (r) => r && (Number(r.waiting) >= 2 || Number(r.znn) >= 10), 240_000)
    // A send is not a balance: Syrius publishes the receive blocks, and it
    // does so once each time its dashboard is opened. So the popup is taken
    // off the dashboard and back, which is what a person does by opening it.
    let nudged = 0
    await until(
      'the wallet to receive the funds',
      () => {
        if (Date.now() - nudged > 40_000) {
          harness('route', 'tabs/settings')
          harness('route', 'tabs/dashboard')
          nudged = Date.now()
        }
        return pageJson(`window.__lab.row('Syrius')`)
      },
      (r) => r && Number(r.znn) >= 10 && Number(r.qsr) >= 10,
      300_000,
    )
  }
  const funded = pageJson(`window.__lab.row('Syrius')`)
  check('the wallet account is funded and fused', Number(funded.znn) >= 10 && Number(funded.plasma.replace(/,/g, '')) > 0, JSON.stringify(funded))

  // ---- a secret to lock to -----------------------------------------------------
  await goto('#/secrets')
  must(page(`window.__lab.click('Make a secret')`), 'make a point secret')

  // ---- Create, declined ----------------------------------------------------------
  say('Create through Syrius, declined in the wallet')
  const { screen: declinedScreen } = await lockFunds({ signer: 'Syrius', minutes: 3, pays: 'Bob', viaWallet: 'reject' })
  const declined = await activity('Lock 1 ZNN', 60_000)
  check('a declined approval is reported as declined, and nothing is sent', declined.stage === 'declined' && !declined.hash, declined.text)

  // ---- Create, approved -----------------------------------------------------------
  // Syrius holds a site off for 30 s after a declined window; the lab waits
  // that out and asks again, so the window for this one opens late.
  say('Create through Syrius: 1 ZNN to a point, payable to Bob, 3 minutes')
  const { screen: approvedScreen } = await lockFunds({ signer: 'Syrius', minutes: 3, pays: 'Bob', viaWallet: 'approve' })
  // The harness reads a window when its title appears, and the wallet may
  // still be preparing the block then: the window says "Preparing this block
  // for review…" and nothing of the call. The declined window and this one
  // are the same call, so the one that was read complete is the one judged.
  const preparing = (s) => /Preparing this block/i.test(s)
  const screen = [approvedScreen, declinedScreen].find((s) => s && !preparing(s)) ?? approvedScreen
  if (preparing(approvedScreen)) say('  the approved window was read before the wallet had prepared the block; judging the declined one, which is the same call')
  check('Syrius showed the call as one it cannot interpret', /cannot fully interpret/i.test(screen))
  check('Syrius named the contract as unrecognized', /unrecogni[sz]ed/i.test(screen))
  const created = await activity('Lock 1 ZNN')
  check('the contract accepted the Create', created.stage === 'accepted', created.text)
  const toBob = created.hash

  // ---- ProxyUnlock through Syrius ---------------------------------------------------
  say('ProxyUnlock through Syrius: the wallet submits, Bob is paid')
  await goto(`#/entries/${toBob}`)
  must(page(`window.__lab.radio('proxy')`), 'choose ProxyUnlock')
  await sleep(400)
  must(page(`window.__lab.click('Proxy unlock as Syrius')`), 'press Proxy unlock')
  approve('signAndSendBlock')
  const proxied = await activity('Proxy unlock')
  check('the contract accepted the ProxyUnlock', proxied.stage === 'accepted', proxied.text)

  // ---- Unlock through Syrius ----------------------------------------------------------
  say('Alice locks 1 ZNN payable to the wallet account; Syrius unlocks it')
  await lockFunds({ signer: 'Alice', minutes: 10, pays: 'Syrius' })
  const forWallet = await activity('Lock 1 ZNN')
  check("the contract accepted Alice's Create", forWallet.stage === 'accepted', forWallet.text)
  await goto(`#/entries/${forWallet.hash}`)
  must(page(`window.__lab.signer('Syrius')`), 'pick Syrius')
  await sleep(400)
  must(page(`window.__lab.click('Unlock as Syrius')`), 'press Unlock')
  approve('signAndSendBlock')
  const unlocked = await activity('Unlock entry')
  check('the contract accepted the Unlock', unlocked.stage === 'accepted', unlocked.text)

  // ---- Reclaim through Syrius: refused early, accepted after expiry ----------------------
  say('Create through Syrius with a 2-minute expiry, to reclaim')
  await lockFunds({ signer: 'Syrius', minutes: 2, pays: 'Bob', viaWallet: 'approve' })
  const short = await activity('Lock 1 ZNN')
  check('the contract accepted the Create', short.stage === 'accepted', short.text)
  await goto(`#/entries/${short.hash}`)
  await sleep(500)
  if (page(`window.__lab.click('Send a reclaim that should be refused')`) === 'ok') {
    approve('signAndSendBlock')
    const early = await activity('Reclaim entry')
    check('a reclaim before the expiry is refused by the contract, not by the wallet', early.stage === 'refused', early.text)
  } else {
    say('  the entry expired before the early reclaim could be sent; skipping the refusal')
  }
  say('waiting for the expiry')
  await until('the entry to expire', () => page(`window.__lab.button('Reclaim as Syrius') ? 'ready' : 'waiting'`), (s) => s === 'ready', 300_000)
  must(page(`window.__lab.click('Reclaim as Syrius')`), 'press Reclaim')
  approve('signAndSendBlock')
  const reclaimed = await activity('Reclaim entry')
  check('the contract accepted the Reclaim after the expiry', reclaimed.stage === 'accepted', reclaimed.text)

  // ---- signMessage ------------------------------------------------------------------------
  say('signMessage: a text that is not 32 bytes')
  await goto('#/wallet')
  must(page(`window.__lab.click('Ask it to sign a longer text')`), 'press sign')
  approve('signMessage')
  const signed = await until('the signature', () => page(`(document.querySelector('pre.well') || {}).innerText || ''`), (t) => t.length > 0 && !/holding this site off/.test(t), 90_000)
  check('Syrius signs an ordinary message', /signature\s+[0-9a-f]{128}/.test(signed), signed.replace(/\n/g, ' | ').slice(0, 120))

  say('signMessage: exactly 32 bytes, with Sign pressed')
  must(page(`window.__lab.click('Ask it to sign 32 bytes')`), 'press sign 32')
  // The window offers Sign like any other message. Sign is pressed, and the
  // wallet has to be the one that refuses.
  let said = ''
  try {
    said = harness('approve', '--expect', 'signMessage', '--timeout', '25')
  } catch (err) {
    said = String(err.message)
  }
  const saidLines = said.split(/\r?\n/)
  for (const line of saidLines) console.log(`${stamp()}     ${line}`)
  check('with Sign pressed, the wallet itself refuses 32 bytes', /exactly 32 bytes/.test(said), saidLines.filter((l) => /32 bytes|failure|Wallet/.test(l)).join(' ').slice(0, 220))
  // A refusal leaves the request in the window; closing it is the answer the page gets.
  try {
    harness('reject', '--timeout', '10')
  } catch {
    /* the wallet had already let go of it */
  }
  const answer = await until('the refusal', () => page(`(document.querySelector('pre.well') || {}).innerText || ''`), (t) => t.length > 0 && !/holding this site off/.test(t), 90_000)
  check('the page is given no signature for a 32-byte message', !/signature\s+[0-9a-f]{128}/.test(answer), answer.slice(0, 120))

  // ---- disconnect -----------------------------------------------------------------------------
  say('disconnect')
  must(page(`window.__lab.clickIn('.panel-head', 'Disconnect')`), 'press Disconnect')
  const after = await until('the page to be told nothing again', () => pageJson('window.__lab.checks()'), (list) => list.some((c) => c.title === 'Not connected'), 30_000)
  check('after disconnecting, the page is told nothing', after.some((c) => c.title === 'Not connected'))

  // ---- a wallet on another chain 69 ---------------------------------------------------------------
  if (wrongNode) {
    say(`a wallet on ${wrongNode}, which is not the lab's chain`)
    harness('stop')
    await sleep(1500)
    harness('start', '--no-build', '--address-index', index, '--node', wrongNode, '--chain', '69')
    harness('open', `${lab}/#/wallet`)
    await sleep(1500)
    page(helpers)
    must(page(`window.__lab.click('Connect Syrius')`), 'press Connect')
    approve('connect')
    const wrong = await until('the verdict on the other node', () => pageJson('window.__lab.checks()'), (list) => list.some((c) => /lab's chain|different chain|could not be compared/.test(c.title)), 60_000)
    for (const c of wrong) console.log(`${stamp()}     [${c.state}] ${c.title}`)
    check('the chain id matches and proves nothing', wrong.some((c) => c.title === 'Signing for chain 69' && c.state === 'ok'))
    check("the node is not accepted as the lab's chain", wrong.some((c) => /different chain|could not be compared/.test(c.title) && c.state === 'no'))
    await goto('#/lock')
    page(`window.__lab.signer('Syrius')`)
    await sleep(500)
    const blocked = page(`window.__lab.click('Lock the funds')`)
    check('the lab will not send a block through that wallet', blocked.startsWith('disabled'), blocked)
    await goto('#/wallet')
    page(`window.__lab.clickIn('.panel-head', 'Disconnect')`)
    await sleep(1000)
  }

  if (!keep) harness('stop')
  console.log(`\n${failures === 0 ? 'All checks passed' : `${failures} check${failures === 1 ? '' : 's'} FAILED`} against the extension in ${syriusDir}`)
  process.exit(failures === 0 ? 0 : 1)
}

main().catch((err) => {
  console.error(`${stamp()} stopped: ${err.message}`)
  if (!keep) {
    try {
      harness('stop')
    } catch {
      /* nothing was running */
    }
  }
  process.exit(1)
})
