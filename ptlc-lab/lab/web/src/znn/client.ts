// A JSON-RPC client for a go-zenon node, covering the calls the lab makes.
//
// The page talks to the node through labd at /rpc, so there is one origin and
// no cross-origin rule to meet. Every call is also kept in a short log, which
// is what the Raw RPC bench shows: the lab's own traffic is one of the ways
// the contract is being talked to.

export interface Momentum {
  hash: string
  height: number
  timestamp: number
  chainIdentifier: number
  producer?: string
}

export interface TokenInfo {
  name: string
  symbol: string
  decimals: number
  tokenStandard: string
}

export interface AccountBlockJson {
  hash: string
  previousHash: string
  address: string
  toAddress: string
  blockType: number
  height: number
  /** Base64. */
  data: string
  /** A decimal string: see `common.StringToBigInt`. */
  amount: string
  tokenStandard: string
  fromBlockHash: string
  descendantBlocks?: AccountBlockJson[]
  token?: TokenInfo | null
  confirmationDetail?: { momentumHeight: number; momentumTimestamp: number; numConfirmations: number } | null
  pairedAccountBlock?: AccountBlockJson | null
}

/** `embedded.ptlc.getById`. `pointLock` is base64, as go-zenon marshals `[]byte`. */
export interface PtlcInfo {
  id: string
  timeLocked: string
  tokenStandard: string
  amount: string
  expirationTime: number
  pointType: number
  pointLock: string
  destination: string
}

export interface RequiredPow {
  availablePlasma: number
  basePlasma: number
  requiredDifficulty: number
}

export interface AccountInfo {
  address: string
  accountHeight: number
  balanceInfoMap: Record<string, { token: TokenInfo; balance: string }> | null
}

export interface Spork {
  id: string
  name: string
  activated: boolean
  enforcementHeight: number
}

export class RpcError extends Error {
  readonly code: number
  constructor(code: number, message: string) {
    super(message)
    this.code = code
    this.name = 'RpcError'
  }
  /** The node's way of saying "no such entry": an expected answer, not a fault. */
  get isNotFound(): boolean {
    return /data non existent/i.test(this.message)
  }
}

export interface RpcLogEntry {
  n: number
  at: number
  method: string
  params: unknown[]
  ms: number
  ok: boolean
  /** The result, or the error message. */
  answer: unknown
}

export class ZenonClient {
  readonly url: string
  private id = 0
  /** Called after every call, for the Raw RPC bench. */
  onCall?: (entry: RpcLogEntry) => void

  constructor(url: string) {
    this.url = url
  }

  async call<T>(method: string, params: unknown[] = []): Promise<T> {
    const started = performance.now()
    const n = ++this.id
    const done = (ok: boolean, answer: unknown) =>
      this.onCall?.({ n, at: Date.now(), method, params, ms: Math.round(performance.now() - started), ok, answer })
    let body: { result?: T; error?: { code: number; message: string } }
    try {
      const response = await fetch(this.url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: n, method, params }),
      })
      body = await response.json()
      if (!response.ok && !body.error) {
        throw new Error((body as { error?: string }).error ?? `the node answered HTTP ${response.status}`)
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      done(false, message)
      throw new Error(`${method}: ${message}`)
    }
    if (body.error) {
      const message = typeof body.error === 'string' ? body.error : body.error.message
      done(false, message)
      throw new RpcError(typeof body.error === 'string' ? 0 : body.error.code, message)
    }
    done(true, body.result)
    return body.result as T
  }

  /** `null` rather than a throw, because "no entry" is a normal answer here. */
  private async callOrNull<T>(method: string, params: unknown[]): Promise<T | null> {
    try {
      return await this.call<T>(method, params)
    } catch (err) {
      if (err instanceof RpcError && err.isNotFound) return null
      throw err
    }
  }

  frontierMomentum(): Promise<Momentum> {
    return this.call('ledger.getFrontierMomentum')
  }

  frontierAccountBlock(address: string): Promise<AccountBlockJson | null> {
    return this.callOrNull('ledger.getFrontierAccountBlock', [address])
  }

  accountBlockByHash(hash: string): Promise<AccountBlockJson | null> {
    return this.callOrNull('ledger.getAccountBlockByHash', [hash])
  }

  async accountBlocksByHeight(address: string, from: number, count: number): Promise<AccountBlockJson[]> {
    const page = await this.call<{ list: AccountBlockJson[] | null }>('ledger.getAccountBlocksByHeight', [
      address,
      from,
      count,
    ])
    return page.list ?? []
  }

  async unreceivedBlocks(address: string, pageSize = 50): Promise<AccountBlockJson[]> {
    const page = await this.call<{ list: AccountBlockJson[] | null }>('ledger.getUnreceivedBlocksByAddress', [
      address,
      0,
      pageSize,
    ])
    return page.list ?? []
  }

  accountInfo(address: string): Promise<AccountInfo> {
    return this.call('ledger.getAccountInfoByAddress', [address])
  }

  plasma(address: string): Promise<{ currentPlasma: number; maxPlasma: number; qsrAmount: string }> {
    return this.call('embedded.plasma.get', [address])
  }

  requiredPow(address: string, blockType: number, toAddress: string, dataBase64: string): Promise<RequiredPow> {
    return this.call('embedded.plasma.getRequiredPoWForAccountBlock', [
      { address, blockType, toAddress, data: dataBase64 },
    ])
  }

  publish(block: unknown): Promise<null> {
    return this.call('ledger.publishRawTransaction', [block])
  }

  ptlcById(id: string): Promise<PtlcInfo | null> {
    return this.callOrNull('embedded.ptlc.getById', [id])
  }

  async sporks(): Promise<Spork[]> {
    const page = await this.call<{ list: Spork[] | null }>('embedded.spork.getAll', [0, 50])
    return page.list ?? []
  }

  /**
   * An account block is unreadable for a momentum or two after it is published,
   * so "not found yet" is the normal first answer rather than a failure.
   * Resolves once the block exists and a momentum has confirmed it.
   */
  async waitForConfirmation(
    hash: string,
    { timeoutMs = 180_000, intervalMs = 2_000, signal }: WaitOptions = {},
  ): Promise<AccountBlockJson> {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      signal?.throwIfAborted()
      const block = await this.accountBlockByHash(hash)
      if (block?.confirmationDetail?.momentumHeight) return block
      if (Date.now() > deadline) {
        throw new Error(`block ${hash.slice(0, 8)}… was not confirmed within ${Math.round(timeoutMs / 1000)} s`)
      }
      await delay(intervalMs, signal)
    }
  }
}

export interface WaitOptions {
  timeoutMs?: number
  intervalMs?: number
  signal?: AbortSignal
}

export function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason)
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    const onAbort = () => {
      clearTimeout(timer)
      reject(signal!.reason)
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}
