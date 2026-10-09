// The lab's state, and the one place blocks leave the page from.
//
// Everything the benches show is derived from three sources that are polled
// here and nowhere else: the frontier momentum, the contract's account chain,
// and the wallet extension's session. A bench never talks to the node to learn
// what happened to something it sent; it sends through `submit` and reads the
// resulting activity, which this module advances as the chain does.

import { markRaw, reactive } from 'vue'

import * as labd from './api.ts'
import type { LabConfig, RunSummary } from './api.ts'
import { PTLC_CONTRACT, PTLC_SPORK_ID, type MethodName } from './ptlc/contract.ts'
import { lockOf } from './ptlc/curve.ts'
import { emptyScan, entriesForView, foldBlocks, type ContractEvent, type Entry } from './ptlc/scan.ts'
import { encodeCall } from './znn/abi.ts'
import { BLOCK_TYPE_USER_RECEIVE, blockTemplate, publishBlock, type BlockJson, type BlockSpec } from './znn/block.ts'
import { ZenonClient, delay, type Momentum, type RpcLogEntry, type TokenInfo } from './znn/client.ts'
import {
  EMPTY_ADDRESS,
  PLASMA_CONTRACT,
  QSR_ZTS,
  ZNN_ZTS,
  fromHex,
  toHex,
} from './znn/primitives.ts'
import { Account } from './znn/wallet.ts'
import * as syrius from './wallet/syrius.ts'
import type { SameChain, WalletState } from './wallet/syrius.ts'

/**
 * The mnemonic the go-zenon branch commits for its devnet
 * (docker/devnet/README.md). It funds nothing anywhere else.
 */
export const DEV_MNEMONIC =
  'abstract affair idle position alien fluid board ordinary exist afraid chapter wood wood guide sun walnut crew perfect place firm poverty model side million'

/** Logged: what a bench asked for. */
export const client = new ZenonClient('/rpc')
/** Not logged: the background polling, which would bury everything else. */
const poller = new ZenonClient('/rpc')

// ---- signers -------------------------------------------------------------------

export type SignerId = 'lab:alice' | 'lab:bob' | 'lab:carol' | 'syrius'

export interface LabKey {
  id: SignerId
  name: string
  what: string
  account: Account
}

function carolSeed(): Uint8Array {
  const key = 'ptlc-lab.carol.v1'
  try {
    const stored = localStorage.getItem(key)
    if (stored && /^[0-9a-f]{64}$/.test(stored)) return fromHex(stored)
    const fresh = crypto.getRandomValues(new Uint8Array(32))
    localStorage.setItem(key, toHex(fresh))
    return fresh
  } catch {
    return crypto.getRandomValues(new Uint8Array(32))
  }
}

export const labKeys: readonly LabKey[] = [
  {
    id: 'lab:alice',
    name: 'Alice',
    what: 'devnet mnemonic, index 1. Funded and fused in genesis.',
    account: markRaw(Account.fromMnemonic(DEV_MNEMONIC, 1)),
  },
  {
    id: 'lab:bob',
    name: 'Bob',
    what: 'devnet mnemonic, index 3. Funded and fused in genesis.',
    account: markRaw(Account.fromMnemonic(DEV_MNEMONIC, 3)),
  },
  {
    id: 'lab:carol',
    name: 'Carol',
    what: 'a key this browser made. Holds nothing until Alice funds it: the stranger who submits a proxy unlock.',
    account: markRaw(Account.fromSeed(carolSeed())),
  },
]

export function labKey(id: SignerId): LabKey | undefined {
  return labKeys.find((k) => k.id === id)
}

// ---- state ---------------------------------------------------------------------

export interface AccountView {
  address: string
  height: number
  balances: Record<string, string>
  plasma: number
  maxPlasma: number
  unreceived: number
}

export interface Secret {
  id: string
  name: string
  pointType: number
  /** Hex, 32 bytes for every type. */
  secret: string
  /** Hex: the lock this secret opens. */
  lock: string
  createdAt: number
  origin: 'generated' | 'imported' | 'derived' | 'read from chain'
}

