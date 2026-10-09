// labd, from the page: what the lab is made of, the programs it can run, and
// the read-only window on bitcoind.

export interface LabConfig {
  chainId: string
  /** Whether the page was reached by the lab's published name, where every visitor shares its keys. */
  public: boolean
  /** What a browser wallet should dial: loopback on the lab's own machine, the published name elsewhere. */
  walletNode: string
  /** The node's HTTP RPC for a client outside the lab, by the same rule. */
  nodeHttp: string
  cliNode: string
  keystore: string
  bitcoind: string
  cli: boolean
  nomToBtc: boolean
  nomToNom: boolean
  goZenonBase: string
  dartSdkBase: string
  dartCliBase: string
}

export interface RunSummary {
  id: string
  kind: 'cli' | 'nom-to-nom' | 'nom-to-btc'
  title: string
  command: string[]
  started: number
  ended?: number
  exit?: number
  lines: number
}

export interface RunLine {
  n: number
  at: number
  stream: 'out' | 'err' | 'lab'
  text: string
}

export type StartRun =
  | { kind: 'cli'; index: number; args: string[] }
  | { kind: 'nom-to-nom'; znnAmount?: number; qsrAmount?: number }
  | { kind: 'nom-to-btc'; scenario: 'happy' | 'refund'; sats?: number; znn?: number; expiry?: number; locktime?: number }

async function json<T>(response: Response): Promise<T> {
  let body: unknown
  try {
    body = await response.json()
  } catch {
    throw new Error(`labd answered HTTP ${response.status} with something that is not JSON`)
  }
  if (!response.ok) {
    throw new Error((body as { error?: string })?.error ?? `labd answered HTTP ${response.status}`)
  }
  return body as T
}

export async function getConfig(): Promise<LabConfig> {
  return json(await fetch('/api/config'))
}

export async function listRuns(): Promise<{ runs: RunSummary[]; busy?: { id: string; title: string } }> {
  return json(await fetch('/api/runs'))
}

export async function startRun(request: StartRun): Promise<RunSummary> {
  return json(
    await fetch('/api/runs', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(request),
    }),
  )
}

export async function stopRun(id: string): Promise<RunSummary> {
  return json(await fetch(`/api/runs/${id}`, { method: 'DELETE' }))
}

export async function getRun(id: string): Promise<RunSummary & { output: RunLine[] }> {
  return json(await fetch(`/api/runs/${id}`))
}

/**
 * Follows a run from its first line. EventSource reconnects by itself and
 * resumes after the last line it saw, so a dropped connection does not print
 * the run twice. Returns the function that stops following.
 */
export function followRun(
  id: string,
  onLine: (line: RunLine) => void,
  onDone: (exit: number) => void,
): () => void {
  const source = new EventSource(`/api/runs/${id}/stream`)
  source.onmessage = (event) => onLine(JSON.parse(event.data) as RunLine)
  source.addEventListener('done', (event) => {
    source.close()
    onDone((JSON.parse((event as MessageEvent).data) as { exit: number }).exit)
  })
  return () => source.close()
}

export async function btc<T>(method: string, params: unknown[] = []): Promise<T> {
  const body = await json<{ result?: T; error?: { message: string } | string | null }>(
    await fetch('/api/btc', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ method, params }),
    }),
  )
  if (body.error) throw new Error(typeof body.error === 'string' ? body.error : body.error.message)
  return body.result as T
}
