// The Syrius browser extension, as the lab uses it. Written for v0.3.4 and
// later; README.md lists what was read out of that version and what was run
// against it.
//
// Syrius does not know the ptlc contract. It does not need to: underneath its
// own screens it is a signer, and it will sign any account block a page hands
// it. A ptlc call is a send to an embedded contract carrying encoded
// arguments, so:
//
//   - the page decides toAddress, amount, tokenStandard and data;
//   - the extension supplies the account, the public key, the chain position,
//     the plasma or proof of work, and the signature.
//
// What v0.3.4 changed for a page, and what this file does about each:
//
//   - A site sees no address, chain or node until it is approved, and after
//     that only the node's host. So nothing is read as "wrong" before connect,
//     and the same-chain check below dials the host rather than a full URL.
//   - An approval is single use, expires after 30 minutes, and belongs to the
//     document that asked: a navigation or a rewritten document cancels it.
//     The lab is one document routed by its fragment, which is not a
//     navigation, and it never reloads itself.
//   - At most two approvals may wait per origin. Requests that prompt go
//     through one queue here, so a second is not sent while one is open.
//   - The wallet will not raise a second window for one site within 30 s of
//     the last, unless the last was approved (which buys one follow-up). A
//     request that arrives inside that time is refused at once with -32005
//     and no window. Nothing was shown and nothing was signed, so the request
//     is held here until the 30 s have passed and then made again.
//   - The approval screen decodes calls to the contracts it knows and shows
//     any other as "unrecognized", with a warning. ptlc is one of the
//     others. That is a warning, not a refusal; the block is signed as shown.
//   - The wallet prepares the block once, shows it, and signs exactly that.
//     If the account's frontier moves while the window is open the node
//     refuses the block; the lab reports that as itself and the call is
//     simply made again.
//   - `signMessage` refuses a 32-byte message, because that is the size of a
//     block hash. The unlock message of a key-type lock is 32 bytes, so a
//     Syrius account's own key cannot be the lock of an Ed25519 entry opened
//     through the extension. Lab keys hold those secrets instead.

import type { BlockJson } from '../znn/block.ts'

export interface ZenonProvider {
  version?: number | string
  isSyriusExtension?: boolean
  connect(): Promise<unknown>
  disconnect(): Promise<unknown>
  getAccounts(): Promise<string[]>
  getChainId(): Promise<number | string | null>
  getNodeUrl(): Promise<string | null>
  sendAccountBlock(block: unknown): Promise<SendAccountBlockResult>
  signMessage?(message: string): Promise<SignMessageResult>
  on?(event: string, handler: (...args: unknown[]) => void): void
  removeListener?(event: string, handler: (...args: unknown[]) => void): void
}

/**
 * v0.2.0 changed this from `{signedTransaction}` to `{hash, block}`. Both are
 * read, so that the hash is found whichever arrived.
 */
export interface SendAccountBlockResult {
  hash?: string
  block?: Partial<BlockJson>
  signedTransaction?: Partial<BlockJson>
}

export interface SignMessageResult {
  message: string
  address: string
  publicKey: string
  signature: string
}

declare global {
  interface Window {
    zenon?: ZenonProvider
    ethereum?: { syrius?: { version?: string; zvm?: number } }
  }
}

export class WalletError extends Error {
  readonly code: number
  constructor(code: number, message: string) {
    super(message)
    this.code = code
    this.name = 'WalletError'
  }
  get declined(): boolean {
    return this.code === 4001
  }
}

/** Thrown here, before any request reaches an extension; never a provider code. */
export const WALLET_NOT_FOUND = -1

/**
 * Rejections arrive as `{code, message}`, a plain object and not an Error. The
 * wallet's own sentence is kept whenever it has one: it names the cause (the
 * account changed, the approval expired, the page left) better than a code.
 */
function translate(err: unknown): WalletError {
  if (err instanceof WalletError) return err
  const raw = err as { code?: number; message?: string } | undefined
  const code = typeof raw?.code === 'number' ? raw.code : 0
  const detail = (raw?.message ?? String(err)).trim()
  switch (code) {
    case 4001:
      return new WalletError(code, 'declined in Syrius')
    case 4100:
      return new WalletError(code, 'Syrius is not connected to this page for the account it is on. Connect again.')
    case 4200:
      return new WalletError(code, 'this version of Syrius does not support that request')
    case 4900:
      return new WalletError(code, detail || 'Syrius did not answer')
    case -32005:
      return new WalletError(
        code,
        'Syrius would not open a window: it holds a site off for 30 s after an approval that was declined or closed, and it queues at most two requests per site. Wait half a minute and send it again.',
      )
    default:
      return new WalletError(code, detail || 'Syrius failed without saying why')
  }
}

/**
 * Presence is advisory. The honest test of whether a wallet is there is to ask
 * it and see whether it answers; this only decides what the page says.
 */
export function providerPresent(): boolean {
  return typeof window !== 'undefined' && typeof window.zenon?.sendAccountBlock === 'function'
}