export type Stage = 'signing' | 'published' | 'confirmed' | 'accepted' | 'refused' | 'declined' | 'failed'

export interface Activity {
  n: number
  label: string
  /** A contract method, or what kind of plain block this is. */
  kind: MethodName | 'Send' | 'Receive' | 'Fuse'
  signer: SignerId
  signerAddress: string
  stage: Stage
  hash?: string
  error?: string
  /** Something true and worth reading that is not an error. */
  note?: string
  started: number
  published?: number
  confirmed?: number
  settled?: number
  /** The momentum that confirmed the send, and the one the contract answered in. */
  sendMomentum?: number
  answerMomentum?: number
  /** Their timestamps. The page's own clock is no measure: a tab in the background is told the time late. */
  sendTimestamp?: number
  answerTimestamp?: number
}

interface WalletView extends WalletState {
  sameChain: SameChain | null
  sameChainDetail: string
  busy: boolean
  error: string
}

export const state = reactive({
  config: null as LabConfig | null,
  labdError: '',

  momentum: null as Momentum | null,
  /** Wall-clock milliseconds when the frontier last moved. */
  momentumSeenAt: 0,
  /** Seconds each of the last momentums took, newest last. */
  slots: [] as number[],
  nodeError: '',
  /** Whether the ptlc spork is active on this chain; null until read. */
  spork: null as boolean | null,
  /** How many peers the RPC node has; null until read. The devnet has four others. */
  peers: null as number | null,

  scanHeight: 0,
  events: [] as ContractEvent[],
  entries: [] as Entry[],

  signer: 'lab:alice' as SignerId,
  wallet: {
    present: false,
    protocol: null,
    extensionVersion: null,
    address: null,
    chainId: null,
    nodeUrl: null,
    canSignMessage: false,
    sameChain: null,
    sameChainDetail: '',
    busy: false,
    error: '',
  } as WalletView,

  accounts: {} as Record<string, AccountView>,
  tokens: {} as Record<string, TokenInfo>,
  secrets: [] as Secret[],
  activity: [] as Activity[],
  rpcLog: [] as RpcLogEntry[],

  runs: [] as RunSummary[],
  /** A scenario that is running holds both dev accounts. */
  busyRun: null as { id: string; title: string } | null,

  btcHeight: null as number | null,
  btcError: '',

  /** Ticks once a second, for countdowns. */
  now: Date.now(),
})

export function chainId(): number {
  return state.momentum?.chainIdentifier ?? Number(state.config?.chainId ?? 69)
}

/**
 * The frontier momentum's timestamp. This, not the wall clock, is what the
 * contract compares an expiry with, and on this host it can trail the wall
 * clock by a missed slot or two.
 */
export function chainNow(): number {
  if (!state.momentum) return Math.floor(state.now / 1000)
  return state.momentum.timestamp
}

// ---- names and amounts -----------------------------------------------------------

export function nameOf(address: string | null | undefined): string | null {
  if (!address) return null
  if (address === PTLC_CONTRACT) return 'ptlc contract'
  if (address === PLASMA_CONTRACT) return 'plasma contract'
  if (address === EMPTY_ADDRESS) return 'any address'
  const key = labKeys.find((k) => k.account.address === address)
  const names: string[] = []
  if (key) names.push(key.name)
  if (state.wallet.address === address) names.push('Syrius')
  return names.length ? names.join(' / ') : null
}

const KNOWN_TOKENS: Record<string, TokenInfo> = {
  [ZNN_ZTS]: { name: 'ZNN', symbol: 'ZNN', decimals: 8, tokenStandard: ZNN_ZTS },
  [QSR_ZTS]: { name: 'QSR', symbol: 'QSR', decimals: 8, tokenStandard: QSR_ZTS },
}