function provider(): ZenonProvider {
  const p = window.zenon
  if (!p) {
    throw new WalletError(
      WALLET_NOT_FOUND,
      'no Zenon wallet in this browser. Install the Syrius extension (v0.3.4 or later) and reload.',
    )
  }
  return p
}

/** Resolves when `window.zenon` exists, or after `ms` if it never does. */
export function whenProviderReady(ms = 1500): Promise<boolean> {
  if (providerPresent()) return Promise.resolve(true)
  return new Promise((resolve) => {
    const done = () => {
      window.removeEventListener('zenon#initialized', done)
      clearTimeout(timer)
      resolve(providerPresent())
    }
    // The provider is injected at document_start, so it is normally already
    // there. The event covers an extension enabled after the page loaded.
    window.addEventListener('zenon#initialized', done)
    const timer = setTimeout(done, ms)
  })
}

/**
 * A reactive object cannot cross `postMessage`: it is structured clone, and a
 * Proxy fails it with "could not be cloned", naming no field. Everything the
 * lab holds lives in Vue state, so the round trip happens here, at the
 * transport boundary, for every request.
 */
function plain<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

// One prompting request at a time. The extension admits two per origin and
// answers a third with -32005; one is what a person can read anyway.
let queue: Promise<unknown> = Promise.resolve()
function exclusive<T>(job: () => Promise<T>): Promise<T> {
  const next = queue.then(job, job)
  queue = next.catch(() => undefined)
  return next
}

/** How long Syrius holds a site off after a window it did not get a yes in. */
const WINDOW_COOLDOWN_MS = 30_000
let lastAskedAt = 0

export interface PromptOptions {
  /** Called once a second while a request is being held back, with the seconds left. */
  onHold?: (secondsLeft: number) => void
}

/**
 * Makes a request that raises a wallet window, and makes it again, once, if
 * the wallet refused to raise one because of its per-site cooldown.
 */
async function prompting<T>(ask: () => Promise<T>, options: PromptOptions = {}): Promise<T> {
  for (let attempt = 0; ; attempt++) {
    const previous = lastAskedAt
    lastAskedAt = Date.now()
    try {
      return await ask()
    } catch (err) {
      const code = (err as { code?: number } | undefined)?.code
      if (code !== -32005 || attempt >= 2) throw translate(err)
      // The cooldown runs from when the last window opened, which is when it
      // was last asked for; a second and a half on top covers the round trip.
      const since = previous ? Date.now() - previous : 0
      let wait = Math.max(2500, WINDOW_COOLDOWN_MS - since + 1500)
      if (attempt > 0) wait = WINDOW_COOLDOWN_MS + 1500
      const until = Date.now() + wait
      while (Date.now() < until) {
        options.onHold?.(Math.ceil((until - Date.now()) / 1000))
        await new Promise((resolve) => setTimeout(resolve, 1000))
      }
      options.onHold?.(0)
    }
  }
}

export interface WalletState {
  present: boolean
  /** The provider protocol, not the extension's release: 2 since v0.2.0. */
  protocol: string | null
  /** Only v0.4.0 and later say, through `window.ethereum.syrius`. */
  extensionVersion: string | null
  address: string | null
  chainId: number | null
  /** `ws://host:port` and nothing more, since v0.3.4. */
  nodeUrl: string | null
  canSignMessage: boolean
}

/**
 * Reads the wallet without prompting. Before this origin is connected, and
 * whenever the wallet is on an account this origin was not approved for, every
 * one of these is empty; that is the wallet declining to say, not a fault.
 */
export async function readState(): Promise<WalletState> {
  const p = window.zenon
  const state: WalletState = {
    present: providerPresent(),
    protocol: p?.version === undefined ? null : String(p.version),
    extensionVersion: window.ethereum?.syrius?.version ?? null,
    address: null,
    chainId: null,
    nodeUrl: null,
    canSignMessage: typeof p?.signMessage === 'function',
  }
  if (!p) return state
  const ask = async <T>(job: () => Promise<T>, fallback: T): Promise<T> => {
    try {
      return await job()
    } catch {
      return fallback
    }
  }
  const [accounts, chainId, nodeUrl] = await Promise.all([
    ask(() => p.getAccounts(), [] as string[]),
    ask(() => p.getChainId(), null),
    ask(() => p.getNodeUrl(), null),
  ])
  state.address = accounts?.[0] ?? null
  state.chainId = chainId === null || chainId === undefined ? null : Number(chainId)
  state.nodeUrl = nodeUrl ?? null
  return state
}

export function connect(options: PromptOptions = {}): Promise<WalletState> {
  return exclusive(async () => {
    const p = provider()
    await prompting(() => p.connect(), options)
    return readState()
  })
}

export async function disconnect(): Promise<void> {
  try {
    await provider().disconnect()
  } catch (err) {
    throw translate(err)
  }
}

export interface SentBlock {
  hash: string
  /** The block as the wallet signed it, when it reported one. */
  block: Partial<BlockJson> | null
}

/**
 * Hands a complete template to the extension to fill in, show, sign and
 * publish. Resolves when the wallet has published; the block is then on its
 * node and one or two momentums from being readable.
 */