export function tokenOf(zts: string): TokenInfo | null {
  return KNOWN_TOKENS[zts] ?? state.tokens[zts] ?? null
}

export async function loadToken(zts: string): Promise<TokenInfo | null> {
  const known = tokenOf(zts)
  if (known) return known
  try {
    const token = await poller.call<TokenInfo | null>('embedded.token.getByZts', [zts])
    if (token) state.tokens[zts] = token
    return token
  } catch {
    return null
  }
}

/** Base units as a decimal number of tokens, trailing zeros dropped. */
export function formatUnits(amount: string | bigint, decimals: number): string {
  const value = BigInt(amount)
  if (decimals === 0) return value.toString()
  const base = 10n ** BigInt(decimals)
  const whole = value / base
  const fraction = (value % base).toString().padStart(decimals, '0').replace(/0+$/, '')
  return fraction ? `${whole}.${fraction}` : whole.toString()
}

export function formatAmount(amount: string | bigint, zts: string): string {
  const token = tokenOf(zts)
  if (!token) return `${BigInt(amount)} base units of ${zts}`
  return `${formatUnits(amount, token.decimals)} ${token.symbol}`
}

/** A decimal number of tokens as base units, or null when it is not one. */
export function parseUnits(text: string, decimals: number): bigint | null {
  const clean = text.trim()
  if (!/^\d+(\.\d+)?$/.test(clean)) return null
  const [whole, fraction = ''] = clean.split('.')
  if (fraction.length > decimals) return null
  return BigInt(whole) * 10n ** BigInt(decimals) + BigInt(fraction.padEnd(decimals, '0') || '0')
}

// ---- signers, as the benches see them -------------------------------------------

export interface SignerView {
  id: SignerId
  kind: 'lab key' | 'wallet extension'
  name: string
  address: string | null
  /** Why this signer cannot be used right now, or null. */
  blocked: string | null
}

export function signerView(id: SignerId): SignerView {
  if (id !== 'syrius') {
    const key = labKey(id)!
    return {
      id,
      kind: 'lab key',
      name: key.name,
      address: key.account.address,
      blocked: state.busyRun && id !== 'lab:carol' ? `${state.busyRun.title} is running and owns this account` : null,
    }
  }
  const w = state.wallet
  let blocked: string | null = null
  if (!w.present) blocked = 'the Syrius extension is not in this browser'
  else if (!w.address) blocked = 'Syrius is not connected to this page'
  else if (w.chainId !== null && w.chainId !== chainId()) {
    blocked = `Syrius is on chain ${w.chainId}; the lab's node is chain ${chainId()}`
  } else if (w.sameChain === 'different') blocked = "Syrius's node is a different chain from the lab's"
  // Not proven is treated as not the same. Every go-zenon devnet is chain 69,
  // Syrius 0.4.0 ships one as a preset, and a Create published to the wrong
  // one sends funds to an address no contract answers at.
  else if (w.sameChain !== 'same') blocked = "Syrius's node has not been shown to be the lab's chain"
  else if (state.busyRun && labKeys.some((k) => k.id !== 'lab:carol' && k.account.address === w.address)) {
    blocked = `${state.busyRun.title} is running and owns this account`
  }
  return { id, kind: 'wallet extension', name: 'Syrius', address: w.address, blocked }
}

export const signerIds: readonly SignerId[] = ['lab:alice', 'lab:bob', 'lab:carol', 'syrius']

export function setSigner(id: SignerId): void {
  state.signer = id
  try {
    localStorage.setItem('ptlc-lab.signer', id)
  } catch {
    /* the choice just will not be remembered */
  }
}

// ---- sending -------------------------------------------------------------------

// An account chain takes one block at a time, and the next block names the
// previous one as its parent, which the node only reports once a momentum has
// confirmed it. So each lab key has a queue, and a block waits in it until the
// one before is confirmed.
const queues = new Map<string, Promise<unknown>>()
function inTurn<T>(address: string, job: () => Promise<{ value: T; hold: Promise<unknown> }>): Promise<T> {
  const before = queues.get(address) ?? Promise.resolve()
  const running = before.then(job, job)
  // The caller gets its value when the block is published; the queue stays
  // taken until `hold` settles.
  queues.set(
    address,
    running.then(
      (r) => r.hold.catch(() => undefined),
      () => undefined,
    ),
  )
  return running.then((r) => r.value)
}

let activityCounter = 0

export interface SubmitOptions {
  label: string
  kind: Activity['kind']
}

/**
 * Sends one block through a signer and returns its activity as soon as the
 * block is published. The activity keeps advancing after that, as the block is
 * confirmed and, for a contract call, as the contract answers.
 *
 * Throws when nothing was published: the signer was not usable, the person
 * declined, or the node refused the block. The activity records why.
 */
export async function submit(signer: SignerId, spec: BlockSpec, options: SubmitOptions): Promise<Activity> {
  const view = signerView(signer)
  state.activity.unshift({
    n: ++activityCounter,
    label: options.label,
    kind: options.kind,
    signer,
    signerAddress: view.address ?? '',
    stage: 'signing',
    started: Date.now(),
  })
  // Read back through the reactive array, so that what is changed below is
  // what the benches are watching.
  const activity = state.activity[0]
  if (state.activity.length > 60) state.activity.length = 60

  const fail = (stage: Stage, message: string): never => {
    activity.stage = stage
    activity.error = message
    activity.settled = Date.now()
    throw new Error(message)
  }

  if (view.blocked) fail('failed', view.blocked)

  try {
    if (signer === 'syrius') {
      const template = blockTemplate(spec, chainId())
      const sent = await syrius.sendAccountBlock(template, {
        onHold: (seconds) => {
          activity.note = seconds > 0 ? `Syrius does not raise a second window for one site within 30 s of one that was declined or closed. Nothing was shown or signed; asking again in ${seconds} s.` : undefined
        },
      })
      activity.hash = sent.hash
      activity.published = Date.now()
      activity.stage = 'published'
      if (sent.block?.address) activity.signerAddress = String(sent.block.address)
      const notes: string[] = []
      const differences = syrius.signedBlockDiffers(template, sent.block)
      if (differences.length) notes.push(`Syrius signed something else than was asked. ${differences.join('; ')}`)
      if (state.wallet.address && activity.signerAddress !== state.wallet.address) {
        notes.push(`signed by ${activity.signerAddress}, not the account this page was connected to`)
      }
      if (notes.length) activity.note = notes.join('. ')
    } else {
      const key = labKey(signer)!
      await inTurn(key.account.address, async () => {
        const published = await publishBlock(client, key.account, chainId(), spec, {
          onMining: (hashes) => {
            activity.note = `no plasma fused for ${key.name}: mining proof of work, ${hashes.toLocaleString('en')} hashes so far`
          },
        })
        activity.hash = published.hash
        activity.published = Date.now()
        activity.stage = 'published'
        if (activity.note?.startsWith('no plasma')) activity.note = `${key.name} paid for this block with proof of work`
        // The queue is held until the block has a momentum, so the next block
        // from this key finds it as the frontier.
        return { value: published, hold: poller.waitForConfirmation(published.hash) }
      })
    }
  } catch (err) {
    if (activity.stage !== 'signing') throw err
    if (err instanceof syrius.WalletError && err.declined) fail('declined', err.message)
    fail('failed', err instanceof Error ? err.message : String(err))
  }
  void advanceActivities()
  return activity
}

const CONTRACT_KINDS: readonly Activity['kind'][] = ['Create', 'Unlock', 'ProxyUnlock', 'Reclaim']

/**
 * Whether a block this page sent is still on its way: signed or published,
 * and for a contract call not yet answered. A bench holds its button while
 * this is true, so that a second press does not send the call twice.
 */