export function sendAccountBlock(template: BlockJson, options: PromptOptions = {}): Promise<SentBlock> {
  return exclusive(async () => {
    const p = provider()
    const result: SendAccountBlockResult = await prompting(() => p.sendAccountBlock(plain(template)), options)
    const block = result?.block ?? result?.signedTransaction ?? null
    const hash = result?.hash ?? block?.hash
    if (!hash) throw new WalletError(-32603, 'Syrius published the block but reported no hash for it')
    return { hash: String(hash), block }
  })
}

/**
 * The fields the page chose, compared with what the wallet says it signed. The
 * wallet fills in height, plasma, nonce and signature, and those are not
 * compared. A field the wallet did not report is not judged; a `data` reported
 * empty is a contract call stripped of its arguments, and is.
 */
export function signedBlockDiffers(template: BlockJson, signed: Partial<BlockJson> | null): string[] {
  if (!signed) return []
  const differences: string[] = []
  const fields = ['chainIdentifier', 'blockType', 'toAddress', 'amount', 'tokenStandard', 'data', 'fromBlockHash'] as const
  for (const field of fields) {
    if (signed[field] === undefined || signed[field] === null) continue
    if (String(signed[field]) !== String(template[field])) {
      differences.push(`${field}: asked for ${String(template[field]) || '(empty)'}, signed ${String(signed[field]) || '(empty)'}`)
    }
  }
  return differences
}

export function signMessage(message: string, options: PromptOptions = {}): Promise<SignMessageResult> {
  return exclusive(async () => {
    const p = provider()
    if (typeof p.signMessage !== 'function') {
      throw new WalletError(4200, 'this version of Syrius has no signMessage; it arrived in v0.3.1')
    }
    return prompting(() => p.signMessage!(message), options)
  })
}

/** The extension announces these. Anything the page prepared is stale after each. */
export function onWalletChange(handler: (event: string) => void): () => void {
  const events = ['accountsChanged', 'chainChanged', 'nodeChanged', 'disconnect']
  const p = window.zenon
  if (!p?.on) return () => {}
  const handlers = events.map((event) => {
    const h = () => handler(event)
    p.on!(event, h)
    return [event, h] as const
  })
  return () => {
    for (const [event, h] of handlers) p.removeListener?.(event, h)
  }
}

export type SameChain = 'same' | 'different' | 'unreachable'

/**
 * Whether the wallet's node and the lab's node are one chain.
 *
 * Equal chain identifiers prove nothing: every go-zenon devnet anyone starts
 * is chain 69, and a machine can run several. So a momentum hash is read from
 * both at one height and compared. `hashAt` reads the lab's own node.
 *
 * The wallet reports only `ws://host:port`, which is enough to dial it. A node
 * that cannot be dialled from the page is reported as unreachable, never as
 * the same chain.
 */
export function probeSameChain(
  nodeUrl: string,
  labHeight: number,
  hashAt: (height: number) => Promise<string | null>,
  timeoutMs = 6000,
): Promise<{ verdict: SameChain; detail: string }> {
  return new Promise((resolve) => {
    let settled = false
    let socket: WebSocket
    const finish = (verdict: SameChain, detail: string) => {
      if (settled) return
      settled = true
      clearTimeout(timer)
      try {
        socket?.close()
      } catch {
        /* already closed */
      }
      resolve({ verdict, detail })
    }
    const timer = setTimeout(() => finish('unreachable', `${nodeUrl} did not answer within ${timeoutMs / 1000} s`), timeoutMs)
    try {
      socket = new WebSocket(nodeUrl)
    } catch (err) {
      finish('unreachable', `${nodeUrl} cannot be dialled from this page: ${err instanceof Error ? err.message : err}`)
      return
    }
    socket.onerror = () => finish('unreachable', `${nodeUrl} cannot be reached from this page`)
    socket.onopen = () => socket.send(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'ledger.getFrontierMomentum', params: [] }))
    let anchor = 0
    socket.onmessage = async (message) => {
      try {
        const body = JSON.parse(String(message.data))
        if (body.error) return finish('unreachable', `${nodeUrl} answered with an error: ${body.error.message}`)
        if (body.id === 1) {
          // A few momentums below the lower tip, so a node that is merely
          // behind still holds the momentum being asked about.
          anchor = Math.max(1, Math.min(Number(body.result.height), labHeight) - 3)
          socket.send(JSON.stringify({ jsonrpc: '2.0', id: 2, method: 'ledger.getMomentumsByHeight', params: [anchor, 1] }))
          return
        }
        const theirs: string | undefined = body.result?.list?.[0]?.hash
        const ours = await hashAt(anchor)
        if (!theirs || !ours) return finish('unreachable', `momentum ${anchor} could not be read from both nodes`)
        if (theirs === ours) finish('same', `momentum ${anchor} has the same hash on both nodes`)
        else finish('different', `momentum ${anchor} is ${theirs.slice(0, 12)}… on the wallet's node and ${ours.slice(0, 12)}… on the lab's`)
      } catch (err) {
        finish('unreachable', `${nodeUrl} sent something unreadable: ${err instanceof Error ? err.message : err}`)
      }
    }
  })
}