export function inFlight(n: number | null | undefined): boolean {
  if (!n) return false
  const activity = state.activity.find((a) => a.n === n)
  if (!activity) return false
  if (['accepted', 'refused', 'failed', 'declined'].includes(activity.stage)) return false
  return CONTRACT_KINDS.includes(activity.kind) || activity.stage !== 'confirmed'
}

/** Resolves when an activity has reached an end: accepted, refused, confirmed for a plain block, or failed. */
export async function settled(activity: Activity, timeoutMs = 240_000): Promise<Activity> {
  const deadline = Date.now() + timeoutMs
  const isContract = CONTRACT_KINDS.includes(activity.kind)
  for (;;) {
    if (['accepted', 'refused', 'failed', 'declined'].includes(activity.stage)) return activity
    if (!isContract && activity.stage === 'confirmed') return activity
    if (Date.now() > deadline) return activity
    await delay(700)
  }
}

async function advanceActivities(): Promise<void> {
  for (const activity of state.activity) {
    if (!activity.hash) continue
    if (activity.stage === 'published') {
      try {
        const block = await poller.accountBlockByHash(activity.hash)
        if (block?.confirmationDetail?.momentumHeight) {
          activity.stage = 'confirmed'
          activity.confirmed = Date.now()
          activity.sendMomentum = block.confirmationDetail.momentumHeight
          activity.sendTimestamp = block.confirmationDetail.momentumTimestamp
          if (!CONTRACT_KINDS.includes(activity.kind)) activity.settled = activity.confirmed
        } else if (Date.now() - (activity.published ?? activity.started) > 150_000) {
          activity.stage = 'failed'
          activity.settled = Date.now()
          activity.error =
            activity.signer === 'syrius'
              ? "the lab's node never saw this block. Syrius published it to its own node, which is not this chain: point Syrius at " +
                (state.config?.walletNode ?? "the lab's node") +
                '.'
              : 'no momentum included this block within 150 s'
        }
      } catch {
        /* the next momentum tries again */
      }
    }
    if (activity.stage === 'confirmed' && CONTRACT_KINDS.includes(activity.kind)) {
      const event = scan.events.find((e) => e.sendHash === activity.hash)
      if (event) {
        activity.stage = event.ok ? 'accepted' : 'refused'
        activity.settled = Date.now()
        activity.answerMomentum = event.momentumHeight
        activity.answerTimestamp = event.timestamp
        if (!event.ok) activity.error = refusalReason(activity, event)
      }
    }
  }
}

/**
 * The contract's receive block carries only "refused", not why. The reason is
 * in the node's log. What can be told from the chain is told here.
 */
function refusalReason(activity: Activity, event: ContractEvent): string {
  const call = event.call
  const at = event.timestamp
  if (!call) return 'the contract refused the call'
  if (call.method === 'Create') {
    if (call.expirationTime <= at) return 'refused: the expiry had already passed when the contract processed the call. The funds were sent back.'
    return 'refused by the contract. The funds were sent back.'
  }
  const entry = scan.entries.get(call.id)
  if (!entry) return 'refused: there is no such entry'
  const closed = entry.closedBy && entry.closedBy.height < event.height
  if (closed) return `refused: the entry was already ${entry.state === 'paid' ? 'paid' : 'returned'}`
  if (call.method === 'Reclaim') {
    if (entry.creator !== activity.signerAddress) return 'refused: only the creator can reclaim'
    if (at < entry.expirationTime) return `refused: the entry was not expired (${entry.expirationTime - at} s to go at that momentum)`
    return 'refused by the contract'
  }
  if (at >= entry.expirationTime) return 'refused: the entry had expired'
  const destination = call.method === 'ProxyUnlock' ? call.destination : activity.signerAddress
  if (entry.destination !== EMPTY_ADDRESS && entry.destination !== destination) {
    return `refused: this entry pays only ${nameOf(entry.destination) ?? entry.destination}`
  }
  return 'refused: the witness does not open the lock'
}

// ---- common blocks ---------------------------------------------------------------

export function contractCall(
  signer: SignerId,
  method: MethodName,
  data: Uint8Array,
  label: string,
  funds?: { amount: bigint; tokenStandard: string },
): Promise<Activity> {
  return submit(
    signer,
    { toAddress: PTLC_CONTRACT, data, amount: funds?.amount ?? 0n, tokenStandard: funds?.tokenStandard ?? ZNN_ZTS },
    { label, kind: method },
  )
}

export function transfer(signer: SignerId, to: string, amount: bigint, tokenStandard: string): Promise<Activity> {
  return submit(
    signer,
    { toAddress: to, amount, tokenStandard },
    { label: `Send ${formatAmount(amount, tokenStandard)} to ${nameOf(to) ?? to}`, kind: 'Send' },
  )
}

/** Fuses the signer's QSR into plasma for `beneficiary`, so its blocks need no proof of work. */
export function fuse(signer: SignerId, beneficiary: string, amount: bigint): Promise<Activity> {
  return submit(
    signer,
    {
      toAddress: PLASMA_CONTRACT,
      amount,
      tokenStandard: QSR_ZTS,
      data: encodeCall('Fuse', ['address'], [beneficiary]),
    },
    { label: `Fuse ${formatAmount(amount, QSR_ZTS)} for ${nameOf(beneficiary) ?? beneficiary}`, kind: 'Fuse' },
  )
}

/**
 * Publishes a receive block for everything waiting on a lab key. Nothing on
 * Zenon credits an account without one: a payout from the contract is an
 * unreceived block until its destination answers it.
 */
export async function receiveAll(signer: SignerId): Promise<number> {
  const key = labKey(signer)
  if (!key) throw new Error('Syrius receives by itself when its dashboard is open')
  const waiting = await client.unreceivedBlocks(key.account.address)
  for (const block of waiting) {
    const activity = await submit(
      signer,
      { blockType: BLOCK_TYPE_USER_RECEIVE, toAddress: EMPTY_ADDRESS, fromBlockHash: block.hash },
      { label: `Receive ${formatAmount(block.amount, block.tokenStandard)} for ${key.name}`, kind: 'Receive' },
    )
    await settled(activity)
  }
  void refreshAccounts()
  return waiting.length
}

// ---- secrets ---------------------------------------------------------------------

const SECRETS_KEY = 'ptlc-lab.secrets.v1'

function saveSecrets(): void {
  try {
    localStorage.setItem(SECRETS_KEY, JSON.stringify(state.secrets))
  } catch {
    /* kept for this page load only */
  }
}

export function addSecret(name: string, pointType: number, secret: Uint8Array, origin: Secret['origin']): Secret {
  const lock = toHex(lockOf(pointType, secret))
  const existing = state.secrets.find((s) => s.pointType === pointType && s.lock === lock)
  if (existing) return existing
  const item: Secret = {
    id: `${Date.now().toString(36)}-${Math.floor(Math.random() * 1e6).toString(36)}`,
    name,
    pointType,
    secret: toHex(secret),
    lock,
    createdAt: Date.now(),
    origin,
  }
  state.secrets.unshift(item)
  saveSecrets()
  return state.secrets[0]
}

export function removeSecret(id: string): void {
  state.secrets = state.secrets.filter((s) => s.id !== id)
  saveSecrets()
}

export function secretFor(pointType: number, lockHex: string): Secret | undefined {
  return state.secrets.find((s) => s.pointType === pointType && s.lock === lockHex.toLowerCase())
}

// ---- polling ---------------------------------------------------------------------

const scan = emptyScan()

function publishScan(): void {
  state.scanHeight = scan.height
  state.events = scan.events.slice().reverse()
  state.entries = entriesForView(scan)
}

function resetScan(): void {
  const fresh = emptyScan()
  scan.height = fresh.height
  scan.events = fresh.events
  scan.entries = fresh.entries
  publishScan()
}

async function scanContract(): Promise<void> {
  const frontier = await poller.frontierAccountBlock(PTLC_CONTRACT)
  if (!frontier) {
    if (scan.height > 0) resetScan()
    return
  }
  if (frontier.height < scan.height) resetScan()
  let changed = false
  while (scan.height < frontier.height) {
    const page = await poller.accountBlocksByHeight(PTLC_CONTRACT, scan.height + 1, 40)
    // A block no momentum has confirmed yet can still be replaced; it is
    // folded in once it has one.
    const confirmed = []
    for (const block of page) {
      if (!block.confirmationDetail?.momentumHeight) break
      confirmed.push(block)
    }
    if (confirmed.length === 0) break
    const before = scan.height
    foldBlocks(scan, confirmed)
    changed = true
    if (scan.height === before) break
  }
  if (changed) publishScan()
}

export function trackedAddresses(): string[] {
  const list = labKeys.map((k) => k.account.address)
  if (state.wallet.address && !list.includes(state.wallet.address)) list.push(state.wallet.address)
  return list
}

export async function refreshAccounts(): Promise<void> {
  await Promise.all(
    trackedAddresses().map(async (address) => {
      try {
        const [info, plasma, unreceived] = await Promise.all([
          poller.accountInfo(address),
          poller.plasma(address),
          poller.unreceivedBlocks(address, 50),
        ])
        const balances: Record<string, string> = {}
        for (const [zts, entry] of Object.entries(info.balanceInfoMap ?? {})) {
          balances[zts] = entry.balance
          if (!tokenOf(zts)) state.tokens[zts] = entry.token
        }
        state.accounts[address] = {
          address,
          height: info.accountHeight ?? 0,
          balances,
          plasma: plasma.currentPlasma,
          maxPlasma: plasma.maxPlasma,
          unreceived: unreceived.length,
        }
      } catch {
        /* an account the node has never seen answers with zeros, not an error;
           a failure here is the node, and nodeError already says so */
      }
    }),
  )
}

async function checkSpork(): Promise<void> {
  try {
    const sporks = await poller.sporks()
    state.spork = sporks.some((s) => s.id === PTLC_SPORK_ID && s.activated)
  } catch {
    state.spork = null
  }
}

/** The strip chart's history, so that it is not empty for the first five minutes. */
async function loadSlots(frontier: Momentum): Promise<void> {
  const count = Math.min(24, frontier.height - 1)
  if (count < 2) return
  try {
    const page = await poller.call<{ list: Momentum[] | null }>('ledger.getMomentumsByHeight', [
      frontier.height - count,
      count + 1,
    ])
    const list = page.list ?? []
    const slots: number[] = []
    for (let i = 1; i < list.length; i++) slots.push(list[i].timestamp - list[i - 1].timestamp)
    if (state.slots.length < slots.length) state.slots = slots
  } catch {
    /* the chart fills in as momentums arrive */
  }
}

async function checkPeers(): Promise<void> {
  try {
    state.peers = (await poller.call<{ numPeers: number }>('stats.networkInfo')).numPeers
  } catch {
    state.peers = null
  }
}

async function onMomentum(): Promise<void> {
  await Promise.allSettled([scanContract(), refreshAccounts(), refreshWallet(), checkPeers()])
  await advanceActivities()
  if (state.spork !== true) await checkSpork()
}

let polling = false
async function tick(): Promise<void> {
  if (polling) return
  polling = true
  try {
    const m = await poller.frontierMomentum()
    state.nodeError = ''
    const previous = state.momentum
    if (!previous || m.hash !== previous.hash) {
      if (previous && m.height < previous.height) {
        // The devnet was wiped and started again under the page.
        resetScan()
        state.slots = []
        state.spork = null
      } else if (previous && m.height > previous.height) {
        const each = (m.timestamp - previous.timestamp) / (m.height - previous.height)
        state.slots.push(each)
        if (state.slots.length > 30) state.slots.shift()
      }
      state.momentum = m
      state.momentumSeenAt = Date.now()
      if (!previous || m.height < previous.height) void loadSlots(m)
      await onMomentum()
    } else {
      await advanceActivities()
    }
  } catch (err) {
    state.nodeError = err instanceof Error ? err.message : String(err)
  } finally {
    polling = false
  }
}

async function pollLab(): Promise<void> {
  try {
    const { runs, busy } = await labd.listRuns()
    state.runs = runs
    state.busyRun = busy ?? null
    state.labdError = ''
  } catch (err) {
    state.labdError = err instanceof Error ? err.message : String(err)
  }
  try {
    state.btcHeight = await labd.btc<number>('getblockcount')
    state.btcError = ''
  } catch (err) {
    state.btcHeight = null
    state.btcError = err instanceof Error ? err.message : String(err)
  }
}

// ---- the wallet extension -----------------------------------------------------------

let probedNode = ''

export async function refreshWallet(): Promise<void> {
  const fresh = await syrius.readState()
  const w = state.wallet
  const nodeChanged = fresh.nodeUrl !== w.nodeUrl
  Object.assign(w, fresh)
  if (!fresh.nodeUrl) {
    w.sameChain = null
    w.sameChainDetail = ''
    probedNode = ''
    return
  }
  if (nodeChanged || probedNode !== fresh.nodeUrl) await probeWalletChain()
}

export async function probeWalletChain(): Promise<void> {
  const w = state.wallet
  if (!w.nodeUrl || !state.momentum) return
  probedNode = w.nodeUrl
  const result = await syrius.probeSameChain(w.nodeUrl, state.momentum.height, async (height) => {
    const page = await poller.call<{ list: { hash: string }[] | null }>('ledger.getMomentumsByHeight', [height, 1])
    return page.list?.[0]?.hash ?? null
  })
  w.sameChain = result.verdict
  w.sameChainDetail = result.detail
}

export async function connectWallet(): Promise<void> {
  const w = state.wallet
  w.busy = true
  w.error = ''
  try {
    Object.assign(
      w,
      await syrius.connect({
        onHold: (seconds) => {
          w.error = seconds > 0 ? `Syrius does not raise a second window for one site within 30 s of one that was declined or closed. Nothing was shown or signed; asking again in ${seconds} s.` : ''
        },
      }),
    )
    await probeWalletChain()
    await refreshAccounts()
  } catch (err) {
    w.error = err instanceof Error ? err.message : String(err)
  } finally {
    w.busy = false
  }
}

export async function disconnectWallet(): Promise<void> {
  const w = state.wallet
  w.error = ''
  try {
    await syrius.disconnect()
  } catch (err) {
    w.error = err instanceof Error ? err.message : String(err)
  }
  await refreshWallet()
}

// ---- start -----------------------------------------------------------------------

let started = false

export function start(): void {
  if (started) return
  started = true

  try {
    const stored = JSON.parse(localStorage.getItem(SECRETS_KEY) ?? '[]')
    if (Array.isArray(stored)) state.secrets = stored
    const signer = localStorage.getItem('ptlc-lab.signer') as SignerId | null
    if (signer && signerIds.includes(signer)) state.signer = signer
  } catch {
    /* start empty */
  }

  client.onCall = (entry) => {
    state.rpcLog.unshift(entry)
    if (state.rpcLog.length > 150) state.rpcLog.length = 150
  }

  labd
    .getConfig()
    .then((config) => {
      state.config = config
    })
    .catch((err) => {
      state.labdError = err instanceof Error ? err.message : String(err)
    })

  setInterval(() => {
    state.now = Date.now()
  }, 1000)
  void tick()
  setInterval(() => void tick(), 2000)
  void pollLab()
  setInterval(() => void pollLab(), 4000)

  void syrius.whenProviderReady().then(async () => {
    await refreshWallet()
    syrius.onWalletChange(() => {
      void refreshWallet().then(refreshAccounts)
    })
  })
}

export type { BlockJson, BlockSpec, ContractEvent, Entry }
